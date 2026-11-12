package watcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/filter"
	"feedwatch/internal/notifier"
	"feedwatch/internal/source"
	"feedwatch/internal/store"
)

var fixedNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func rel(id, title string, at time.Time) domain.Release {
	return domain.Release{
		ID: id, Title: title, Version: title,
		URL: "https://example.com/" + title, PublishedAt: at, IsStable: true,
	}
}

func res(rs ...domain.Release) source.Result {
	return source.Result{Releases: rs, ETag: `"e-1"`, LastModified: "Tue, 10 Nov 2026 11:00:00 GMT"}
}

type step struct {
	result source.Result
	err    error
}

// fakeSource replays a scripted sequence; once exhausted it keeps returning
// the last step, which models "the feed did not change".
type fakeSource struct {
	steps []step
	idx   int
}

func (f *fakeSource) Type() string { return "fake" }

func (f *fakeSource) Fetch(_ context.Context, _ source.Cursor) (source.Result, error) {
	if len(f.steps) == 0 {
		return source.Result{}, nil
	}
	i := f.idx
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	} else {
		f.idx++
	}
	return f.steps[i].result, f.steps[i].err
}

func cloneState(s domain.SeenState) domain.SeenState {
	out := s
	out.Feeds = make(map[string]*domain.FeedState, len(s.Feeds))
	for k, fs := range s.Feeds {
		if fs == nil {
			out.Feeds[k] = nil
			continue
		}
		c := *fs
		c.SeenIDs = make(map[string]time.Time, len(fs.SeenIDs))
		maps.Copy(c.SeenIDs, fs.SeenIDs)
		out.Feeds[k] = &c
	}
	return out
}

type memStore struct {
	state     domain.SeenState
	exists    bool
	loadErr   error
	saveCalls int
	loadCalls int
}

func (m *memStore) Load(context.Context) (domain.SeenState, error) {
	m.loadCalls++
	if m.loadErr != nil {
		return domain.SeenState{}, m.loadErr
	}
	if !m.exists {
		return domain.SeenState{}, store.ErrNotFound
	}
	return cloneState(m.state), nil
}

func (m *memStore) Save(_ context.Context, s domain.SeenState) error {
	m.saveCalls++
	m.state = cloneState(s)
	m.exists = true
	return nil
}

type recordingNotifier struct {
	notes []domain.Notification
	fail  bool
}

func (r *recordingNotifier) Type() string { return "fake" }

func (r *recordingNotifier) Send(_ context.Context, n domain.Notification) error {
	if r.fail {
		return errors.New("send failed")
	}
	r.notes = append(r.notes, n)
	return nil
}

// selectiveNotifier fails only one per_feed batch, so partial-failure
// semantics can be tested with two feeds in a single cycle.
type selectiveNotifier struct {
	notes     []domain.Notification
	failScope string
}

func (r *selectiveNotifier) Type() string { return "selective" }

func (r *selectiveNotifier) Send(_ context.Context, n domain.Notification) error {
	r.notes = append(r.notes, n)
	if r.failScope != "" && n.Scope == r.failScope {
		return errors.New("send failed")
	}
	return nil
}

// trackingSource records how many Fetch calls overlap.
type trackingSource struct {
	inFlight atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64
}

func (s *trackingSource) Type() string { return "tracking" }

