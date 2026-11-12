// Package watcher is the core orchestration: fetch -> filter -> diff -> notify
// -> commit. It depends only on interfaces and the domain model; it does not
// know config, Atom, Resend or JSON files.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/filter"
	"feedwatch/internal/notifier"
	"feedwatch/internal/pace"
	"feedwatch/internal/source"
	"feedwatch/internal/store"
)

type FeedTask struct {
	Name   string
	Source source.Source
	Filter filter.Filter // nil means "no filtering" (useful for tests / custom sources)
}

type Options struct {
	Interval   time.Duration
	Timeout    time.Duration
	RunOnStart bool
	NotifyMode string // "digest" (default) | "per_feed"
	// FetchConcurrency caps simultaneous feed requests; 1 (the default) keeps
	// the original fully serial behaviour. FetchSpacing additionally paces the
	// START of each request, which caps the aggregate rate against one host.
	FetchConcurrency int
	FetchSpacing     time.Duration
}

type Watcher struct {
	tasks     []FeedTask
	st        store.StateStore
	n         notifier.Notifier
	opt       Options
	log       *slog.Logger
	now       func() time.Time // injectable for deterministic tests
	fetchGate *pace.Gate       // shared by every feed in every cycle
}

func New(tasks []FeedTask, st store.StateStore, n notifier.Notifier, opt Options, log *slog.Logger) *Watcher {
	if opt.Interval <= 0 {
		opt.Interval = 30 * time.Minute
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Minute
	}
	if opt.NotifyMode == "" {
		opt.NotifyMode = "digest"
	}
	if opt.FetchConcurrency < 1 {
		opt.FetchConcurrency = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		tasks: tasks, st: st, n: n, opt: opt, log: log, now: time.Now,
		fetchGate: pace.New(opt.FetchConcurrency, opt.FetchSpacing),
	}
}

type outcome struct {
	name        string
	res         source.Result
	all         []domain.Release
	stable      []domain.Release
	notModified bool
	err         error
}

// RunOnce executes exactly one poll cycle. It is the entry point for -once and
// for the tests that assert the first-run / restart / retry semantics.
func (w *Watcher) RunOnce(ctx context.Context) error {
	pollCtx, cancel := context.WithTimeout(ctx, w.opt.Timeout)
	defer cancel()

	state, err := w.st.Load(pollCtx)
	firstRun := errors.Is(err, store.ErrNotFound)
	if err != nil && !firstRun {
		// A damaged state file must never be mistaken for a first run: doing so
		// would silently re-send the baseline email.
		return fmt.Errorf("watcher: load state: %w", err)
	}
	if firstRun {
		state = domain.NewSeenState()
		w.log.Info("no state found: this is a first run, baseline will be sent")
	}
	if state.Feeds == nil {
		state.Feeds = make(map[string]*domain.FeedState)
	}
	now := w.now().UTC()

	outs := w.fetchAll(pollCtx, state)
	if firstRun {
		return w.baseline(pollCtx, &state, outs, now)
	}
	return w.regular(pollCtx, &state, outs, now)
}

// fetchAll uses a bounded worker pool plus a shared rate limiter. outcomes
// are stored by task index, so diff and commit stay deterministic no matter
// which worker finishes first. A cancelled/timed-out context makes the
// remaining Acquire calls fail fast; those feeds are simply skipped (and stay
// uncommitted) until the next cycle.
func (w *Watcher) fetchAll(ctx context.Context, state domain.SeenState) []outcome {
	outs := make([]outcome, len(w.tasks))
	if len(w.tasks) == 0 {
		return outs
	}
	indices := make(chan int)
	workers := min(w.fetchGate.Concurrency(), len(w.tasks))
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range indices {
				outs[i] = w.fetchOne(ctx, i, state)
			}
		})
	}
	for i := range w.tasks {
		indices <- i
	}
	close(indices)
	wg.Wait()
	return outs
}

