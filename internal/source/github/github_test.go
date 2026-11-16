package github

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"feedwatch/internal/source"
)

const releasesBody = `[
  {"id":101,"tag_name":"v2.0.0","name":"Version 2.0","body":"stable notes",
   "html_url":"https://github.com/acme/app/releases/tag/v2.0.0",
   "created_at":"2026-11-01T00:00:00Z","published_at":"2026-11-01T01:00:00Z",
   "updated_at":"2026-11-01T01:00:00Z","prerelease":false,"draft":false},
  {"id":102,"tag_name":"v2.1.0-rc.1","name":"Release Candidate 1","body":"rc notes",
   "html_url":"https://github.com/acme/app/releases/tag/v2.1.0-rc.1",
   "created_at":"2026-11-05T00:00:00Z","published_at":"2026-11-05T00:00:00Z",
   "updated_at":"2026-11-05T00:00:00Z","prerelease":true,"draft":false},
  {"id":103,"tag_name":"v3.0.0-draft","name":null,"body":null,
   "html_url":"","created_at":"2026-11-06T00:00:00Z","published_at":null,
   "updated_at":"2026-11-06T00:00:00Z","prerelease":false,"draft":true}
]`

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testSource(t *testing.T, srv *httptest.Server, opts map[string]string) *Source {
	t.Helper()
	merged := map[string]string{
		"retry_max":        "2",
		"retry_base_delay": "1ms",
		"api_base":         srv.URL,
	}
	maps.Copy(merged, opts)
	client := srv.Client()
	s, err := newSource(source.SourceSpec{
		Name:    "acme/app",
		Type:    SourceType,
		URL:     "Acme/App",
		Options: merged,
		Client:  client,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNormalizeRepo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zsyo/feedwatch", "zsyo/feedwatch"},
		{"  Zsyo/FeedWatch  ", "zsyo/feedwatch"},
		{"https://github.com/Zsyo/FeedWatch", "zsyo/feedwatch"},
		{"https://github.com/Zsyo/FeedWatch.git", "zsyo/feedwatch"},
		{"github.com/zsyo/feedwatch/", "zsyo/feedwatch"},
	}
	for _, tc := range cases {
		got, err := NormalizeRepo(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeRepo(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "owner", "a/b/c", "a b/c", "https://example.com/a/b"} {
		if _, err := NormalizeRepo(bad); err == nil {
			t.Errorf("NormalizeRepo(%q) should fail", bad)
		}
	}
}

func TestFetchScopeFiltering(t *testing.T) {
	cases := []struct {
		scope    string
		wantIDs  []string
		wantTech bool
	}{
		{"stable", []string{"github:101"}, true},
		{"prerelease", []string{"github:102"}, false},
		{"all", []string{"github:101", "github:102"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/app/releases" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				_, _ = io.WriteString(w, releasesBody)
			}))
			s := testSource(t, srv, map[string]string{"release_scope": tc.scope})
			res, err := s.Fetch(context.Background(), source.Cursor{})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			var ids []string
			for _, r := range res.Releases {
				ids = append(ids, r.ID)
				if r.IsStable != (r.ID == "github:101") {
					t.Errorf("release %s IsStable = %v", r.ID, r.IsStable)
				}
				if tc.scope == "stable" && strings.Contains(r.Title, "Release Candidate") {
					t.Errorf("prerelease leaked Title into stable scope")
				}
			}
			if len(ids) != len(tc.wantIDs) {
				t.Fatalf("scope %s: got %v, want %v", tc.scope, ids, tc.wantIDs)
			}
			for i := range ids {
				if ids[i] != tc.wantIDs[i] {
					t.Errorf("scope %s: got %v, want %v", tc.scope, ids, tc.wantIDs)
				}
			}
		})
	}
}

func TestFetchMapsFieldsAndSendsHeaders(t *testing.T) {
	var gotAuth, gotAccept, gotVersion string
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		w.Header().Set("ETag", `"etag-1"`)
		w.Header().Set("Last-Modified", "Tue, 20 Oct 2026 01:00:00 GMT")
		_, _ = io.WriteString(w, releasesBody)
	}))
	s := testSource(t, srv, map[string]string{
		"release_scope": "stable", "token": "ghp_test", "content_policy": "html",
	})
	res, err := s.Fetch(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ghp_test" || gotAccept != "application/vnd.github+json" || gotVersion != "2022-11-28" {
		t.Errorf("headers: auth=%q accept=%q version=%q", gotAuth, gotAccept, gotVersion)
	}
	if len(res.Releases) != 1 {
		t.Fatalf("got %d releases", len(res.Releases))
	}
	r := res.Releases[0]
	if r.Title != "Version 2.0" || r.Version != "v2.0.0" || r.CoreVersion != "2.0.0" {
		t.Errorf("unexpected release: %+v", r)
	}
	if r.URL != "https://github.com/acme/app/releases/tag/v2.0.0" {
		t.Errorf("unexpected URL %q", r.URL)
	}
	if r.Summary != "stable notes" || r.ContentHTML != "stable notes" {
		t.Errorf("unexpected body: %+v", r)
	}
	if res.ETag != `"etag-1"` || res.LastModified == "" {
		t.Errorf("unexpected result metadata: %+v", res)
	}
}

func TestFetchConditionalAnd304(t *testing.T) {
	var seenETag string
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenETag = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	s := testSource(t, srv, nil)
	res, err := s.Fetch(context.Background(), source.Cursor{ETag: `"v1"`})
	if err != nil {
		t.Fatal(err)
	}
	if seenETag != `"v1"` || !res.NotModified || len(res.Releases) != 0 {
		t.Errorf("304 handling: etag=%q res=%+v", seenETag, res)
	}
}

func TestFetchRetriesRateLimit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, releasesBody)
	}))
	s := testSource(t, srv, nil)
	if _, err := s.Fetch(context.Background(), source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Errorf("expected 2 requests, got %d", hits.Load())
	}
}

func TestFetchRejectsBadScope(t *testing.T) {
	if _, err := ParseScope("nightly"); err == nil {
		t.Fatal("invalid release_scope must fail")
	}
}

func TestFetchDoesNotRetry404(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	s := testSource(t, srv, nil)
	if _, err := s.Fetch(context.Background(), source.Cursor{}); err == nil {
		t.Fatal("404 must fail")
	}
	if hits.Load() != 1 {
		t.Errorf("404 should not retry, got %d requests", hits.Load())
	}
}
