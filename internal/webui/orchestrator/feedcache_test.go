//go:build webui

package orchestrator

import (
	"testing"

	"feedwatch/internal/domain"
)

func TestFeedCacheMissThenHit(t *testing.T) {
	c := NewFeedCache()
	if _, ok := c.Get("key1"); ok {
		t.Fatal("fresh cache should miss")
	}
	rs := []domain.Release{release("id-1", "1.2.3"), release("id-2", "1.2.4")}
	c.Put("key1", rs)

	got, ok := c.Get("key1")
	if !ok || len(got) != 2 {
		t.Fatalf("Get after Put = %v, %v", got, ok)
	}
	if got[0].ID != "id-1" || got[1].Version != "1.2.4" {
		t.Fatalf("unexpected cached releases: %+v", got)
	}
}

func TestFeedCacheEmptyPutIgnored(t *testing.T) {
	c := NewFeedCache()
	c.Put("key1", []domain.Release{release("id-1", "1.2.3")})
	// A 304 carries no releases and must not wipe the previous snapshot.
	c.Put("key1", nil)
	got, ok := c.Get("key1")
	if !ok || len(got) != 1 {
		t.Fatalf("empty Put overwrote cache: %v, %v", got, ok)
	}
}

func TestFeedCacheReturnsCopy(t *testing.T) {
	c := NewFeedCache()
	c.Put("key1", []domain.Release{release("id-1", "1.2.3")})
	got, _ := c.Get("key1")
	got[0].Version = "MUTATED"
	again, _ := c.Get("key1")
	if again[0].Version != "1.2.3" {
		t.Fatal("cache must return a defensive copy")
	}
}

func TestFeedCacheNilReceiverSafe(t *testing.T) {
	var c *FeedCache
	c.Put("key1", []domain.Release{release("id-1", "1.2.3")}) // no panic
	if _, ok := c.Get("key1"); ok {
		t.Fatal("nil cache should always miss")
	}
}
