//go:build webui

package api

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Explicit MIME types keep the SPA working on hosts whose system MIME database
// maps .js/.css to text/plain (common on minimal Windows images).
var staticContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := staticContentTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// cleanAssetName turns a URL path into a safe fs.FS key. A path that still
// contains ".." after cleaning, or resolves to a directory, is rejected and
// falls through to the SPA index instead of being served.
func cleanAssetName(raw string) string {
	cleaned := path.Clean("/" + raw)
	if strings.Contains(cleaned, "..") {
		return ""
	}
	name := strings.TrimPrefix(cleaned, "/")
	if name == "" || name == "." || strings.HasSuffix(name, "/") {
		return ""
	}
	return name
}

// static serves the embedded SPA. Vite's index.html is preferred; a clean
// checkout only has stub.html, which tells the operator to build the frontend.
// Unknown /api/* paths are always JSON 404s, never HTML.
func (s *Server) static(w http.ResponseWriter, r *http.Request, _ Ctx) {
	if isAPI(r) {
		s.apiNotFound(w, r, Ctx{})
		return
	}
	if s.assets == nil {
		http.Error(w, "webui assets are unavailable", http.StatusServiceUnavailable)
		return
	}
	if name := cleanAssetName(r.URL.Path); name != "" {
		if data, err := fs.ReadFile(s.assets, name); err == nil {
			w.Header().Set("Content-Type", contentType(name))
			// Vite fingerprints files under assets/, so they can be cached forever.
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=300")
			}
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write(data)
			return
		}
	}

	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		data, err = fs.ReadFile(s.assets, "stub.html")
		if err != nil {
			http.Error(w, "webui assets are unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	// The entry point must not be cached: a stale index would reference assets
	// that no longer exist after an upgrade.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(data)
}

// apiNotFound is the method-agnostic catch-all for /api/*. Without it a POST to
// an unknown endpoint would be a bare 405 from ServeMux instead of the same
// JSON error shape the rest of the API uses.
func (s *Server) apiNotFound(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	fail(w, http.StatusNotFound, "not_found", "unknown API endpoint")
}
