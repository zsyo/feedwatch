//go:build webui

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"feedwatch/internal/webui/store"
)

// maxTimezoneLen bounds what we are willing to look up: every real IANA name is
// well under this, and it keeps a hostile body out of LoadLocation's parser.
const maxTimezoneLen = 64

// updatePreferences handles PATCH /api/me. Display preferences only: they are
// account state, not server behaviour, so they change nothing about polling,
// delivery or stored timestamps. Credentials and role changes stay on their own
// admin-only endpoints.
func (s *Server) updatePreferences(w http.ResponseWriter, r *http.Request, c Ctx) {
	var req struct {
		Timezone *string `json:"timezone"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Timezone == nil {
		fail(w, http.StatusBadRequest, "bad_request", "timezone is required")
		return
	}
	tz, err := normalizeTimezone(*req.Timezone)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_timezone", err.Error())
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET timezone=?, updated_at=? WHERE id=?`,
		tz, store.Now(), c.User.ID); err != nil {
		fail(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timezone": tz})
}

// normalizeTimezone maps "" to "follow the server" and otherwise validates the
// name against the zone database. Whitespace is trimmed so a padded value from
// the select never becomes "unset"; the name itself must match exactly, because
// the picker always sends the canonical IANA spelling.
func normalizeTimezone(raw string) (string, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return "", nil
	}
	if len(tz) > maxTimezoneLen {
		return "", fmt.Errorf("time zone name is longer than %d characters", maxTimezoneLen)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", errors.New("unknown IANA time zone")
	}
	// "Local" is the server's own zone, which is exactly what "" already means.
	// Folding them keeps one representation and makes the picker unambiguous.
	if loc == time.Local {
		return "", nil
	}
	return loc.String(), nil
}
