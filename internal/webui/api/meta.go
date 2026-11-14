//go:build webui

package api

import (
	"io/fs"
	"net/http"
	"strings"
	"time"
)

func (s *Server) meta(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	// The browser needs the server's zone to render "follow the server" and to
	// label it before anyone signs in. It is public information: a zone name and
	// an offset reveal nothing about the deployment's contents.
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.version,
		"build_id": s.buildID,
		"mode":     "webui",
		// May be "Local" when the host has no TZ and no named /etc/localtime;
		// server_utc_offset is then the only meaningful label.
		"server_timezone":   now.Location().String(),
		"server_utc_offset": now.Format("-07:00"),
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