func (w *Watcher) fetchOne(ctx context.Context, i int, state domain.SeenState) outcome {
	t := w.tasks[i]
	release, err := w.fetchGate.Acquire(ctx)
	if err != nil {
		return outcome{name: t.Name, err: err}
	}
	defer release()

	var cur source.Cursor
	if fs := state.Feeds[t.Name]; fs != nil {
		cur = source.Cursor{ETag: fs.ETag, LastModified: fs.LastModified}
	}
	res, err := t.Source.Fetch(ctx, cur)
	if err != nil {
		w.log.Error("feed fetch failed; skipping this feed for this cycle",
			"feed", t.Name, "err", err)
		return outcome{name: t.Name, err: err}
	}
	if res.NotModified {
		w.log.Debug("feed not modified (304)", "feed", t.Name)
		return outcome{name: t.Name, res: res, notModified: true}
	}
	stable := res.Releases
	if t.Filter != nil {
		stable = t.Filter.Apply(res.Releases)
	}
	w.log.Info("feed fetched", "feed", t.Name,
		"entries", len(res.Releases), "stable", len(stable))
	return outcome{name: t.Name, res: res, all: res.Releases, stable: stable}
}

// baseline sends exactly one summary mail listing each feed's current latest
// stable version. It is all-or-nothing: if any feed failed, the whole baseline
// is postponed (no mail, no state write) and retried next cycle. mode=per_feed
// does NOT apply here: the baseline is always one summary.
func (w *Watcher) baseline(ctx context.Context, state *domain.SeenState, outs []outcome, now time.Time) error {
	for _, o := range outs {
		if o.err != nil {
			w.log.Warn("baseline postponed because a feed failed", "feed", o.name, "err", o.err)
			return nil
		}
	}

	items := make([]domain.ReleaseItem, 0, len(outs))
	for _, o := range outs {
		if o.notModified {
			continue
		}
		if latest := latestStable(o.stable); latest != nil {
			items = append(items, toItem(o.name, *latest, false))
		}
	}
	if len(items) == 0 {
		w.log.Warn("baseline found no stable releases; committing without sending a mail")
		w.commit(state, outs, now, nil)
		return w.save(ctx, *state)
	}

	note := domain.Notification{Kind: domain.KindBaseline, GeneratedAt: now, Items: items}
	if err := w.n.Send(ctx, note); err != nil {
		return fmt.Errorf("watcher: send baseline: %w", err)
	}
	w.commit(state, outs, now, nil)
	return w.save(ctx, *state)
}

// regular notifies only about stable releases whose IDs are not yet seen.
//
// Batches are committed INDEPENDENTLY. Under per_feed mode a failure in batch
// k used to abort the whole cycle without any state write, so the next cycle
// re-sent batches 1..k-1 as well. Now only the feeds in failed batches are
// held back; successful feeds have their IDs and validators persisted even
// when a later batch fails. digest mode has exactly one batch, so its
// behaviour is unchanged (all-or-nothing).
func (w *Watcher) regular(ctx context.Context, state *domain.SeenState, outs []outcome, now time.Time) error {
	var newItems []domain.ReleaseItem
	for _, o := range outs {
		if o.err != nil || o.notModified {
			continue
		}
		// Looking up a nil map is safe, so there is no need to copy SeenIDs
		// into a set just to test membership: this phase only reads state.
		var seen map[string]time.Time
		if fs := state.Feeds[o.name]; fs != nil {
			seen = fs.SeenIDs
		}
		for _, r := range o.stable {
			if _, ok := seen[r.ID]; !ok {
				newItems = append(newItems, toItem(o.name, r, true))
			}
		}
	}

	blocked := make(map[string]struct{})
	var sendErr error
	failed := 0
	if len(newItems) == 0 {
		w.log.Debug("no new stable releases; staying silent")
	} else {
		for _, b := range w.batch(newItems) {
			note := domain.Notification{
				Kind: domain.KindNewReleases, GeneratedAt: now,
				Items: b.Items, Scope: b.Scope,
			}
			if err := w.n.Send(ctx, note); err != nil {
				failed++
				if sendErr == nil {
					sendErr = err
				}
				for _, it := range b.Items {
					blocked[it.Feed] = struct{}{}
				}
				w.log.Error("notification batch failed; only these feeds will be retried",
					"scope", b.Scope, "items", len(b.Items), "err", err)
				continue
			}
		}
	}
	if failed > 0 {
		w.log.Warn("some notification batches failed; committing only the delivered feeds",
			"failed_batches", failed, "blocked_feeds", len(blocked))
	}

	w.commit(state, outs, now, blocked)
	saveErr := w.save(ctx, *state)
	if sendErr != nil {
		wrapped := fmt.Errorf("watcher: send notification: %w", sendErr)
		if saveErr != nil {
			return errors.Join(wrapped, saveErr)
		}
		return wrapped
	}
	return saveErr
}

