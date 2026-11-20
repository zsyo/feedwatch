//go:build webui

package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/source"
	"feedwatch/internal/webui/store"
)

// BaselineSubscription carries the fields of a subscription that the baseline
// snapshot needs. It is a plain DTO so the api package can call Push without
// importing orchestrator types (the interface is defined on the api side).
type BaselineSubscription struct {
	URL         string
	URLKey      string
	SourceType  string
	FilterJSON  string
	DisplayName string
}

// gateFunc is the subset of pace.Gate that Baseline uses. Declaring it here
// keeps this package decoupled from pace's concrete type while still sharing
// the exact gate instance built by the composition root. A nil gate simply
// skips rate limiting (useful in tests).
type gateFunc interface {
	Acquire(context.Context) (func(), error)
}

// Baseline delivers a per-user "current version" snapshot when a user
// subscribes or re-enables a subscription. It deliberately never advances any
// seen state: the snapshot is an extra, immediate notification, while the
// regular poll cycle keeps owning diffs (new releases -> KindNewReleases).
//
// Data comes from the shared FeedCache first; only a miss triggers a network
// fetch, which is rate-limited by the shared poll gate and refills the cache so
// a burst of subscribers to one URL costs one request.
type Baseline struct {
	st      *store.Store
	fetcher Fetcher
	cache   *FeedCache
	gate    gateFunc
	prefix  string
	log     *slog.Logger
	now     func() time.Time
}

// NewBaseline builds the baseline service.
func NewBaseline(st *store.Store, f Fetcher, cache *FeedCache, gate gateFunc, prefix string, log *slog.Logger) *Baseline {
	if log == nil {
		log = slog.Default()
	}
	if cache == nil {
		cache = NewFeedCache()
	}
	return &Baseline{st: st, fetcher: f, cache: cache, gate: gate, prefix: prefix, log: log, now: time.Now}
}

// PushBaseline is the scalar-argument adapter that satisfies api.BaselineSource
// without this package importing api (which would invert the dependency).
func (b *Baseline) PushBaseline(ctx context.Context, userID int64, url, urlKey, sourceType, filterJSON, displayName string) error {
	return b.Push(ctx, userID, BaselineSubscription{
		URL: url, URLKey: urlKey, SourceType: sourceType,
		FilterJSON: filterJSON, DisplayName: displayName,
	})
}

// Push materializes and stores one KindBaseline notification for userID. It
// returns an error only for real failures so the caller can log them; it never
// mutates seen_ids, user_seen or feed_pool.
func (b *Baseline) Push(ctx context.Context, userID int64, sub BaselineSubscription) error {
	releases, ok := b.cache.Get(sub.URLKey)
	if !ok {
		// Cache miss: fetch the full feed (empty cursor => no conditional
		// request) under the shared poll gate, then refill the cache.
		var release func()
		if b.gate != nil {
			var err error
			release, err = b.gate.Acquire(ctx)
			if err != nil {
				return fmt.Errorf("baseline: acquire fetch slot: %w", err)
			}
		}
		res, err := b.fetcher.Fetch(ctx, sub.URL, sub.SourceType,
			releaseScopeFrom(sub.FilterJSON), source.Cursor{})
		if release != nil {
			release()
		}
		if err != nil {
			return fmt.Errorf("baseline: fetch %s: %w", sub.URLKey, err)
		}
		if res.NotModified || len(res.Releases) == 0 {
			return fmt.Errorf("baseline: no releases for %s", sub.URLKey)
		}
		releases = res.Releases
		b.cache.Put(sub.URLKey, releases)
	}

	// Apply the subscriber's own filter (stable-only, include/exclude, ...) so
	// the snapshot matches what this user will actually be notified about.
	flt, err := filterFor(sub.FilterJSON, sub.SourceType)
	if err != nil {
		return fmt.Errorf("baseline: filter: %w", err)
	}
	releases = flt.Apply(releases)

	version := latestStable(releases)
	if version == "" {
		// Fall back to the pool's last known version, like cycle.go does for
		// `always` status summaries, so a snapshot is never empty-handed.
		var last string
		if err := b.st.DB().QueryRow(`SELECT last_version FROM feed_pool WHERE url_key=?`,
			sub.URLKey).Scan(&last); err == nil && last != "" {
			version = last
		}
	}
	if version == "" {
		version = "未知"
	}
	name := sub.DisplayName
	if name == "" {
		name = sub.URL
	}
	item := domain.ReleaseItem{Feed: name, Version: version, URL: sub.URL}

	// Only users with an enabled channel get a snapshot; the subscription gate
	// normally guarantees one, but a channel may be disabled in the meantime.
	byUser, err := loadChannelIDs(b.st.DB())
	if err != nil {
		return fmt.Errorf("baseline: load channels: %w", err)
	}
	chIDs := byUser[userID]
	if len(chIDs) == 0 {
		return nil
	}

	nowTime := b.now().UTC()
	note := domain.Notification{
		Kind:        domain.KindBaseline,
		GeneratedAt: nowTime,
		Items:       []domain.ReleaseItem{item},
	}
	r, err := materialize(note, b.prefix, userID, chIDs)
	if err != nil {
		return fmt.Errorf("baseline: materialize: %w", err)
	}
	tx, err := b.st.DB().Begin()
	if err != nil {
		return fmt.Errorf("baseline: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := insertNotification(tx, "baseline-"+randomHex(8), r, store.Now()); err != nil {
		return fmt.Errorf("baseline: insert notification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("baseline: commit: %w", err)
	}
	b.log.Info("webui: baseline snapshot queued", "user", userID, "url_key", sub.URLKey, "version", version)
	return nil
}
