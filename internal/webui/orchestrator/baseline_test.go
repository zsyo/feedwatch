//go:build webui

package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/source"
	"feedwatch/internal/webui"
	"feedwatch/internal/webui/store"
)

func newBaselineHarness(t *testing.T, f Fetcher, cache *FeedCache) (*Baseline, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := NewBaseline(st, f, cache, nil, "[FW]", quietLog())
	b.now = func() time.Time { return fixedTime }
	return b, st
}

func baselineSub(urlKey string) BaselineSubscription {
	return BaselineSubscription{
		URL: feedA, URLKey: urlKey, SourceType: "atom",
		FilterJSON: `{"stable_only":true}`, DisplayName: "Go",
	}
}

func TestBaselineCacheHitSkipsFetcher(t *testing.T) {
	key := "key-hit"
	cache := NewFeedCache()
	cache.Put(key, []domain.Release{release("id-1", "1.2.3")})
	// A fetcher that fails loudly: any call means the cache was not used.
	f := &fakeFetcher{errs: map[string]error{feedA: context.DeadlineExceeded}}
	b, st := newBaselineHarness(t, f, cache)
	db := st.DB()
	u := addUser(t, db, "alice")
	addChannel(t, db, u)

	if err := b.Push(context.Background(), u, baselineSub(key)); err != nil {
		t.Fatal(err)
	}
	if f.callCount() != 0 {
		t.Fatalf("cache hit still called fetcher %d times", f.callCount())
	}
	envs := notificationsEnvelopes(t, db)
	if len(envs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(envs))
	}
	env := envs[0]
	if env.UserID != u || len(env.ChannelIDs) != 1 {
		t.Fatalf("bad envelope: %+v", env)
	}
	if env.Note.Kind != domain.KindBaseline || len(env.Note.Items) != 1 {
		t.Fatalf("bad note: %+v", env.Note)
	}
	item := env.Note.Items[0]
	if item.Feed != "Go" || item.Version != "1.2.3" || item.URL != feedA {
		t.Fatalf("bad item: %+v", item)
	}
	if item.IsNew {
		t.Fatal("baseline item must not be flagged as a new release")
	}
}

func TestBaselineCacheMissFetchesAndFillsCache(t *testing.T) {
	key := "key-miss"
	cache := NewFeedCache()
	f := &fakeFetcher{results: map[string]source.Result{
		feedA: {Releases: []domain.Release{release("id-1", "1.2.3"), release("id-2", "1.2.4")}},
	}}
	b, st := newBaselineHarness(t, f, cache)
	db := st.DB()
	u := addUser(t, db, "alice")
	addChannel(t, db, u)

	if err := b.Push(context.Background(), u, baselineSub(key)); err != nil {
		t.Fatal(err)
	}
	if f.callCount() != 1 {
		t.Fatalf("fetcher called %d times, want 1", f.callCount())
	}
	if cached, ok := cache.Get(key); !ok || len(cached) != 2 {
		t.Fatalf("cache not filled after fetch: %v, %v", cached, ok)
	}
	envs := notificationsEnvelopes(t, db)
	if len(envs) != 1 || envs[0].Note.Items[0].Version != "1.2.4" {
		t.Fatalf("baseline should show latest stable 1.2.4: %+v", envs)
	}
	// A second subscriber to the same key hits the cache, no extra fetch.
	u2 := addUser(t, db, "bob")
	addChannel(t, db, u2)
	if err := b.Push(context.Background(), u2, baselineSub(key)); err != nil {
		t.Fatal(err)
	}
	if f.callCount() != 1 {
		t.Fatalf("second subscriber caused a redundant fetch: %d", f.callCount())
	}
}

func TestBaselineDoesNotAdvanceSeenState(t *testing.T) {
	key := "key-seen"
	cache := NewFeedCache()
	cache.Put(key, []domain.Release{release("id-1", "1.2.3"), release("id-2", "1.2.4")})
	f := &fakeFetcher{}
	b, st := newBaselineHarness(t, f, cache)
	db := st.DB()
	u := addUser(t, db, "alice")
	addChannel(t, db, u)

	if err := b.Push(context.Background(), u, baselineSub(key)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM seen_ids`); got != 0 {
		t.Fatalf("baseline wrote global seen_ids: %d", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM user_seen`); got != 0 {
		t.Fatalf("baseline wrote user_seen: %d", got)
	}
}

func TestBaselineNoChannelSkipsNotification(t *testing.T) {
	key := "key-nochan"
	cache := NewFeedCache()
	cache.Put(key, []domain.Release{release("id-1", "1.2.3")})
	b, st := newBaselineHarness(t, &fakeFetcher{}, cache)
	db := st.DB()
	u := addUser(t, db, "alice") // no channel

	if err := b.Push(context.Background(), u, baselineSub(key)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notifications`); got != 0 {
		t.Fatalf("user without channel got a notification: %d", got)
	}
}

func TestBaselineFallsBackToPoolVersion(t *testing.T) {
	key := "key-fallback"
	normalized, _, err := webui.URLKey(feedA)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewFeedCache()
	// Only a prerelease in cache; stable_only filter will drop it.
	pre := release("id-pre", "1.3.0-rc1")
	pre.IsStable = false
	cache.Put(key, []domain.Release{pre})
	b, st := newBaselineHarness(t, &fakeFetcher{}, cache)
	db := st.DB()
	u := addUser(t, db, "alice")
	addChannel(t, db, u)
	mustExecDB(t, db, `INSERT INTO feed_pool (url_key, url, source_type, last_version) VALUES (?,?,?,?)`,
		key, normalized, "atom", "1.2.9")

	if err := b.Push(context.Background(), u, baselineSub(key)); err != nil {
		t.Fatal(err)
	}
	envs := notificationsEnvelopes(t, db)
	if len(envs) != 1 || envs[0].Note.Items[0].Version != "1.2.9" {
		t.Fatalf("baseline should fall back to pool version 1.2.9: %+v", envs)
	}
}

func TestBaselinePushBaselineAdapter(t *testing.T) {
	key := "key-adapter"
	cache := NewFeedCache()
	cache.Put(key, []domain.Release{release("id-1", "1.2.3")})
	b, st := newBaselineHarness(t, &fakeFetcher{}, cache)
	db := st.DB()
	u := addUser(t, db, "alice")
	addChannel(t, db, u)

	sub := baselineSub(key)
	if err := b.PushBaseline(context.Background(), u, sub.URL, sub.URLKey, sub.SourceType, sub.FilterJSON, sub.DisplayName); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM notifications`); got != 1 {
		t.Fatalf("adapter produced %d notifications, want 1", got)
	}
}

func mustExecDB(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
