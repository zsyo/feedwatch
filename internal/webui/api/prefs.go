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

const (
	modeDigest  = "digest"
	modePerFeed = "per_feed"
)

// updatePreferences handles PATCH /api/me. These are account preferences, not
// server behaviour, so they change nothing about polling or delivery timing:
// timezone is display-only, and notify_mode only decides how one cycle's new
// releases are grouped into messages. Either field may be sent alone; sending
// both updates both. Credentials and role changes stay on their own
// admin-only endpoints.
func (s *Server) updatePreferences(w http.ResponseWriter, r *http.Request, c Ctx) {
	var req struct {
		Timezone   *string `json:"timezone"`
		NotifyMode *string `json:"notify_mode"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Timezone == nil && req.NotifyMode == nil {
		fail(w, http.StatusBadRequest, "bad_request", "timezone or notify_mode is required")
		return
	}

	sets := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if req.Timezone != nil {
		tz, err := normalizeTimezone(*req.Timezone)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_timezone", err.Error())
			return
		}
		sets = append(sets, "timezone=?")
		args = append(args, tz)
	}
	if req.NotifyMode != nil {
		mode, err := normalizeNotifyMode(*req.NotifyMode)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_notify_mode", err.Error())
			return
		}
		sets = append(sets, "notify_mode=?")
		args = append(args, mode)
	}
	args = append(args, store.Now(), c.User.ID)
	if _, err := s.db.Exec(`UPDATE users SET `+strings.Join(sets, ", ")+`, updated_at=? WHERE id=?`, args...); err != nil {
		fail(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}

	// Read back instead of echoing the request: normalizeTimezone may fold a
	// value ("Local" becomes ""), and the SPA needs the stored form.
	var tz, mode string
	if err := s.db.QueryRow(`SELECT timezone, notify_mode FROM users WHERE id=?`, c.User.ID).
		Scan(&tz, &mode); err != nil {
		fail(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timezone": tz, "notify_mode": mode})
}

// normalizeNotifyMode accepts the two shapes the UI offers plus an empty string,
// which falls back to the column default rather than failing a form submit.
func normalizeNotifyMode(raw string) (string, error) {
	switch mode := strings.TrimSpace(raw); mode {
	case "":
		return modeDigest, nil
	case modeDigest, modePerFeed:
		return mode, nil
	default:
		return "", fmt.Errorf("notify_mode must be %s or %s", modeDigest, modePerFeed)
	}
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
