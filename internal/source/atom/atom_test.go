package atom

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"feedwatch/internal/source"
)

const atomBody = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
<title>Test feed</title>
<entry>
  <id>tag:test/1.2.3</id>
  <title>v1.2.3</title>
  <updated>2026-10-20T01:00:00Z</updated>
  <link rel="alternate" href="https://example.com/releases/tag/v1.2.3"/>
</entry></feed>`

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testSource(t *testing.T, srv *httptest.Server, opts map[string]string) *Source {
	t.Helper()
	merged := map[string]string{
		"retry_max":        "3",
		"retry_base_delay": "1ms",
	}
	maps.Copy(merged, opts)
	// Client() also starts the in-memory server and sets srv.URL, so it must
	// be called before the spec literal is evaluated.
	client := srv.Client()
	s, err := newSource(source.SourceSpec{
		Name:    "test",
		Type:    "atom",
		URL:     srv.URL,
		Options: merged,
		Client:  client,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFetchSuccessAndConditionalHeaders(t *testing.T) {
	var gotETag, gotModified string
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotETag = r.Header.Get("If-None-Match")
		gotModified = r.Header.Get("If-Modified-Since")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Tue, 20 Oct 2026 01:00:00 GMT")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, atomBody)
	}))

	s := testSource(t, srv, nil)
	res, err := s.Fetch(context.Background(), source.Cursor{ETag: `"v1"`, LastModified: "Tue, 20 Oct 2026 01:00:00 GMT"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotETag != `"v1"` || gotModified == "" {
		t.Errorf("conditional headers not sent: %q / %q", gotETag, gotModified)
	}
	if len(res.Releases) != 1 || res.Releases[0].ID != "tag:test/1.2.3" {
		t.Errorf("unexpected releases: %+v", res.Releases)
	}
	if res.ETag != `"v1"` || res.LastModified == "" || res.NotModified {
		t.Errorf("unexpected result metadata: %+v", res)
	}
}

func TestConditionalCanBeDisabled(t *testing.T) {
	var got string
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("If-None-Match")
		_, _ = io.WriteString(w, atomBody)
	}))
	s := testSource(t, srv, map[string]string{"conditional": "false"})
	if _, err := s.Fetch(context.Background(), source.Cursor{ETag: `"v1"`}); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("conditional=false still sent If-None-Match: %q", got)
	}
}

func TestFetchNotModified(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	s := testSource(t, srv, nil)
	res, err := s.Fetch(context.Background(), source.Cursor{ETag: `"v1"`})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotModified || len(res.Releases) != 0 {
		t.Errorf("304 must not produce releases: %+v", res)
	}
}

func TestFetchRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, atomBody)
	}))
	s := testSource(t, srv, nil)
	res, err := s.Fetch(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls.Load() != 3 || len(res.Releases) != 1 {
		t.Errorf("expected 3 attempts and 1 release, got %d / %d", calls.Load(), len(res.Releases))
	}
}

func TestFetch429WithZeroRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, atomBody)
	}))
	s := testSource(t, srv, nil)
	if _, err := s.Fetch(context.Background(), source.Cursor{}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", calls.Load())
	}
}

func TestFetchNonRetryableStatusAndBadBody(t *testing.T) {
	t.Run("404 is not retried", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		s := testSource(t, srv, nil)
		if _, err := s.Fetch(context.Background(), source.Cursor{}); err == nil {
			t.Error("404 should fail")
		}
		if calls.Load() != 1 {
			t.Errorf("404 must not be retried, got %d calls", calls.Load())
		}
	})

	t.Run("malformed XML is not retried", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = io.WriteString(w, "<feed><broken>")
		}))
		s := testSource(t, srv, nil)
		if _, err := s.Fetch(context.Background(), source.Cursor{}); err == nil {
			t.Error("malformed XML should fail")
		}
		if calls.Load() != 1 {
			t.Errorf("parse errors must not be retried, got %d calls", calls.Load())
		}
	})
}

func TestFetchRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	s := testSource(t, srv, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := s.Fetch(ctx, source.Cursor{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context deadline, got %v", err)
	}
}

func TestOptionValidation(t *testing.T) {
	// Option parsing never performs I/O, so no server is needed at all.
	for _, opts := range []map[string]string{
		{"retry_max": "-1"},
		{"retry_base_delay": "tomorrow"},
		{"conditional": "maybe"},
	} {
		_, err := newSource(source.SourceSpec{
			Name: "x", URL: "http://example.test/feed.atom", Options: opts,
		}, quietLogger())
		if err == nil {
			t.Errorf("expected option validation error for %v", opts)
		}
	}
}