// notificationBatch is one Send() call. Scope is set only for per_feed
// batches, so templates can name the project in the subject.
type notificationBatch struct {
	Items []domain.ReleaseItem
	Scope string
}

func (w *Watcher) batch(items []domain.ReleaseItem) []notificationBatch {
	if w.opt.NotifyMode != "per_feed" {
		return []notificationBatch{{Items: items}}
	}
	groups := make(map[string][]domain.ReleaseItem)
	var order []string
	for _, it := range items {
		if _, ok := groups[it.Feed]; !ok {
			order = append(order, it.Feed)
		}
		groups[it.Feed] = append(groups[it.Feed], it)
	}
	out := make([]notificationBatch, 0, len(order))
	for _, name := range order {
		out = append(out, notificationBatch{Items: groups[name], Scope: name})
	}
	return out
}

// commit only touches feeds that succeeded in this cycle AND are not blocked
// by a failed notification. All fetched IDs are marked seen (including
// prereleases and non-version tags), so they never trigger repeated diffs.
// Skipping a blocked feed also keeps its OLD ETag: committing the new one
// would make the next poll return 304 and the pending release would never be
// re-notified.
func (w *Watcher) commit(state *domain.SeenState, outs []outcome, now time.Time, blocked map[string]struct{}) {
	for _, o := range outs {
		if o.err != nil {
			continue
		}
		if _, skip := blocked[o.name]; skip {
			continue
		}
		fs := state.EnsureFeed(o.name)
		fs.LastChecked = now
		if o.notModified {
			// 304: never rewrite SeenIDs or validators.
			continue
		}
		fs.ETag = o.res.ETag
		fs.LastModified = o.res.LastModified
		for _, r := range o.all {
			if r.ID == "" {
				continue
			}
			if _, exists := fs.SeenIDs[r.ID]; !exists {
				fs.SeenIDs[r.ID] = now
			}
		}
		if latest := latestStable(o.stable); latest != nil {
			fs.LastVersion = latest.Version
			if fs.LastVersion == "" {
				fs.LastVersion = latest.Title
			}
		}
	}
	state.UpdatedAt = now
}

// save uses its own short, cancellation-independent deadline. A poll cycle
// with many feeds can hit poll.timeout after some notifications were already
// delivered; dropping that state write would re-send them on the next cycle.
func (w *Watcher) save(ctx context.Context, state domain.SeenState) error {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.st.Save(saveCtx, state); err != nil {
		return fmt.Errorf("watcher: save state: %w", err)
	}
	return nil
}

// latestStable picks the highest semantic version, not the newest timestamp:
// GitHub often publishes several maintenance branches on the same day, so an
// "updated" timestamp can make go1.26.10 or v3.11.18 look newer than the real
// head (go1.27.3 / v3.15.2). Time is only a tie-breaker.
func latestStable(rs []domain.Release) *domain.Release {
	var best *domain.Release
	for i := range rs {
		r := &rs[i]
		if best == nil {
			best = r
			continue
		}
		switch c := filter.CompareVersion(r.CoreVersion, best.CoreVersion); {
		case c > 0:
			best = r
		case c == 0 && r.EffectiveTime().After(best.EffectiveTime()):
			best = r
		}
	}
	return best
}

func toItem(feed string, r domain.Release, isNew bool) domain.ReleaseItem {
	version := r.Version
	if version == "" {
		version = r.Title
	}
	return domain.ReleaseItem{
		Feed: feed, Version: version, URL: r.URL,
		PublishedAt: r.EffectiveTime(), IsNew: isNew,
	}
}

// Run executes a cycle immediately (when configured), then on every tick.
// RunOnce is called inline so cycles never overlap; ctx cancellation propagates
// into the in-flight HTTP requests.
func (w *Watcher) Run(ctx context.Context) error {
	if w.opt.RunOnStart {
		if err := w.RunOnce(ctx); err != nil {
			w.log.Error("initial poll failed", "err", err)
		}
	}
	ticker := time.NewTicker(w.opt.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("shutting down")
			return nil
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil {
				w.log.Error("poll failed", "err", err)
			}
		}
	}
}
