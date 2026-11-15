//go:build webui

package api

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	"feedwatch/internal/webui/oauth"
)

func (s *Server) meta(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	// The browser needs the server's zone to render "follow the server" and to
	// label it before anyone signs in. It is public information: a zone name and
	// an offset reveal nothing about the deployment's contents.
	now := time.Now()
	// The login page must know what it can offer before anyone is signed in.
	// Only availability leaks here: client ids and secrets never do.
	state := s.providerRows()
	providers := make(map[string]bool, len(oauth.Keys()))
	for _, key := range oauth.Keys() {
		providers[key] = state[key].available()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.version,
		"build_id": s.buildID,
		"mode":     "webui",
		// May be "Local" when the host has no TZ and no named /etc/localtime;
		// server_utc_offset is then the only meaningful label.
		"server_timezone":   now.Location().String(),
		"server_utc_offset": now.Format("-07:00"),
		// Registration covers both the sign-up form and "a provider account we
		// have never seen creates one".
		"registration_enabled": s.registrationEnabled(),
		"oauth":                providers,
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
