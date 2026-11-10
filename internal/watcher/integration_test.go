package watcher

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/filter"
	"feedwatch/internal/source"
	atomsource "feedwatch/internal/source/atom"
	"feedwatch/internal/store"
	jsonstore "feedwatch/internal/store/jsonfile"
)

// This test wires the REAL atom source, filter, JSON store and watcher over a
// local HTTP server, so the whole pipeline is checked without external network.
// Feed titles mirror the actual GitHub releases.atom output.
const goFeed = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
<title>Release notes from go</title>
<entry><id>tag:go/go1.27.3</id><title>[release-branch.go1.27] go1.27.3</title><updated>2026-10-20T01:26:06Z</updated><link rel="alternate" href="https://github.com/golang/go/releases/tag/go1.27.3"/></entry>
<entry><id>tag:go/go1.26.10</id><title>[release-branch.go1.26] go1.26.10</title><updated>2026-10-20T01:26:11Z</updated><link rel="alternate" href="https://github.com/golang/go/releases/tag/go1.26.10"/></entry>
<entry><id>tag:go/go1.27rc3</id><title>[release-branch.go1.27] go1.27rc3</title><updated>2026-08-13T17:38:53Z</updated><link rel="alternate" href="https://github.com/golang/go/releases/tag/go1.27rc3"/></entry>
</feed>`

const rustFeed = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
<title>Release notes from rust</title>
<entry><id>tag:rust/1.99.1</id><title>Rust 1.99.1</title><updated>2026-10-16T04:47:44Z</updated><link rel="alternate" href="https://github.com/rust-lang/rust/releases/tag/1.99.1"/></entry>
<entry><id>tag:rust/1.99.0</id><title>Rust 1.99.0</title><updated>2026-10-24T20:21:25Z</updated><link rel="alternate" href="https://github.com/rust-lang/rust/releases/tag/1.99.0"/></entry>
</feed>`

const pythonFeed = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
<title>Release notes from cpython</title>
<entry><id>tag:py/v3.15.2</id><title>v3.15.2</title><updated>2026-10-25T16:57:52Z</updated><link rel="alternate" href="https://github.com/python/cpython/releases/tag/v3.15.2"/></entry>
<entry><id>tag:py/v3.11.18</id><title>v3.11.18</title><updated>2026-10-25T19:41:29Z</updated><link rel="alternate" href="https://github.com/python/cpython/releases/tag/v3.11.18"/></entry>
<entry><id>tag:py/v3.16.0a1</id><title>v3.16.0a1</title><updated>2026-10-13T15:05:54Z</updated><link rel="alternate" href="https://github.com/python/cpython/releases/tag/v3.16.0a1"/></entry>
<entry><id>tag:py/eol</id><title>Python 3.10 end of life</title><updated>2026-10-12T14:17:51Z</updated><link rel="alternate" href="https://github.com/python/cpython/releases/tag/3.10"/></entry>
</feed>`

const pythonNewEntry = `<entry><id>tag:py/v3.15.3</id><title>v3.15.3</title><updated>2026-11-10T10:00:00Z</updated><link rel="alternate" href="https://github.com/python/cpython/releases/tag/v3.15.3"/></entry>`

func TestIntegrationRealComponentsBaselineRestartAndNewRelease(t *testing.T) {
	var newVersion atomic.Bool
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		switch r.URL.Path {
		case "/go":
			_, _ = io.WriteString(w, goFeed)
		case "/rust":
			_, _ = io.WriteString(w, rustFeed)
		case "/python":
			body := pythonFeed
			if newVersion.Load() {
				body = strings.Replace(body, "</feed>", pythonNewEntry+"\n</feed>", 1)
			}
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// Go 1.27: in-memory fake network, auto-closed via t.Cleanup. Client()
	// starts it and sets srv.URL, so call it before building any source.
	client := srv.Client()

	st, err := jsonstore.New(store.StoreSpec{
		Type: "jsonfile", Path: filepath.Join(t.TempDir(), "data", "state.json"),
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	n := &recordingNotifier{}

	build := func(name, path string) FeedTask {
		src, err := atomsource.New(source.SourceSpec{
			Name: name, Type: "atom", URL: srv.URL + path,
			Options: map[string]string{"retry_max": "0", "conditional": "false", "http_timeout": "5s"},
			Client:  client,
		}, quiet())
		if err != nil {
			t.Fatal(err)
		}
		flt, err := filter.New(filter.Rules{StableOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		return FeedTask{Name: name, Source: src, Filter: flt}
	}

	tasks := []FeedTask{
		build("golang/go", "/go"),
		build("rust-lang/rust", "/rust"),
		build("python/cpython", "/python"),
	}
	w := New(tasks, st, n, Options{Interval: time.Hour, Timeout: 30 * time.Second, RunOnStart: true}, quiet())
	w.now = func() time.Time { return fixedNow }

	ctx := t.Context()
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(n.notes) != 1 || n.notes[0].Kind != domain.KindBaseline {
		t.Fatalf("expected one baseline mail, got %+v", n.notes)
	}
	want := map[string]string{
		"golang/go":      "go1.27.3",
		"rust-lang/rust": "1.99.1",
		"python/cpython": "v3.15.2",
	}
	if len(n.notes[0].Items) != 3 {
		t.Fatalf("baseline should list 3 projects: %+v", n.notes[0].Items)
	}
	for _, it := range n.notes[0].Items {
		if want[it.Feed] != it.Version {
			t.Errorf("baseline %s = %q, want %q", it.Feed, it.Version, want[it.Feed])
		}
	}

	// Restart with no new releases: completely silent.
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if len(n.notes) != 1 {
		t.Errorf("restart must not send any mail, got %d", len(n.notes))
	}

	// A genuinely new stable version produces exactly one notification.
	newVersion.Store(true)
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("new release: %v", err)
	}
	if len(n.notes) != 2 || n.notes[1].Kind != domain.KindNewReleases {
		t.Fatalf("expected one new-release mail, got %+v", n.notes)
	}
	if len(n.notes[1].Items) != 1 || n.notes[1].Items[0].Version != "v3.15.3" {
		t.Errorf("wrong new version: %+v", n.notes[1].Items)
	}

	// And it is not announced a second time.
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("following cycle: %v", err)
	}
	if len(n.notes) != 2 {
		t.Errorf("same release must not be re-announced: %d mails", len(n.notes))
	}
}
