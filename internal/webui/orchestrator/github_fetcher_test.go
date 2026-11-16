//go:build webui

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"feedwatch/internal/domain"
	"feedwatch/internal/source"
	githubsource "feedwatch/internal/source/github"
)

func TestRegistryFetcherBuildsBySourceType(t *testing.T) {
	var gotPath, gotAccept string
	var hits int
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		_, _ = io.WriteString(w, `[
		  {"id":1,"tag_name":"v1.0.0","name":"Stable","html_url":"https://github.com/a/b/releases/tag/v1.0.0",
		   "created_at":"2026-11-01T00:00:00Z","updated_at":"2026-11-01T00:00:00Z","prerelease":false,"draft":false},
		  {"id":2,"tag_name":"v2.0.0-rc1","name":"RC","html_url":"https://github.com/a/b/releases/tag/v2.0.0-rc1",
		   "created_at":"2026-11-02T00:00:00Z","updated_at":"2026-11-02T00:00:00Z","prerelease":true,"draft":false}
		]`)
	}))
	reg := source.NewRegistry()
	reg.Register("atom", func(spec source.SourceSpec, log *slog.Logger) (source.Source, error) {
		t.Fatal("atom factory must not be used for a github subscription")
		return nil, nil
	})
	reg.Register(githubsource.SourceType, func(spec source.SourceSpec, log *slog.Logger) (source.Source, error) {
		if spec.URL != "acme/app" {
			t.Errorf("source URL = %q, want bare owner/repo", spec.URL)
		}
		if spec.Options["release_scope"] != "stable" {
			t.Errorf("release_scope = %q, want stable", spec.Options["release_scope"])
		}
		return githubsource.New(spec, log)
	})
	// Client() starts the test server and fills in srv.URL (Go 1.27 lazy start).
	client := srv.Client()
	f := NewRegistryFetcher(reg, map[string]string{
		"api_base": srv.URL, "retry_max": "0",
	}, quietLog()).WithGitHubCredentials("", client)

	res, err := f.Fetch(context.Background(), "github:acme/app", "github", "stable", source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 || gotPath != "/repos/acme/app/releases" || !strings.Contains(gotAccept, "vnd.github") {
		t.Errorf("unexpected request: hits=%d path=%q accept=%q", hits, gotPath, gotAccept)
	}
	if len(res.Releases) != 1 || res.Releases[0].ID != "github:1" || !res.Releases[0].IsStable {
		t.Errorf("unexpected releases: %+v", res.Releases)
	}
}

func TestReleaseScopeFromFilterJSON(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"stable_only":true,"release_scope":"prerelease"}`, "prerelease"},
		{`{"release_scope":"all"}`, "all"},
		{`{"stable_only":true}`, ""},
		{"", ""},
		{"not-json", ""},
	}
	for _, tc := range cases {
		if got := releaseScopeFrom(tc.raw); got != tc.want {
			t.Errorf("releaseScopeFrom(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestFilterForTrustsGitHubSource(t *testing.T) {
	f, err := filterFor(`{"stable_only":true,"release_scope":"prerelease"}`, "github")
	if err != nil {
		t.Fatal(err)
	}
	// A prerelease title must pass because the source already applied the
	// authoritative scope; the title heuristic must not override IsStable.
	r := domain.Release{Title: "v2.0.0-rc1", ID: "x", IsStable: false}
	kept, ok := f.Keep(r)
	if !ok || kept.IsStable {
		t.Errorf("github prerelease was filtered or mislabeled: ok=%v rel=%+v", ok, kept)
	}
}
