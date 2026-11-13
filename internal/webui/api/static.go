//go:build webui

package api

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// static serves the embedded SPA. Vite's index.html is preferred; a clean
// checkout only has stub.html, which tells the operator to build the frontend.
// Unknown /api/* paths must never fall through to an HTML response.
func (s *Server) static(w http.ResponseWriter, r *http.Request, _ Ctx) {
	if isAPI(r) {
		fail(w, http.StatusNotFound, "not_found", "unknown API endpoint")
		return
	}
	if s.assets == nil {
		http.Error(w, "webui assets are unavailable", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "." && !strings.HasPrefix(name, "..") {
		if data, err := fs.ReadFile(s.assets, name); err == nil {
			if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.Header().Set("Cache-Control", "public, max-age=300")
			_, _ = w.Write(data)
			return
		}
	}

	page := "index.html"
	data, err := fs.ReadFile(s.assets, page)
	if err != nil {
		page = "stub.html"
		data, err = fs.ReadFile(s.assets, page)
		if err != nil {
			http.Error(w, "webui assets are unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