func (s *trackingSource) Fetch(context.Context, source.Cursor) (source.Result, error) {
	s.calls.Add(1)
	cur := s.inFlight.Add(1)
	for {
		old := s.peak.Load()
		if cur <= old || s.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	s.inFlight.Add(-1)
	return res(rel("id-v1", "v1.2.3", fixedNow)), nil
}

func twoFeedStore() *memStore {
	st := domain.NewSeenState()
	st.EnsureFeed("golang/go")
	st.EnsureFeed("rust-lang/rust")
	return &memStore{state: st, exists: true}
}

// existingStore builds a state that already contains the given seen IDs,
// simulating a container restart with an intact volume.
func existingStore(seen ...string) *memStore {
	st := domain.NewSeenState()
	fs := st.EnsureFeed("golang/go")
	for _, id := range seen {
		fs.SeenIDs[id] = fixedNow.Add(-time.Hour)
	}
	fs.ETag = `"old"`
	fs.LastChecked = fixedNow.Add(-2 * time.Hour)
	return &memStore{state: st, exists: true}
}

func newWatcher(st *memStore, n notifier.Notifier, mode string, tasks ...FeedTask) *Watcher {
	w := New(tasks, st, n, Options{
		Interval: time.Hour, Timeout: 5 * time.Second, RunOnStart: true, NotifyMode: mode,
	}, quiet())
	w.now = func() time.Time { return fixedNow }
	return w
}

func stableTask(name string, steps ...step) FeedTask {
	f, err := filter.New(filter.Rules{StableOnly: true})
	if err != nil {
		panic(err)
	}
	return FeedTask{Name: name, Source: &fakeSource{steps: steps}, Filter: f}
}

func TestFirstRunSendsBaselineAndCommitsAllIDs(t *testing.T) {
	t1 := fixedNow.Add(-3 * time.Hour)
	t2 := fixedNow.Add(-1 * time.Hour)
	r1 := rel("id-v1", "v1.2.2", t1)
	r2 := rel("id-v2", "v1.2.3", t2)
	pre := rel("id-rc", "v1.3.0rc1", fixedNow)
	st := &memStore{}
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go", step{result: res(r1, r2, pre)}))

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(n.notes) != 1 || n.notes[0].Kind != domain.KindBaseline {
		t.Fatalf("expected 1 baseline mail, got %+v", n.notes)
	}
	if len(n.notes[0].Items) != 1 || n.notes[0].Items[0].Version != "v1.2.3" {
		t.Errorf("baseline should show the newest stable version: %+v", n.notes[0].Items)
	}
	if n.notes[0].Items[0].IsNew {
		t.Error("baseline rows must not be marked new")
	}
	fs := st.state.Feeds["golang/go"]
	for _, id := range []string{"id-v1", "id-v2", "id-rc"} {
		if _, ok := fs.SeenIDs[id]; !ok {
			t.Errorf("id %s should be marked seen (including prereleases)", id)
		}
	}
	if st.saveCalls != 1 {
		t.Errorf("expected exactly one state write, got %d", st.saveCalls)
	}
}

func TestBaselinePicksHighestVersionNotNewestTimestamp(t *testing.T) {
	// Same-day maintenance releases have later `updated` times than the head
	// version; the baseline must still report the highest version.
	head := rel("id-head", "v3.15.2", fixedNow.Add(-3*time.Hour))
	oldBranch := rel("id-branch", "v3.11.18", fixedNow.Add(-1*time.Hour))
	st := &memStore{}
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("python/cpython", step{result: res(oldBranch, head)}))
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 1 || len(n.notes[0].Items) != 1 || n.notes[0].Items[0].Version != "v3.15.2" {
		t.Fatalf("baseline should show v3.15.2: %+v", n.notes)
	}
}

