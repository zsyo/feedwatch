// Package atom implements source.Source for Atom 1.0 / RSS 2.0 feeds.
package atom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"feedwatch/internal/source"
)

const maxBodyBytes = 4 << 20 // 4 MiB guard against an abnormal response

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("atom: HTTP %d", e.code) }

type Source struct {
	name           string
	url            string
	userAgent      string
	client         *http.Client
	policy         contentPolicy
	retryMax       int
	retryBaseDelay time.Duration
	conditional    bool
	log            *slog.Logger
}

func optionDuration(o map[string]string, key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(o[key])
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("atom: option %s: %w", key, err)
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
		return 0, fmt.Errorf("atom: option %s must be a non-negative integer, got %q", key, v)
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
		return false, fmt.Errorf("atom: option %s must be a boolean, got %q", key, v)
	}
	return b, nil
}

func newSource(spec source.SourceSpec, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	url := strings.TrimSpace(spec.URL)
	if url == "" {
		return nil, errors.New("atom: url is required")
	}
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
	ua := strings.TrimSpace(spec.Options["user_agent"])
	if ua == "" {
		ua = "feedwatch/1.0"
	}
	client := spec.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Source{
		name:           spec.Name,
		url:            url,
		userAgent:      ua,
		client:         client,
		policy:         parsePolicy(spec.Options["content_policy"]),
		retryMax:       retryMax,
		retryBaseDelay: base,
		conditional:    conditional,
		log:            log,
	}, nil
}

// New matches source.Factory and is registered by the composition root.
func New(spec source.SourceSpec, log *slog.Logger) (source.Source, error) {
	return newSource(spec, log)
}

func (s *Source) Type() string { return "atom" }

func (s *Source) Fetch(ctx context.Context, since source.Cursor) (source.Result, error) {
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
		s.log.Warn("atom fetch failed, retrying",
			"feed", s.name, "attempt", attempt+1, "err", err)
	}
	return source.Result{}, lastErr
}

func (s *Source) backoff(attempt int, retryAfter time.Duration) time.Duration {
	exponential := min(s.retryBaseDelay*time.Duration(1<<min(attempt, 10)), 30*time.Second)
	// Honour a server-provided Retry-After even when it is longer, but never
	// sleep more than 60s in one attempt.
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
		return zero, false, 0, fmt.Errorf("atom: build request: %w", err)
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/xml;q=0.9, */*;q=0.8")
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
		return zero, true, 0, fmt.Errorf("atom: GET %s: %w", s.url, err)
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
		return zero, true, 0, fmt.Errorf("atom: read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return zero, false, 0, fmt.Errorf("atom: body exceeds %d bytes", maxBodyBytes)
	}
	releases, err := parseFeed(body, s.name, s.policy)
	if err != nil {
		return zero, false, 0, err
	}
	s.log.Debug("atom feed fetched", "feed", s.name, "entries", len(releases))
	return source.Result{Releases: releases, ETag: etag, LastModified: lastModified}, false, 0, nil
}

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
			// Use the injected now (like the Retry-After branch) instead of the
			// wall clock, so this stays deterministic under test.
			if d := time.Unix(epoch, 0).Sub(now); d > 0 {
				return d
			}
		}
	}
	return 0
}
