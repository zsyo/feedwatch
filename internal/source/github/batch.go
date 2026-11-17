package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"feedwatch/internal/source"
)

// This file implements the optional GraphQL batch channel. It deliberately
// does not touch source.Source: each poll cycle prefetches every GitHub repo
// once into a context-carried cache, and individual Source.Fetch calls read
// from that cache (falling back to REST on a miss or an error). This keeps the
// watcher/orchestrator diff logic completely source-agnostic.

const (
	graphQLEndpoint = "https://api.github.com/graphql"
	batchSize       = 30 // aliases per POST; keeps payload and node count small
	graphQLFirst    = 10 // releases fetched per repository
)

// BatchResult is one repository's outcome from a prefetch. Nodes are the raw
// GraphQL release nodes: each subscription may apply a different scope and
// body policy, so filtering stays in the source, not here. Err is set for a
// missing/inaccessible repository or a query error; the source then falls
// back to REST so one bad repo cannot poison the whole batch.
type BatchResult struct {
	Nodes []graphQLRelease
	Err   error
}

// BatchFetcher prefetches a batch of repositories in one (or a few) calls.
type BatchFetcher interface {
	Prefetch(ctx context.Context, repos []string) map[string]BatchResult
}

// GraphQLBatchFetcher talks to GitHub's GraphQL endpoint with aliased
// repository queries. GraphQL requires a token and has no ETag/304, so every
// cycle is a full fetch of the most recent releases; REST stays the primary
// path for cheap conditional polling when this channel is disabled.
type GraphQLBatchFetcher struct {
	token    string
	client   *http.Client
	endpoint string
	first    int
	log      *slog.Logger
}

// NewGraphQLBatchFetcher returns nil when no token is configured: the caller
// treats nil as "batch channel disabled" and every source uses REST.
func NewGraphQLBatchFetcher(token string, client *http.Client, log *slog.Logger) *GraphQLBatchFetcher {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &GraphQLBatchFetcher{
		token:    strings.TrimSpace(token),
		client:   client,
		endpoint: graphQLEndpoint,
		first:    graphQLFirst,
		log:      log,
	}
}

func (b *GraphQLBatchFetcher) Prefetch(ctx context.Context, repos []string) map[string]BatchResult {
	out := make(map[string]BatchResult, len(repos))
	for start := 0; start < len(repos); start += batchSize {
		end := min(start+batchSize, len(repos))
		batch := repos[start:end]
		results, err := b.prefetchBatch(ctx, batch)
		if err != nil {
			b.log.Warn("github graphql batch failed, falling back to REST",
				"repos", len(batch), "err", err)
			for _, repo := range batch {
				out[repo] = BatchResult{Err: err}
			}
			continue
		}
		maps.Copy(out, results)
		// Any alias absent from the response also falls back to REST.
		for _, repo := range batch {
			if _, ok := out[repo]; !ok {
				out[repo] = BatchResult{Err: fmt.Errorf("github graphql: no result for %s", repo)}
			}
		}
	}
	return out
}

func (b *GraphQLBatchFetcher) prefetchBatch(ctx context.Context, repos []string) (map[string]BatchResult, error) {
	query := buildQuery(repos, b.first)
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "feedwatch/1.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST graphql: %w", err)
	}
	defer resp.Body.Close()
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
		b.log.Debug("github graphql rate limit", "remaining", remaining, "resource", "graphql")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("body exceeds %d bytes", maxBodyBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &statusError{code: resp.StatusCode}
	}
	var parsed graphQLResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse graphql response: %w", err)
	}
	if len(parsed.Errors) > 0 {
		// Top-level errors (bad query, rate limit) fail the whole batch; the
		// caller downgrades every repo to REST.
		return nil, fmt.Errorf("graphql error: %s", parsed.Errors[0].Message)
	}

	out := make(map[string]BatchResult, len(repos))
	for i, repo := range repos {
		alias := aliasFor(i)
		entry, ok := parsed.Data[alias]
		if !ok || entry.Repository == nil {
			out[repo] = BatchResult{Err: fmt.Errorf("github graphql: repository %s not found or inaccessible", repo)}
			continue
		}
		out[repo] = BatchResult{Nodes: entry.Repository.Releases.Nodes}
	}
	return out, nil
}

func aliasFor(i int) string { return "r" + strconv.Itoa(i) }

// buildQuery emits one aliased repository connection per repo. first is
// bounded to GitHub's 1..100 connection limit.
func buildQuery(repos []string, first int) string {
	if first < 1 {
		first = 1
	}
	if first > 100 {
		first = 100
	}
	var b strings.Builder
	b.WriteString("query {\n")
	for i, repo := range repos {
		owner, name := Repo(repo)
		fmt.Fprintf(&b, "  %s: repository(owner: %q, name: %q) {\n", aliasFor(i), owner, name)
		fmt.Fprintf(&b, "    releases(first: %d) {\n      nodes {\n", first)
		b.WriteString("        databaseId name tagName description descriptionHTML url\n")
		b.WriteString("        isPrerelease isDraft publishedAt updatedAt\n")
		b.WriteString("      }\n    }\n  }\n")
	}
	b.WriteString("}")
	return b.String()
}

// ---- per-cycle context cache ------------------------------------------------

type cacheKey struct{}

// Cache holds one cycle's prefetched results. Entries are read-only after
// Prefetch completes, but a mutex keeps the -race detector happy when several
// fetch workers read concurrently.
type Cache struct {
	mu      sync.RWMutex
	results map[string]BatchResult
}

func NewCache() *Cache { return &Cache{results: make(map[string]BatchResult)} }

func (c *Cache) Put(repo string, res BatchResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[repo] = res
}

// Get returns the cached result, whether a entry exists, and whether it is
// usable (no error). A miss or an errored entry both mean "use REST".
func (c *Cache) Get(repo string) (BatchResult, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res, ok := c.results[repo]
	return res, ok, ok && res.Err == nil
}

// WithCache attaches a cycle's batch cache to ctx.
func WithCache(ctx context.Context, c *Cache) context.Context {
	return context.WithValue(ctx, cacheKey{}, c)
}

// CacheFrom returns the cache attached to ctx, or nil when the channel is off.
func CacheFrom(ctx context.Context) *Cache {
	c, _ := ctx.Value(cacheKey{}).(*Cache)
	return c
}

// repoSource is the capability a source exposes to participate in batch
// prefetch. Only GitHub sources implement it; atom sources are untouched.
type repoSource interface{ RepoKey() string }

// PrefetchInto collects the GitHub repos in sources, prefetches them once, and
// returns a context carrying the cache. The orchestrator calls this before a
// poll cycle's fetch loop; sources read it via CacheFrom.
func PrefetchInto(ctx context.Context, batch BatchFetcher, sources []source.Source) context.Context {
	if batch == nil {
		return ctx
	}
	repos := make([]string, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		gs, ok := src.(repoSource)
		if !ok {
			continue
		}
		repo := gs.RepoKey()
		if repo == "" {
			continue
		}
		if _, dup := seen[repo]; dup {
			continue
		}
		seen[repo] = struct{}{}
		repos = append(repos, repo)
	}
	if len(repos) == 0 {
		return ctx
	}
	cache := NewCache()
	for repo, res := range batch.Prefetch(ctx, repos) {
		cache.Put(repo, res)
	}
	return WithCache(ctx, cache)
}
