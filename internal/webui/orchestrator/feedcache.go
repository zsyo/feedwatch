//go:build webui

package orchestrator

import (
	"sync"
	"time"

	"feedwatch/internal/domain"
)

// FeedCache is a process-local snapshot of the most recent releases fetched for
// each url_key. It serves two purposes in the multi-tenant baseline flow:
//
//   - the poll cycle fills it after every successful fetch, so a user who
//     subscribes later gets the same data the running instance already has
//     instead of triggering a redundant network request;
//   - a subscription-triggered fetch that missed the cache fills it too, so N
//     users subscribing to the same URL in a burst cause only one request.
//
// A hit is used as-is regardless of age: a stale snapshot is intentional (the
// next global poll advances it and delivers the real update as a diff). There
// is no TTL eviction — the cache is bounded by the number of distinct URLs in
// the pool and is overwritten in place on each poll. WebUI is single-instance
// by design (one orchestrator, one SQLite connection), so no cross-process
// coherence is needed.
type FeedCache struct {
	mu    sync.RWMutex
	items map[string]feedSnapshot
}

type feedSnapshot struct {
	releases []domain.Release
	at       int64 // unix nanos, for logging / future TTL use
}

// NewFeedCache returns an empty, ready-to-use cache.
func NewFeedCache() *FeedCache {
	return &FeedCache{items: make(map[string]feedSnapshot)}
}

// Put stores the releases for a url_key. Callers must skip HTTP 304 results:
// an empty release list must never overwrite a previously cached snapshot.
func (c *FeedCache) Put(urlKey string, releases []domain.Release) {
	if c == nil || len(releases) == 0 {
		return
	}
	// Copy so a caller mutating its slice later cannot corrupt the cache.
	cp := make([]domain.Release, len(releases))
	copy(cp, releases)
	c.mu.Lock()
	c.items[urlKey] = feedSnapshot{releases: cp, at: time.Now().UnixNano()}
	c.mu.Unlock()
}

// Get returns the cached releases for a url_key and whether the key was found.
// The returned slice is a copy and is safe for the caller to filter in place.
func (c *FeedCache) Get(urlKey string) ([]domain.Release, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	snap, ok := c.items[urlKey]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	cp := make([]domain.Release, len(snap.releases))
	copy(cp, snap.releases)
	return cp, true
}
