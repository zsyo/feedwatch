//go:build webui

package presets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ManifestFileName = "manifest.json"

// Loader downloads the manifest and every category file it names. Client and
// Guard are injectable so httptest can drive the whole chain without touching
// the production SSRF rules or the network.
type Loader struct {
	Client       *http.Client // nil: build a guarded production client
	Guard        TargetGuard  // nil: production SSRF guard
	BaseURL      string
	UserAgent    string
	Timeout      time.Duration
	MaxBytes     int64
	MaxRedirects int
}

func NewLoader() *Loader {
	return &Loader{
		Guard:        newSSRFGuard(),
		UserAgent:    "feedwatch/1.0 (+https://github.com/zsyo/feedwatch)",
		Timeout:      15 * time.Second,
		MaxBytes:     MaxBytes,
		MaxRedirects: 3,
	}
}

func (l *Loader) guard() TargetGuard {
	if l.Guard != nil {
		return l.Guard
	}
	return newSSRFGuard()
}

func (l *Loader) redirectCheck() func(*http.Request, []*http.Request) error {
	max := l.MaxRedirects
	if max <= 0 {
		max = 3
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("presets: too many redirects")
		}
		return l.guard().Check(req.Context(), req.URL)
	}
}

func (l *Loader) client() *http.Client {
	if l.Client != nil {
		// Copy: setting CheckRedirect must never mutate a shared test client.
		c := *l.Client
		c.CheckRedirect = l.redirectCheck()
		return &c
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Keep the implicit HTTPS_PROXY / NO_PROXY support: an administrator may
	// need a proxy or a mirror to reach the presets host.
	tr.Proxy = http.ProxyFromEnvironment
	tr.DialContext = guardedDial(&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
	return &http.Client{
		Transport:     tr,
		Timeout:       l.Timeout,
		CheckRedirect: l.redirectCheck(),
	}
}

// Fetch downloads the manifest and all category files. It performs no
// database work. On any failure it still returns the states collected so far,
// so the API can report which file broke; the caller must not write anything.
func (l *Loader) Fetch(ctx context.Context, baseURL string) (*Snapshot, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("presets: invalid base URL: %w", err)
	}
	if err := l.guard().Check(ctx, u); err != nil {
		return nil, fmt.Errorf("presets: invalid base URL: %w", err)
	}

	c := l.client()
	snap := &Snapshot{
		Files:  make([]LoadedFile, 0),
		States: make([]FileState, 0),
	}

	var m Manifest
	if err := l.fetchJSON(ctx, c, base+"/"+ManifestFileName, ManifestFileName, &m); err != nil {
		snap.States = append(snap.States, FileState{Name: ManifestFileName, Status: "failed", Reason: err.Error()})
		return snap, err
	}
	if err := ValidateManifest(m); err != nil {
		snap.States = append(snap.States, FileState{Name: ManifestFileName, Status: "failed", Reason: err.Error()})
		return snap, err
	}
	snap.States = append(snap.States, FileState{Name: ManifestFileName, Status: "ok"})

	for _, f := range m.Files {
		var cf CategoryFile
		if err := l.fetchJSON(ctx, c, base+"/"+f.Name, f.Name, &cf); err != nil {
			snap.States = append(snap.States, FileState{Name: f.Name, Status: "failed", Reason: err.Error()})
			return snap, err
		}
		if err := ValidateCategoryFile(f.Name, f.Category, cf); err != nil {
			snap.States = append(snap.States, FileState{Name: f.Name, Status: "failed", Reason: err.Error()})
			return snap, err
		}
		snap.Files = append(snap.Files, LoadedFile{Name: f.Name, Category: cf.Category, Presets: cf.Presets})
		snap.States = append(snap.States, FileState{Name: f.Name, Status: "ok"})
	}
	return snap, nil
}

func (l *Loader) fetchJSON(ctx context.Context, c *http.Client, raw, label string, dst any) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid URL: %w", label, err)
	}
	if err := l.guard().Check(ctx, u); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("%s: build request: %w", label, err)
	}
	req.Header.Set("User-Agent", l.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", label, resp.StatusCode)
	}
	limit := l.MaxBytes
	if limit <= 0 {
		limit = MaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("%s: read body: %w", label, err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("%s: response is larger than %d bytes", label, limit)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%s: invalid JSON: %w", label, err)
	}
	return nil
}
