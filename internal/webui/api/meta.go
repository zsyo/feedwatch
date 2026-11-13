//go:build webui

package api

import (
	"io/fs"
	"net/http"
	"strings"
)

func (s *Server) meta(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.version,
		"build_id": s.buildID,
		"mode":     "webui",
	})
}

func readBuildID(assets fs.FS) string {
	if assets == nil {
		return "dev"
	}
	b, err := fs.ReadFile(assets, "BUILD_ID")
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(b))
}
