package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"feedwatch/internal/source"
)

// graphQLBody builds a GraphQL response with one alias per repo. A nil entry
// means the repository node was null (missing or inaccessible).
func graphQLBody(repos []string, releases map[string][]graphQLRelease) string {
	data := make(map[string]any)
	for i, repo := range repos {
		alias := aliasFor(i)
		if nodes, ok := releases[repo]; ok {
			data[alias] = map[string]any{
				"repository": map[string]any{
					"releases": map[string]any{"nodes": nodes},
				},
			}
		} else {
			data[alias] = map[string]any{"repository": nil}
		}
	}
	b, _ := json.Marshal(map[string]any{"data": data})
	return string(b)
}

func gqlNode(id int64, tag string, prerelease bool) graphQLRelease {
	return graphQLRelease{
		DatabaseID: &id, Name: tag, TagName: tag, DescriptionHTML: tag + " notes",
		URL:          "https://github.com/acme/app/releases/tag/" + tag,
		IsPrerelease: prerelease,
	}
}

func TestBuildQueryUsesAliasesAndBoundsFirst(t *testing.T) {
	q := buildQuery([]string{"acme/app", "other/repo"}, 500)
	if !strings.Contains(q, `r0: repository(owner: "acme", name: "app")`) ||
		!strings.Contains(q, `r1: repository(owner: "other", name: "repo")`) {
		t.Errorf("missing aliases:\n%s", q)
	}
	if !strings.Contains(q, "releases(first: 100)") {
		t.Errorf("first must be capped at 100:\n%s", q)
	}
	if strings.Contains(q, "first: 500") {
		t.Errorf("unbounded first:\n%s", q)
	}
}

func TestNewGraphQLBatchFetcherRequiresToken(t *testing.T) {
	if got := NewGraphQLBatchFetcher("  ", nil, quietLogger()); got != nil {
		t.Fatal("a missing token must disable the batch channel")
	}
	got := NewGraphQLBatchFetcher("token", nil, quietLogger())
	if got == nil {
		t.Fatal("a token should enable the batch channel")
	}
}

func TestPrefetchMapsNodesAndPartialFailures(t *testing.T) {
	repos := []string{"acme/app", "acme/missing"}
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected request %s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req struct{ Query string }
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.Query, "r0:") || !strings.Contains(req.Query, "r1:") {
			t.Errorf("query missing aliases: %s", req.Query)
		}
		_, _ = io.WriteString(w, graphQLBody(repos, map[string][]graphQLRelease{
			"acme/app": {gqlNode(10, "v1.0.0", false), gqlNode(11, "v1.1.0-rc1", true)},
		}))
	}))
	b := NewGraphQLBatchFetcher("token", srv.Client(), quietLogger())
	b.endpoint = srv.URL
	got := b.Prefetch(context.Background(), repos)
	if len(got["acme/app"].Nodes) != 2 || got["acme/app"].Err != nil {
		t.Errorf("acme/app result: %+v", got["acme/app"])
	}
	if got["acme/missing"].Err == nil {
		t.Errorf("missing repository must carry an error for REST fallback")
	}
}

func TestPrefetchBatchesLargeRepoLists(t *testing.T) {
	repos := make([]string, 35)
	for i := range repos {
		repos[i] = "acme/repo" + string(rune('a'+i/10)) + string(rune('a'+i%10))
	}
	var hits int
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	b := NewGraphQLBatchFetcher("token", srv.Client(), quietLogger())
	b.endpoint = srv.URL
	got := b.Prefetch(context.Background(), repos)
	if hits != 2 {
		t.Errorf("expected 2 POSTs for 35 repos at 30/batch, got %d", hits)
	}
	if len(got) != len(repos) {
		t.Errorf("expected a result per repo, got %d", len(got))
	}
	for _, repo := range repos {
		if res, ok := got[repo]; !ok || res.Err == nil {
			t.Errorf("missing repos must be marked for REST fallback: %s -> %+v", repo, res)
		}
	}
}

func TestPrefetchTopLevelErrorFallsBack(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"API rate limit exceeded"}]}`)
	}))
	b := NewGraphQLBatchFetcher("token", srv.Client(), quietLogger())
	b.endpoint = srv.URL
	got := b.Prefetch(context.Background(), []string{"acme/app"})
	if got["acme/app"].Err == nil || !strings.Contains(got["acme/app"].Err.Error(), "rate limit") {
		t.Errorf("top-level error must fall back: %+v", got["acme/app"])
	}
}

type fakeBatch struct {
	calls int
	mu    sync.Mutex
}

func (f *fakeBatch) Prefetch(_ context.Context, repos []string) map[string]BatchResult {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	out := make(map[string]BatchResult, len(repos))
	for _, repo := range repos {
		out[repo] = BatchResult{Nodes: []graphQLRelease{gqlNode(20, "v9.0.0", false)}}
	}
	return out
}

func TestSourceUsesCacheAndScope(t *testing.T) {
	var hits int
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, releasesBody)
	}))
	s := testSource(t, srv, map[string]string{"release_scope": "stable"})
	batch := &fakeBatch{}
	ctx := PrefetchInto(context.Background(), batch, []source.Source{s})
	if batch.calls != 1 {
		t.Errorf("expected one prefetch, got %d", batch.calls)
	}
	res, err := s.Fetch(ctx, source.Cursor{ETag: `"rest-etag"`})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("cached fetch must not hit REST, got %d requests", hits)
	}
	if len(res.Releases) != 1 || res.Releases[0].ID != "github:20" {
		t.Errorf("unexpected cached releases: %+v", res.Releases)
	}
	if res.ETag != "" || res.LastModified != "" {
		t.Errorf("GraphQL results must not overwrite the REST cursor: %+v", res)
	}
}

func TestSourceFallsBackToRESTOnCacheError(t *testing.T) {
	var hits int
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, releasesBody)
	}))
	s := testSource(t, srv, nil)
	cache := NewCache()
	cache.Put(s.RepoKey(), BatchResult{Err: errors.New("graphql failed")})
	ctx := WithCache(context.Background(), cache)
	if _, err := s.Fetch(ctx, source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("expected REST fallback, got %d requests", hits)
	}
}

func TestCacheConcurrentReadSafe(t *testing.T) {
	c := NewCache()
	c.Put("acme/app", BatchResult{Nodes: []graphQLRelease{gqlNode(1, "v1", false)}})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 100 {
				res, _, ok := c.Get("acme/app")
				if !ok || len(res.Nodes) != 1 {
					t.Errorf("unexpected cache result: %+v", res)
					return
				}
			}
		})
	}
	wg.Wait()
}