func TestRestartWithoutNewReleasesIsSilent(t *testing.T) {
	r := rel("id-v1", "v1.2.3", fixedNow.Add(-time.Hour))
	st := &memStore{}
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go", step{result: res(r)}))
	for i := range 3 {
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if len(n.notes) != 1 {
		t.Errorf("restart must be silent: got %d mails", len(n.notes))
	}
	if st.saveCalls != 3 {
		t.Errorf("each cycle should refresh metadata, got %d saves", st.saveCalls)
	}
}

func TestNewReleaseNotifiesOnceThenGoesSilent(t *testing.T) {
	t1 := fixedNow.Add(-2 * time.Hour)
	t2 := fixedNow.Add(-1 * time.Hour)
	v1 := rel("id-v1", "v1.2.3", t1)
	v2 := rel("id-v2", "v1.2.4", t2)
	st := &memStore{}
	n := &recordingNotifier{}
	task := stableTask("golang/go",
		step{result: res(v1)},
		step{result: res(v2, v1)},
	)
	w := newWatcher(st, n, "digest", task)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 2 || n.notes[1].Kind != domain.KindNewReleases {
		t.Fatalf("expected baseline + 1 new-release mail, got %+v", n.notes)
	}
	if len(n.notes[1].Items) != 1 || n.notes[1].Items[0].Version != "v1.2.4" {
		t.Errorf("wrong new release: %+v", n.notes[1].Items)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 2 {
		t.Errorf("must not re-notify the same release: %d mails", len(n.notes))
	}
}

func TestSendFailureDoesNotCommitState(t *testing.T) {
	r := rel("id-v1", "v1.2.3", fixedNow.Add(-time.Hour))
	st := &memStore{}
	n := &recordingNotifier{fail: true}
	w := newWatcher(st, n, "digest", stableTask("golang/go", step{result: res(r)}))

	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("baseline send failure should be returned")
	}
	if st.saveCalls != 0 || st.exists {
		t.Fatal("state must NOT be committed when the mail failed")
	}

	n.fail = false
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 1 || st.saveCalls != 1 {
		t.Errorf("retry should send the baseline once and persist: notes=%d saves=%d", len(n.notes), st.saveCalls)
	}
}

func TestNewReleaseSendFailureIsRetried(t *testing.T) {
	v1 := rel("id-v1", "v1.2.3", fixedNow.Add(-2*time.Hour))
	v2 := rel("id-v2", "v1.2.4", fixedNow.Add(-time.Hour))
	st := existingStore("id-v1")
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go", step{result: res(v2, v1)}))

	n.fail = true
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("notification failure should be returned")
	}
	// The state file is still written (so other feeds' progress is not lost),
	// but the failed feed must stay completely uncommitted.
	if st.saveCalls != 1 {
		t.Fatalf("expected one partial state write, got %d", st.saveCalls)
	}
	if _, ok := st.state.Feeds["golang/go"].SeenIDs["id-v2"]; ok {
		t.Fatal("new id must stay unseen so it is retried")
	}
	if got := st.state.Feeds["golang/go"].ETag; got != `"old"` {
		t.Fatalf("failed feed ETag was committed as %q; the next 304 would swallow the retry", got)
	}

	n.fail = false
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 1 || n.notes[0].Items[0].Version != "v1.2.4" {
		t.Errorf("retry produced wrong mails: %+v", n.notes)
	}
	if st.saveCalls != 2 {
		t.Errorf("expected a second commit after the successful retry, got %d", st.saveCalls)
	}
}

func TestFirstRunWithFailedFeedPostponesBaseline(t *testing.T) {
	r := rel("id-v1", "v1.2.3", fixedNow.Add(-time.Hour))
	st := &memStore{}
	n := &recordingNotifier{}
	bad := &fakeSource{steps: []step{
		{err: errors.New("network down")},
		{result: res(r)},
	}}
	f, _ := filter.New(filter.Rules{StableOnly: true})
	w := newWatcher(st, n, "digest",
		stableTask("golang/go", step{result: res(r)}),
		FeedTask{Name: "rust-lang/rust", Source: bad, Filter: f},
	)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 0 || st.saveCalls != 0 {
		t.Fatalf("baseline must be postponed entirely: notes=%d saves=%d", len(n.notes), st.saveCalls)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 1 || n.notes[0].Kind != domain.KindBaseline || st.saveCalls != 1 {
		t.Errorf("second cycle should complete the baseline: notes=%+v saves=%d", n.notes, st.saveCalls)
	}
}

func TestSingleFeedFailureDoesNotBlockOtherFeeds(t *testing.T) {
	old := rel("id-old", "v1.2.3", fixedNow.Add(-2*time.Hour))
	newRel := rel("id-new", "v1.2.4", fixedNow.Add(-time.Hour))
	st := existingStore("id-old")
	st.state.EnsureFeed("rust-lang/rust").SeenIDs["id-rust"] = fixedNow.Add(-3 * time.Hour)
	before := st.state.Feeds["rust-lang/rust"].LastChecked

	f, _ := filter.New(filter.Rules{StableOnly: true})
	bad := &fakeSource{steps: []step{{err: errors.New("network down")}}}
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest",
		stableTask("golang/go", step{result: res(newRel, old)}),
		FeedTask{Name: "rust-lang/rust", Source: bad, Filter: f},
	)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 1 || n.notes[0].Items[0].Version != "v1.2.4" {
		t.Errorf("the healthy feed should still notify: %+v", n.notes)
	}
	after := st.state.Feeds["rust-lang/rust"].LastChecked
	if !after.Equal(before) {
		t.Errorf("failed feed state must be untouched: %v -> %v", before, after)
	}
}

