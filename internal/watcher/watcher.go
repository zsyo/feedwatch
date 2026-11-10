// Package watcher is the core orchestration: fetch -> filter -> diff -> notify
// -> commit. It depends only on interfaces and the domain model; it does not
// know config, Atom, Resend or JSON files.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/filter"
	"feedwatch/internal/notifier"
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
}

type Watcher struct {
	tasks []FeedTask
	st    store.StateStore
	n     notifier.Notifier
	opt   Options
	log   *slog.Logger
	now   func() time.Time // injectable for deterministic tests
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
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{tasks: tasks, st: st, n: n, opt: opt, log: log, now: time.Now}
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

// fetchAll is deliberately serial: three feeds every 30 minutes makes the cost
// negligible, keeps diff/commit single-threaded and avoids GitHub rate limits.
// The fetch phase could be parallelised later without changing the interface.
func (w *Watcher) fetchAll(ctx context.Context, state domain.SeenState) []outcome {
	outs := make([]outcome, 0, len(w.tasks))
	for _, t := range w.tasks {
		var cur source.Cursor
		if fs := state.Feeds[t.Name]; fs != nil {
			cur = source.Cursor{ETag: fs.ETag, LastModified: fs.LastModified}
		}
		res, err := t.Source.Fetch(ctx, cur)
		if err != nil {
			w.log.Error("feed fetch failed; skipping this feed for this cycle",
				"feed", t.Name, "err", err)
			outs = append(outs, outcome{name: t.Name, err: err})
			continue
		}
		if res.NotModified {
			w.log.Debug("feed not modified (304)", "feed", t.Name)
			outs = append(outs, outcome{name: t.Name, res: res, notModified: true})
			continue
		}
		stable := res.Releases
		if t.Filter != nil {
			stable = t.Filter.Apply(res.Releases)
		}
		w.log.Info("feed fetched", "feed", t.Name,
			"entries", len(res.Releases), "stable", len(stable))
		outs = append(outs, outcome{
			name: t.Name, res: res, all: res.Releases, stable: stable,
		})
	}
	return outs
}

// baseline sends exactly one summary mail listing each feed's current latest
// stable version. It is all-or-nothing: if any feed failed, the whole baseline
// is postponed (no mail, no state write) and retried next cycle.
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
		w.commit(state, outs, now)
		return w.save(ctx, *state)
	}

	note := domain.Notification{Kind: domain.KindBaseline, GeneratedAt: now, Items: items}
	if err := w.n.Send(ctx, note); err != nil {
		return fmt.Errorf("watcher: send baseline: %w", err)
	}
	w.commit(state, outs, now)
	return w.save(ctx, *state)
}

// regular notifies only about stable releases whose IDs are not yet seen.
// A send failure returns before commit, so the same batch is retried next time.
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

	if len(newItems) == 0 {
		w.log.Debug("no new stable releases; staying silent")
	} else {
		for _, batch := range w.batch(newItems) {
			note := domain.Notification{Kind: domain.KindNewReleases, GeneratedAt: now, Items: batch}
			if err := w.n.Send(ctx, note); err != nil {
				w.log.Error("notification failed; state NOT committed, will retry next cycle", "err", err)
				return fmt.Errorf("watcher: send notification: %w", err)
			}
		}
	}
	w.commit(state, outs, now)
	return w.save(ctx, *state)
}

func (w *Watcher) batch(items []domain.ReleaseItem) [][]domain.ReleaseItem {
	if w.opt.NotifyMode != "per_feed" {
		return [][]domain.ReleaseItem{items}
	}
	groups := make(map[string][]domain.ReleaseItem)
	var order []string
	for _, it := range items {
		if _, ok := groups[it.Feed]; !ok {
			order = append(order, it.Feed)
		}
		groups[it.Feed] = append(groups[it.Feed], it)
	}
	out := make([][]domain.ReleaseItem, 0, len(order))
	for _, name := range order {
		out = append(out, groups[name])
	}
	return out
}

// commit only touches feeds that succeeded in this cycle. All fetched IDs are
// marked seen (including prereleases and non-version tags), so they never
// trigger repeated diffs; a later stable release has a different Atom ID.
func (w *Watcher) commit(state *domain.SeenState, outs []outcome, now time.Time) {
	for _, o := range outs {
		if o.err != nil {
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

func (w *Watcher) save(ctx context.Context, state domain.SeenState) error {
	if err := w.st.Save(ctx, state); err != nil {
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
