//go:build webui

package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/source"
	"feedwatch/internal/webui"
	"feedwatch/internal/webui/store"
)

var fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeFetcher struct {
	mu      sync.Mutex
	calls   []string
	results map[string]source.Result
	errs    map[string]error
}

func (f *fakeFetcher) Fetch(_ context.Context, url string, _ source.Cursor) (source.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, url)
	if err := f.errs[url]; err != nil {
		return source.Result{}, err
	}
	return f.results[url], nil
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func release(id, version string) domain.Release {
	return domain.Release{
		ID: id, Feed: "project", Title: "Release " + version,
		Version: version, CoreVersion: version,
		URL:         "https://example.com/releases/" + id,
		PublishedAt: fixedTime, IsStable: true,
	}
}

func newHarness(t *testing.T, f Fetcher) (*Orchestrator, *sql.DB) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	o := New(st, f, Options{
		Interval: time.Hour, Timeout: 10 * time.Second,
		FetchConcurrency: 1, SubjectPrefix: "[FW]",
	}, quietLog())
	o.now = func() time.Time { return fixedTime }
	return o, st.DB()
}

func addUser(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, name, "hash", "user", 1, 0, fixedTime.Format(time.RFC3339Nano), fixedTime.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func addChannel(t *testing.T, db *sql.DB, userID int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO channels (user_id, type, name, config_json, enabled) VALUES (?,?,?,?,?)`,
		userID, "log", "log", `{"type":"log"}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func addSubscription(t *testing.T, db *sql.DB, userID int64, rawURL, policy, mode, filterJSON string) string {
	t.Helper()
	normalized, key, err := webui.URLKey(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if filterJSON == "" {
		filterJSON = `{"stable_only":true}`
	}
	if _, err := db.Exec(`INSERT INTO subscriptions
		(user_id, url, url_key, display_name, source_type, filter_json, notify_policy, mode, enabled, seeded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, userID, normalized, key, "project", "atom",
		filterJSON, policy, mode, 1, fixedTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO feed_pool (url_key, url, source_type) VALUES (?,?,?)`,
		key, normalized, "atom"); err != nil {
		t.Fatal(err)
	}
	return key
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func notificationsEnvelopes(t *testing.T, db *sql.DB) []Envelope {
	t.Helper()
	rows, err := db.Query(`SELECT markdown FROM notifications ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make([]Envelope, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env Envelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatalf("decode envelope %q: %v", raw, err)
		}
		out = append(out, env)
	}
	return out
}

const feedA = "https://example.com/releases.atom?a=1&b=2"
const feedAEquivalent = "https://EXAMPLE.com:443/releases.atom?b=2&a=1#frag"

func TestRunOnceFetchesEachURLOnceAndCommitsGlobalSeen(t *testing.T) {
	f := &fakeFetcher{results: map[string]source.Result{
		feedA: {Releases: []domain.Release{release("id-1", "1.2.3"), release("id-2", "1.2.4")}},
	}}
	o, db := newHarness(t, f)
	u1, u2 := addUser(t, db, "alice"), addUser(t, db, "bob")
	addChannel(t, db, u1)
	addChannel(t, db, u2)
	key := addSubscription(t, db, u1, feedA, "on_update", "digest", "")
	addSubscription(t, db, u2, feedAEquivalent, "on_update", "digest", "")

	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callCount() != 1 {
		t.Fatalf("same normalized URL fetched %d times, want 1 (calls=%v)", f.callCount(), f.calls)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM seen_ids WHERE url_key=?`, key); got != 2 {
		t.Fatalf("global seen_ids = %d, want 2", got)
	}
	var lastVersion, etag string
	if err := db.QueryRow(`SELECT last_version, etag FROM feed_pool WHERE url_key=?`, key).Scan(&lastVersion, &etag); err != nil {
		t.Fatal(err)
	}
	if lastVersion != "1.2.4" {
		t.Fatalf("last_version = %q, want 1.2.4", lastVersion)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notifications`); got != 2 {
		t.Fatalf("notifications = %d, want one per user", got)
	}
	envs := notificationsEnvelopes(t, db)
	seenUsers := map[int64]int{}
	for _, env := range envs {
		seenUsers[env.UserID]++
		if len(env.Note.Items) != 2 || env.Note.Kind != domain.KindNewReleases {
			t.Errorf("unexpected note: %+v", env.Note)
		}
	}
	if seenUsers[u1] != 1 || seenUsers[u2] != 1 {
		t.Fatalf("envelopes per user = %+v", seenUsers)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM user_seen`); got != 4 {
		t.Fatalf("user_seen = %d, want 4 (2 releases x 2 users)", got)
	}
}

func TestAlwaysEmitsStatusSummaryWhileOnUpdateStaysSilent(t *testing.T) {
	f := &fakeFetcher{results: map[string]source.Result{
		feedA: {Releases: []domain.Release{release("id-1", "1.2.3")}},
	}}
	o, db := newHarness(t, f)
	always, quiet := addUser(t, db, "always"), addUser(t, db, "quiet")
	addChannel(t, db, always)
	addChannel(t, db, quiet)
	key := addSubscription(t, db, always, feedA, "always", "digest", "")
	addSubscription(t, db, quiet, feedAEquivalent, "on_update", "digest", "")

	// Both users already have the whole feed in their baseline (as an API
	// subscription would after seeding user_seen from seen_ids).
	if _, err := db.Exec(`INSERT OR IGNORE INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)`,
		key, "id-1", fixedTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	for _, u := range []int64{always, quiet} {
		if _, err := db.Exec(`INSERT OR IGNORE INTO user_seen (user_id, url_key, release_id, seen_at) VALUES (?,?,?,?)`,
			u, key, "id-1", fixedTime.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	envs := notificationsEnvelopes(t, db)
	if len(envs) != 1 {
		t.Fatalf("want exactly one status summary, got %d: %+v", len(envs), envs)
	}
	if envs[0].UserID != always || envs[0].Note.Kind != domain.KindBaseline {
		t.Fatalf("wrong summary: %+v", envs[0])
	}
}

func TestPerUserFiltersAreIndependent(t *testing.T) {
	stable := release("id-1", "1.2.3")
	pre := release("id-2", "1.3.0-rc1")
	f := &fakeFetcher{results: map[string]source.Result{
		feedA: {Releases: []domain.Release{stable, pre}},
	}}
	o, db := newHarness(t, f)
	u1, u2 := addUser(t, db, "stable-only"), addUser(t, db, "all")
	addChannel(t, db, u1)
	addChannel(t, db, u2)
	addSubscription(t, db, u1, feedA, "on_update", "digest", `{"stable_only":true}`)
	addSubscription(t, db, u2, feedAEquivalent, "on_update", "digest", `{"stable_only":false}`)

	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	envs := notificationsEnvelopes(t, db)
	if len(envs) != 2 {
		t.Fatalf("want 2 notifications, got %d", len(envs))
	}
	counts := map[int64]int{}
	for _, env := range envs {
		counts[env.UserID] = len(env.Note.Items)
	}
	if counts[u1] != 1 || counts[u2] != 2 {
		t.Fatalf("per-user filter was not applied independently: %+v", counts)
	}
}

func TestFetchFailureDoesNotAdvanceState(t *testing.T) {
	f := &fakeFetcher{errs: map[string]error{feedA: context.DeadlineExceeded}}
	o, db := newHarness(t, f)
	u := addUser(t, db, "alice")
	addChannel(t, db, u)
	key := addSubscription(t, db, u, feedA, "on_update", "digest", "")

	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM seen_ids WHERE url_key=?`, key); got != 0 {
		t.Fatalf("failed fetch must not commit seen IDs, got %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notifications`); got != 0 {
		t.Fatalf("failed fetch must not produce notifications, got %d", got)
	}
}

func TestSecondCycleDoesNotDuplicateNotifications(t *testing.T) {
	f := &fakeFetcher{results: map[string]source.Result{
		feedA: {Releases: []domain.Release{release("id-1", "1.2.3")}},
	}}
	o, db := newHarness(t, f)
	u := addUser(t, db, "alice")
	addChannel(t, db, u)
	addSubscription(t, db, u, feedA, "on_update", "digest", "")

	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callCount() != 2 {
		t.Fatalf("want 2 fetches across 2 cycles, got %d", f.callCount())
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notifications`); got != 1 {
		t.Fatalf("second cycle replayed history: %d notifications", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM cycles`); got != 2 {
		t.Fatalf("cycles = %d, want 2", got)
	}
}

func TestNewSubscriptionBaselineDoesNotReplayHistory(t *testing.T) {
	f := &fakeFetcher{results: map[string]source.Result{
		feedA: {Releases: []domain.Release{release("id-1", "1.2.3")}},
	}}
	o, db := newHarness(t, f)
	old, fresh := addUser(t, db, "old"), addUser(t, db, "fresh")
	addChannel(t, db, old)
	addChannel(t, db, fresh)
	key := addSubscription(t, db, old, feedA, "on_update", "digest", "")

	// First cycle establishes the global baseline.
	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A new user subscribing to an already-known URL copies seen_ids into
	// user_seen (this is what POST /api/subscriptions does) and must stay silent.
	addSubscription(t, db, fresh, feedAEquivalent, "on_update", "digest", "")
	if _, err := db.Exec(`INSERT OR IGNORE INTO user_seen (user_id, url_key, release_id, seen_at)
		SELECT ?, url_key, release_id, ? FROM seen_ids WHERE url_key=?`,
		fresh, fixedTime.Format(time.RFC3339Nano), key); err != nil {
		t.Fatal(err)
	}
	if err := o.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notifications`); got != 1 {
		t.Fatalf("new subscription replayed history: %d notifications", got)
	}
}
