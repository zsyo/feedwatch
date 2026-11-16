package github

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"feedwatch/internal/source"
)

const (
	maxBodyBytes = 4 << 20 // 4 MiB guard against an abnormal response
	apiBase      = "https://api.github.com"
)

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("github: HTTP %d", e.code) }

// Source fetches the releases of a single GitHub repository via REST. The
// source layer deliberately does no prerelease filtering via title heuristics:
// GitHub's is_prerelease/is_draft flags are authoritative, and the filter
// package is told (via TrustSourceStability) to leave IsStable alone.
type Source struct {
	name        string
	repo        string
	owner       string
	reponame    string
	url         string
	token       string
	userAgent   string
	client      *http.Client
	policy      contentPolicy
	scope       ReleaseScope
	limit       int
	retryMax    int
	retryDelay  time.Duration
	conditional bool
	log         *slog.Logger
}

func optionDuration(o map[string]string, key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(o[key])
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("github: option %s: %w", key, err)
	}
	return d, nil
}

func optionInt(o map[string]string, key string, def int) (int, error) {
	v := strings.TrimSpace(o[key])
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("github: option %s must be a non-negative integer, got %q", key, v)
	}
	return n, nil
}

func optionBool(o map[string]string, key string, def bool) (bool, error) {
	v := strings.TrimSpace(o[key])
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("github: option %s must be a boolean, got %q", key, v)
	}
	return b, nil
}

func newSource(spec source.SourceSpec, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	repo, err := NormalizeRepo(spec.URL)
	if err != nil {
		return nil, err
	}
	owner, name := Repo(repo)
	timeout, err := optionDuration(spec.Options, "http_timeout", 15*time.Second)
	if err != nil {
		return nil, err
	}
	retryMax, err := optionInt(spec.Options, "retry_max", 3)
	if err != nil {
		return nil, err
	}
	base, err := optionDuration(spec.Options, "retry_base_delay", 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	conditional, err := optionBool(spec.Options, "conditional", true)
	if err != nil {
		return nil, err
	}
	scope, err := ParseScope(spec.Options["release_scope"])
	if err != nil {
		return nil, err
	}
	limit, err := optionInt(spec.Options, "release_limit", 0)
	if err != nil {
		return nil, err
	}
	// api_base allows GitHub Enterprise and httptest servers in tests.
	api := strings.TrimRight(strings.TrimSpace(spec.Options["api_base"]), "/")
	if api == "" {
		api = apiBase
	}
	if err != nil {
		return nil, err
	}
	ua := strings.TrimSpace(spec.Options["user_agent"])
	if ua == "" {
		ua = "feedwatch/1.0"
	}
	client := spec.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Source{
		name:        spec.Name,
		repo:        repo,
		owner:       owner,
		reponame:    name,
		url:         fmt.Sprintf("%s/repos/%s/%s/releases", api, owner, name),
		token:       strings.TrimSpace(spec.Options["token"]),
		userAgent:   ua,
		client:      client,
		policy:      parsePolicy(spec.Options["content_policy"]),
		scope:       scope,
		limit:       limit,
		retryMax:    retryMax,
		retryDelay:  base,
		conditional: conditional,
		log:         log,
	}, nil
}

// New matches source.Factory and is registered by both composition roots.
func New(spec source.SourceSpec, log *slog.Logger) (source.Source, error) {
	return newSource(spec, log)
}

func (s *Source) Type() string { return SourceType }

// RepoKey returns the normalised "owner/repo" for batch prefetch collection.
func (s *Source) RepoKey() string { return s.repo }

// Scope returns the configured release scope; used by the batch layer so it
// can apply the same filtering to GraphQL results.
func (s *Source) Scope() ReleaseScope { return s.scope }

// ContentPolicy returns the configured body policy for the batch layer.
func (s *Source) ContentPolicy() contentPolicy { return s.policy }

// Limit returns the maximum number of releases to emit (0 means no limit).
func (s *Source) Limit() int { return s.limit }

// APIPath returns the REST releases endpoint, exposed for tests.
func (s *Source) APIPath() string { return s.url }

func (s *Source) Fetch(ctx context.Context, since source.Cursor) (source.Result, error) {
	// Optional GraphQL batch channel: when this cycle prefetched our repo, use
	// those nodes instead of a REST request. A missing or errored entry falls
	// through to REST. GraphQL has no ETag/304, so the result deliberately
	// leaves the cursor untouched rather than clearing a valid REST validator.
	if cache := CacheFrom(ctx); cache != nil {
		if res, _, ok := cache.Get(s.repo); ok {
			releases := parseGraphQL(res.Nodes, s.name, s.scope, s.policy, s.limit)
			s.log.Debug("github releases served from batch cache", "repo", s.repo, "releases", len(releases))
			return source.Result{Releases: releases}, nil
		}
	}
	var lastErr error
	var retryAfter time.Duration
	for attempt := 0; attempt <= s.retryMax; attempt++ {
		if attempt > 0 {
			if err := wait(ctx, s.backoff(attempt-1, retryAfter)); err != nil {
				return source.Result{}, err
			}
		}
		res, retry, after, err := s.fetchOnce(ctx, since)
		if err == nil {
			return res, nil
		}
		lastErr = err
		retryAfter = after
		if !retry {
			return source.Result{}, err
		}
		if attempt == s.retryMax {
			break
		}
		s.log.Warn("github fetch failed, retrying",
			"repo", s.repo, "attempt", attempt+1, "err", err)
	}
	return source.Result{}, lastErr
}

func (s *Source) backoff(attempt int, retryAfter time.Duration) time.Duration {
	exponential := min(s.retryDelay*time.Duration(1<<min(attempt, 10)), 30*time.Second)
	return min(max(exponential, retryAfter), 60*time.Second)
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Source) fetchOnce(ctx context.Context, since source.Cursor) (source.Result, bool, time.Duration, error) {
	zero := source.Result{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return zero, false, 0, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if s.conditional {
		if since.ETag != "" {
			req.Header.Set("If-None-Match", since.ETag)
		}
		if since.LastModified != "" {
			req.Header.Set("If-Modified-Since", since.LastModified)
		}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, false, 0, ctxErr
		}
		return zero, true, 0, fmt.Errorf("github: GET %s: %w", s.url, err)
	}
	defer resp.Body.Close()

	etag := resp.Header.Get("ETag")
	lastModified := resp.Header.Get("Last-Modified")
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return source.Result{NotModified: true, ETag: etag, LastModified: lastModified}, false, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return zero, true, parseRetryAfter(resp.Header, time.Now()), &statusError{code: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		return zero, false, 0, &statusError{code: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return zero, true, 0, fmt.Errorf("github: read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return zero, false, 0, fmt.Errorf("github: body exceeds %d bytes", maxBodyBytes)
	}
	releases, err := parseREST(body, s.name, s.scope, s.policy, s.limit)
	if err != nil {
		return zero, false, 0, err
	}
	s.log.Debug("github releases fetched", "repo", s.repo, "releases", len(releases))
	return source.Result{Releases: releases, ETag: etag, LastModified: lastModified}, false, 0, nil
}

// parseRetryAfter mirrors the atom source: Retry-After (seconds or HTTP-date)
// and X-RateLimit-Reset (epoch seconds) are both used by GitHub.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := t.Sub(now); d > 0 {
				return d
			}
		}
	}
	if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Unix(epoch, 0).Sub(now); d > 0 {
				return d
			}
		}
	}
	return 0
}

var _ source.Source = (*Source)(nil)
