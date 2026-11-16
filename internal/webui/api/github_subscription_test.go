//go:build webui

package api

import (
	"fmt"
	"net/http"
	"testing"

	"feedwatch/internal/webui"
)

func TestGithubSubscriptionUsesRepoURLKey(t *testing.T) {
	ts, db := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")

	out := c.do(http.MethodPost, "/api/subscriptions", map[string]any{
		"url":          "Acme/App",
		"display_name": "Acme App",
		"source_type":  "github",
		"filter":       map[string]any{"release_scope": "prerelease"},
	}, http.StatusCreated)
	key, _ := out["url_key"].(string)
	wantNormalized, wantKey, err := webui.RepoURLKey("Acme/App")
	if err != nil {
		t.Fatal(err)
	}
	if key != wantKey {
		t.Errorf("url_key = %q, want %q", key, wantKey)
	}
	var storedURL, storedType, storedFilter string
	if err := db.QueryRow(`SELECT url, source_type, filter_json FROM subscriptions WHERE url_key=?`, key).
		Scan(&storedURL, &storedType, &storedFilter); err != nil {
		t.Fatal(err)
	}
	if storedURL != wantNormalized || storedType != "github" {
		t.Errorf("stored = %q / %q, want %q / github", storedURL, storedType, wantNormalized)
	}
	if storedFilter != `{"release_scope":"prerelease"}` {
		t.Errorf("filter_json = %q", storedFilter)
	}
	// feed_pool and the user's baseline must share the same key.
	var poolType string
	if err := db.QueryRow(`SELECT source_type FROM feed_pool WHERE url_key=?`, key).Scan(&poolType); err != nil {
		t.Fatalf("feed_pool row missing: %v", err)
	}
	if poolType != "github" {
		t.Errorf("feed_pool source_type = %q, want github", poolType)
	}

	// A duplicate subscription must conflict, not create a second row.
	c.do(http.MethodPost, "/api/subscriptions", map[string]any{
		"url": "https://github.com/acme/app", "source_type": "github",
	}, http.StatusConflict)
}

func TestGithubSubscriptionRejectsBadScopeAndType(t *testing.T) {
	ts, _ := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")
	c.do(http.MethodPost, "/api/subscriptions", map[string]any{
		"url": "acme/app", "source_type": "github",
		"filter": map[string]any{"release_scope": "nightly"},
	}, http.StatusBadRequest)
	c.do(http.MethodPost, "/api/subscriptions", map[string]any{
		"url": "acme/app", "source_type": "unknown",
	}, http.StatusBadRequest)
}

func TestSubscriptionSourceTypeIsImmutable(t *testing.T) {
	ts, _ := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")
	out := c.do(http.MethodPost, "/api/subscriptions", map[string]any{
		"url": "https://example.com/a.atom", "source_type": "atom",
	}, http.StatusCreated)
	_ = out
	list := c.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
	items, _ := list["subscriptions"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one subscription, got %d", len(items))
	}
	id := items[0].(map[string]any)["id"].(float64)
	c.do(http.MethodPatch, "/api/subscriptions/"+fmt.Sprintf("%.0f", id), map[string]any{
		"source_type": "github",
	}, http.StatusBadRequest)
}

func TestCatalogURLKeyAcceptsGithubRepo(t *testing.T) {
	first, key1, err := webui.CatalogURLKey("zsyo/feedwatch")
	if err != nil {
		t.Fatal(err)
	}
	second, key2, err := webui.CatalogURLKey("https://github.com/Zsyo/FeedWatch.git")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || key1 != key2 {
		t.Errorf("equivalent repo spellings differ: %q/%s vs %q/%s", first, key1, second, key2)
	}
}
