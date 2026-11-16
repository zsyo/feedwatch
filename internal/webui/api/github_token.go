//go:build webui

package api

import (
	"net/http"
	"strconv"
	"strings"

	"feedwatch/internal/webui/secrets"
	"feedwatch/internal/webui/store"
)

// The GitHub token is a single, installation-wide credential. It is sealed
// with the same envelope used for channel secrets, stored in the settings
// table under a reserved key, and never echoed back: the UI only learns
// whether a token is set. The optional GraphQL batch channel requires it.

func (s *Server) adminGetGithubToken(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	set := false
	var sealed string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`,
		secrets.GithubTokenSettingKey).Scan(&sealed); err == nil {
		plain, err := s.sealer.Open(sealed, secrets.GithubTokenAAD())
		set = err == nil && strings.TrimSpace(string(plain)) != ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"set": set})
}

func (s *Server) adminPutGithubToken(w http.ResponseWriter, r *http.Request, _ Ctx) {
	var req struct {
		Token   string `json:"token"`
		GraphQL *bool  `json:"graphql"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	now := store.Now()
	if token == "" {
		// An empty submission clears the credential; the polling loop falls
		// back to anonymous REST (and GraphQL, if enabled, is ignored).
		if _, err := s.db.Exec(`DELETE FROM settings WHERE setting_key=?`, secrets.GithubTokenSettingKey); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	} else {
		sealed, err := s.sealer.Seal([]byte(token), secrets.GithubTokenAAD())
		if err != nil {
			fail(w, http.StatusInternalServerError, "seal_failed", err.Error())
			return
		}
		if _, err := s.db.Upsert(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
			"setting_key", []string{"value", "updated_at"}, secrets.GithubTokenSettingKey, sealed, now); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	// The GraphQL toggle is an ordinary setting; it is read at startup like
	// every other poll knob, so the response tells the operator to restart.
	if req.GraphQL != nil {
		if _, err := s.db.Upsert(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
			"setting_key", []string{"value", "updated_at"},
			"github.graphql", strconv.FormatBool(*req.GraphQL), now); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "set": token != ""})
}