func TestPrereleaseOnlyIsSilentButMarkedSeen(t *testing.T) {
	pre := rel("id-rc", "v1.3.0rc1", fixedNow.Add(-time.Hour))
	st := existingStore()
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go", step{result: res(pre)}))

	for range 2 {
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.notes) != 0 {
		t.Errorf("prereleases must not trigger mail: %+v", n.notes)
	}
	if _, ok := st.state.Feeds["golang/go"].SeenIDs["id-rc"]; !ok {
		t.Error("prerelease id should be marked seen to avoid repeated diffs")
	}
}

func Test304KeepsSeenIDsAndValidators(t *testing.T) {
	st := existingStore("id-v1")
	before := len(st.state.Feeds["golang/go"].SeenIDs)
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go",
		step{result: source.Result{NotModified: true, ETag: `"ignored"`}},
	))

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	fs := st.state.Feeds["golang/go"]
	if len(n.notes) != 0 {
		t.Errorf("304 must be silent: %+v", n.notes)
	}
	if len(fs.SeenIDs) != before || fs.ETag != `"old"` {
		t.Errorf("304 must not rewrite seen IDs or ETag: %+v", fs)
	}
	if !fs.LastChecked.Equal(fixedNow) {
		t.Errorf("304 should still refresh LastChecked: %v", fs.LastChecked)
	}
}

func TestCorruptedStateIsNeverTreatedAsFirstRun(t *testing.T) {
	st := &memStore{loadErr: errors.New("decode error")}
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go",
		step{result: res(rel("id-v1", "v1.2.3", fixedNow))},
	))
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("corrupted state must fail the cycle")
	}
	if len(n.notes) != 0 || st.saveCalls != 0 {
		t.Errorf("must not send a baseline on corrupted state: notes=%d saves=%d", len(n.notes), st.saveCalls)
	}
}

func TestPerFeedModeGroupsNotifications(t *testing.T) {
	st := domain.NewSeenState()
	st.EnsureFeed("golang/go")
	st.EnsureFeed("rust-lang/rust")
	mem := &memStore{state: st, exists: true}
	n := &recordingNotifier{}
	goRel := rel("id-go", "go1.27.4", fixedNow.Add(-time.Hour))
	rustRel := rel("id-rust", "1.99.2", fixedNow.Add(-time.Hour))
	w := newWatcher(mem, n, "per_feed",
		stableTask("golang/go", step{result: res(goRel)}),
		stableTask("rust-lang/rust", step{result: res(rustRel)}),
	)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 2 {
		t.Fatalf("per_feed should produce 2 mails, got %d", len(n.notes))
	}
	for _, note := range n.notes {
		if len(note.Items) != 1 {
			t.Errorf("each per_feed mail should hold one project's items: %+v", note.Items)
		}
	}
}

func TestPerFeedBatchesCarryScope(t *testing.T) {
	st := twoFeedStore()
	n := &recordingNotifier{}
	w := newWatcher(st, n, "per_feed",
		stableTask("golang/go", step{result: res(rel("id-go", "go1.27.4", fixedNow))}),
		stableTask("rust-lang/rust", step{result: res(rel("id-rust", "1.99.2", fixedNow))}),
	)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"golang/go": false, "rust-lang/rust": false}
	for _, note := range n.notes {
		if note.Scope == "" || note.Items[0].Feed != note.Scope {
			t.Errorf("per_feed batch missing a matching Scope: %+v", note)
		}
		want[note.Scope] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing batch for %s", name)
		}
	}
}

func TestBaselineStaysOneSummaryUnderPerFeed(t *testing.T) {
	st := &memStore{} // first run
	n := &recordingNotifier{}
	w := newWatcher(st, n, "per_feed",
		stableTask("golang/go", step{result: res(rel("id-go", "go1.27.4", fixedNow))}),
		stableTask("rust-lang/rust", step{result: res(rel("id-rust", "1.99.2", fixedNow))}),
	)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 1 || n.notes[0].Kind != domain.KindBaseline ||
		n.notes[0].Scope != "" || len(n.notes[0].Items) != 2 {
		t.Fatalf("baseline must stay one scope-free summary: %+v", n.notes)
	}
}

