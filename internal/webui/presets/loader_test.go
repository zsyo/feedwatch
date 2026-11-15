//go:build webui

package presets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The production SSRF guard resolves DNS and would reject an httptest server on
// 127.0.0.1. Loader tests inject a permissive guard so they exercise fetching,
// parsing and per-file reporting in isolation.
type allowAllGuard struct{}

func (allowAllGuard) Check(context.Context, *url.URL) error { return nil }

type countingGuard struct {
	allowed int
	calls   int
}

func (g *countingGuard) Check(_ context.Context, _ *url.URL) error {
	g.calls++
	if g.calls > g.allowed {
		return fmt.Errorf("blocked hop %d", g.calls)
	}
	return nil
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func twoFileManifest() string {
	return `{"format":"` + ManifestFormat + `","format_version":1,"files":[
		{"name":"a.json","category":"语言与运行时"},
		{"name":"b.json","category":"编辑器"}]}`
}

func categoryA() string {
	return `{"format":"` + CategoryFormat + `","format_version":1,"category":"语言与运行时",
		"presets":[{"preset_key":"k1","version":1,"name":"K1","url":"https://example.com/a.atom",
		"description":"简介","homepage":"https://example.com/k1"}]}`
}

func categoryB() string {
	return `{"format":"` + CategoryFormat + `","format_version":1,"category":"编辑器",
		"presets":[{"preset_key":"k2","version":1,"name":"K2","url":"https://example.com/b.atom",
		"description":"简介","homepage":"https://example.com/k2"}]}`
}

// The handler runs in the server's goroutine, so header recording is mutex
// protected: these tests also run under -race.
type presetHTTPServer struct {
	*httptest.Server
	mu      sync.Mutex
	headers []string
}

func (p *presetHTTPServer) headerSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.headers...)
}

func newPresetHTTPServer(t *testing.T, files map[string]string) *presetHTTPServer {
	t.Helper()
	// Copy so callers may build a second server from a modified map without a
	// concurrent map read/write.
	local := make(map[string]string, len(files))
	for k, v := range files {
		local[k] = v
	}
	p := &presetHTTPServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.headers = append(p.headers, r.Method+" "+r.URL.Path+" | UA="+r.Header.Get("User-Agent")+
			" | Accept="+r.Header.Get("Accept"))
		p.mu.Unlock()
		body, ok := local[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(p.Close)
	return p
}

func testLoader(srv *httptest.Server, guard TargetGuard) *Loader {
	return &Loader{
		Client: srv.Client(), Guard: guard, BaseURL: srv.URL,
		UserAgent: "feedwatch-test/1.0", Timeout: 3 * time.Second,
		MaxBytes: MaxBytes, MaxRedirects: 3,
	}
}

func TestLoaderFetchesEveryFileAndReportsStates(t *testing.T) {
	srv := newPresetHTTPServer(t, map[string]string{
		ManifestFileName: twoFileManifest(),
		"a.json":         categoryA(),
		"b.json":         categoryB(),
	})
	snap, err := testLoader(srv.Server, allowAllGuard{}).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Files) != 2 || len(snap.States) != 3 {
		t.Fatalf("files=%d states=%d, want 2/3: %+v", len(snap.Files), len(snap.States), snap.States)
	}
	for _, s := range snap.States {
		if s.Status != "ok" || s.Reason != "" {
			t.Fatalf("state not ok: %+v", s)
		}
	}
	if snap.Files[0].Category != "语言与运行时" || snap.Files[1].Presets[0].PresetKey != "k2" {
		t.Fatalf("category files were mixed up: %+v", snap.Files)
	}
	items, err := Prepare(snap)
	if err != nil || len(items) != 2 {
		t.Fatalf("Prepare = %d items, %v", len(items), err)
	}
	headers := srv.headerSnapshot()
	if len(headers) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(headers))
	}
	for _, h := range headers {
		if !strings.Contains(h, "UA=feedwatch-test/1.0") ||
			!strings.Contains(h, "Accept=application/json") || !strings.Contains(h, "GET ") {
			t.Fatalf("request headers are wrong: %q", h)
		}
	}
}

func TestLoaderStopsAtFirstBadFileAndNamesIt(t *testing.T) {
	srv := newPresetHTTPServer(t, map[string]string{
		ManifestFileName: twoFileManifest(),
		"a.json":         categoryA(),
		"b.json":         "{not json",
	})
	snap, err := testLoader(srv.Server, allowAllGuard{}).Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "b.json") {
		t.Fatalf("want a b.json error, got %v", err)
	}
	if len(snap.States) != 3 {
		t.Fatalf("states = %+v", snap.States)
	}
	if snap.States[0].Status != "ok" || snap.States[1].Status != "ok" || snap.States[2].Status != "failed" {
		t.Fatalf("states do not isolate the failed file: %+v", snap.States)
	}
	if !strings.Contains(snap.States[2].Reason, "invalid JSON") {
		t.Fatalf("failure reason lost: %+v", snap.States[2])
	}
	// The caller MUST NOT apply these partial files; Fetch only returns them
	// for diagnostics.
	if len(snap.Files) != 1 {
		t.Fatalf("partial files leaked into snapshot: %+v", snap.Files)
	}

	srv2 := newPresetHTTPServer(t, map[string]string{
		ManifestFileName: twoFileManifest(),
		"a.json":         categoryA(),
		"b.json":         "",
	})
	_, err = testLoader(srv2.Server, allowAllGuard{}).Fetch(context.Background(), srv2.URL)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("empty document was accepted: %v", err)
	}
}

func TestLoaderRejectsNon200AndOversizeResponses(t *testing.T) {
	srv := newPresetHTTPServer(t, map[string]string{ManifestFileName: twoFileManifest()})
	snap, err := testLoader(srv.Server, allowAllGuard{}).Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "a.json: HTTP 404") {
		t.Fatalf("missing category file was not reported: %v", err)
	}
	if snap == nil || len(snap.States) != 2 || snap.States[1].Status != "failed" {
		t.Fatalf("states = %+v", snap.States)
	}

	big := `{"format":"` + ManifestFormat + `","format_version":1,"files":[{"name":"a.json","category":"` +
		strings.Repeat("x", 4096) + `"}]}`
	srv2 := newPresetHTTPServer(t, map[string]string{ManifestFileName: big})
	l := testLoader(srv2.Server, allowAllGuard{})
	l.MaxBytes = 128
	_, err = l.Fetch(context.Background(), srv2.URL)
	if err == nil || !strings.Contains(err.Error(), "larger than 128 bytes") {
		t.Fatalf("size limit not enforced: %v", err)
	}
}

func TestLoaderRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, want string
	}{
		{"bad manifest format", `{"format":"nope","format_version":1,"files":[{"name":"a.json","category":"x"}]}`, "unsupported manifest format"},
		{"empty file list", `{"format":"` + ManifestFormat + `","format_version":1,"files":[]}`, "lists no category files"},
		{"path traversal in file list", `{"format":"` + ManifestFormat + `","format_version":1,"files":[{"name":"../a.json","category":"x"}]}`, "invalid category file name"},
		{"malformed manifest", `{`, "invalid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newPresetHTTPServer(t, map[string]string{ManifestFileName: tc.manifest})
			snap, err := testLoader(srv.Server, allowAllGuard{}).Fetch(context.Background(), srv.URL)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if snap == nil || len(snap.States) != 1 || snap.States[0].Name != ManifestFileName ||
				snap.States[0].Status != "failed" {
				t.Fatalf("manifest failure state missing: %+v", snap)
			}
		})
	}

	// A valid manifest can still carry a category file whose own category
	// drifted away from the manifest row.
	drifted := mustJSON(t, sampleCategory("别的分类", samplePreset("k1", "https://example.com/a.atom")))
	srv := newPresetHTTPServer(t, map[string]string{
		ManifestFileName: twoFileManifest(),
		"a.json":         drifted,
	})
	_, err := testLoader(srv.Server, allowAllGuard{}).Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "does not match manifest") {
		t.Fatalf("category drift was accepted: %v", err)
	}
}

func TestLoaderChecksEveryRedirectHop(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/evil.json", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	// base URL check + initial manifest check succeed; the redirect hop is the
	// third Check and must be refused before the request is followed.
	guard := &countingGuard{allowed: 2}
	_, err := testLoader(srv, guard).Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "blocked hop 3") {
		t.Fatalf("redirect target was not re-checked: %v", err)
	}
}

func TestLoaderCapsRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/"+ManifestFileName, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	l := testLoader(srv, allowAllGuard{})
	l.MaxRedirects = 2
	_, err := l.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("redirect loop was not capped: %v", err)
	}
}

func TestLoaderHonoursContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second): // never let srv.Close hang the test
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := testLoader(srv, allowAllGuard{}).Fetch(ctx, srv.URL)
	if err == nil {
		t.Fatal("cancelled context still produced a snapshot")
	}
}

func TestLoaderRejectsBaseURLBeforeAnyRequest(t *testing.T) {
	srv := newPresetHTTPServer(t, map[string]string{ManifestFileName: twoFileManifest()})
	l := testLoader(srv.Server, &countingGuard{}) // allows zero hops
	_, err := l.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "invalid base URL") {
		t.Fatalf("guard verdict on base URL was ignored: %v", err)
	}
	if got := srv.headerSnapshot(); len(got) != 0 {
		t.Fatalf("a request was sent after the guard refused: %v", got)
	}
}