// A failing per_feed batch used to abort the whole cycle, so the next cycle
// re-sent EVERY project. Now only the failed feed is held back.
func TestPerFeedPartialFailureOnlyRetriesFailedFeed(t *testing.T) {
	st := twoFeedStore()
	n := &selectiveNotifier{failScope: "rust-lang/rust"}
	w := newWatcher(st, n, "per_feed",
		stableTask("golang/go", step{result: res(rel("id-go", "go1.27.4", fixedNow))}),
		stableTask("rust-lang/rust", step{result: res(rel("id-rust", "1.99.2", fixedNow))}),
	)

	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("a failed batch must surface as an error")
	}
	if len(n.notes) != 2 {
		t.Fatalf("all batches should still be attempted, got %d", len(n.notes))
	}
	goFS, rustFS := st.state.Feeds["golang/go"], st.state.Feeds["rust-lang/rust"]
	if _, ok := goFS.SeenIDs["id-go"]; !ok {
		t.Error("successfully delivered feed must be committed")
	}
	if goFS.ETag != `"e-1"` {
		t.Errorf("delivered feed should keep the new ETag, got %q", goFS.ETag)
	}
	if _, ok := rustFS.SeenIDs["id-rust"]; ok {
		t.Error("failed feed must stay unseen so it can be retried")
	}
	if rustFS.ETag != "" {
		t.Errorf("failed feed must keep its old ETag, got %q", rustFS.ETag)
	}
	if st.saveCalls != 1 {
		t.Errorf("partial progress must be persisted, got %d saves", st.saveCalls)
	}

	// Next cycle: only the previously failed feed is notified. No duplicates
	// for golang/go, which already delivered.
	n.failScope = ""
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.notes) != 3 {
		t.Fatalf("expected exactly one retry mail, got %d notes total", len(n.notes))
	}
	retry := n.notes[2]
	if retry.Scope != "rust-lang/rust" || len(retry.Items) != 1 || retry.Items[0].Version != "1.99.2" {
		t.Errorf("wrong retry batch: %+v", retry)
	}
	if _, ok := st.state.Feeds["rust-lang/rust"].SeenIDs["id-rust"]; !ok {
		t.Error("retry should commit the previously failed feed")
	}
}

func TestFetchConcurrencyIsBounded(t *testing.T) {
	const feeds, limit = 8, 3
	tracker := &trackingSource{}
	tasks := make([]FeedTask, 0, feeds)
	for i := range feeds {
		tasks = append(tasks, FeedTask{Name: fmt.Sprintf("feed-%d", i), Source: tracker})
	}
	st := &memStore{}
	n := &recordingNotifier{}
	w := New(tasks, st, n, Options{
		Interval: time.Hour, Timeout: 5 * time.Second,
		FetchConcurrency: limit, FetchSpacing: 0,
	}, quiet())
	w.now = func() time.Time { return fixedNow }

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tracker.calls.Load(); got != feeds {
		t.Errorf("fetched %d feeds, want %d", got, feeds)
	}
	if got := tracker.peak.Load(); got > limit {
		t.Errorf("peak concurrent fetches = %d, want <= %d", got, limit)
	} else if got < 2 {
		t.Errorf("expected real parallelism, peak was %d", got)
	}
	if len(n.notes) != 1 || n.notes[0].Kind != domain.KindBaseline {
		t.Errorf("parallel fetch should still produce one baseline: %+v", n.notes)
	}
}

func TestFetchGateWiring(t *testing.T) {
	w := newWatcher(&memStore{}, &recordingNotifier{}, "digest")
	if w.fetchGate.Concurrency() != 1 || w.fetchGate.Spacing() != 0 {
		t.Errorf("default should stay serial and unthrottled: %d / %v",
			w.fetchGate.Concurrency(), w.fetchGate.Spacing())
	}
	w2 := New(nil, &memStore{}, &recordingNotifier{}, Options{
		Interval: time.Hour, Timeout: time.Second,
		FetchConcurrency: 5, FetchSpacing: 30 * time.Millisecond,
	}, quiet())
	if w2.fetchGate.Concurrency() != 5 || w2.fetchGate.Spacing() != 30*time.Millisecond {
		t.Errorf("fetch limits not wired: %d / %v", w2.fetchGate.Concurrency(), w2.fetchGate.Spacing())
	}
}

func TestRunReturnsOnContextCancellation(t *testing.T) {
	st := &memStore{}
	n := &recordingNotifier{}
	w := newWatcher(st, n, "digest", stableTask("golang/go"))
	w.opt.Interval = 10 * time.Millisecond
	w.opt.RunOnStart = false
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run should exit cleanly: %v", err)
	}
}
