//go:build webui

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// The administrator is an operations role: it can read a user's resources and
// switch them off, but never edit them and never see their credentials. These
// endpoints implement exactly that and nothing more — there is deliberately no
// admin route that can create, update or delete somebody else's subscription or
// channel.

type adminSubscription struct {
	ID           int64  `json:"id"`
	URL          string `json:"url"`
	DisplayName  string `json:"display_name"`
	SourceType   string `json:"source_type"`
	NotifyPolicy string `json:"notify_policy"`
	Enabled      bool   `json:"enabled"`
}

type adminDelivery struct {
	ID          int64  `json:"id"`
	CycleID     string `json:"cycle_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	SentAt      string `json:"sent_at"`
	ChannelType string `json:"channel_type"`
	ChannelName string `json:"channel_name"`
	LastError   string `json:"last_error"`
	CreatedAt   string `json:"created_at"`
}

func (s *Server) loadManagedUser(id int64) (managedUser, error) {
	var u managedUser
	var active, must int
	err := s.db.QueryRow(`SELECT id, username, role, is_active, must_change_password, notify_mode, created_at
		FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &active, &must, &u.NotifyMode, &u.CreatedAt)
	if err != nil {
		return managedUser{}, err
	}
	u.IsActive, u.MustChangePassword = active != 0, must != 0
	return u, nil
}

func (s *Server) requireManagedUser(w http.ResponseWriter, id int64) (managedUser, bool) {
	u, err := s.loadManagedUser(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "not_found", "user not found")
			return managedUser{}, false
		}
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return managedUser{}, false
	}
	return u, true
}

func (s *Server) adminUserDetail(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, ok := s.requireManagedUser(w, id)
	if !ok {
		return
	}
	counts := make(map[string]int, 5)
	for key, query := range map[string]string{
		"subscriptions": `SELECT COUNT(*) FROM subscriptions WHERE user_id=? AND deleted_at IS NULL`,
		"channels":      `SELECT COUNT(*) FROM channels WHERE user_id=?`,
		"deliveries":    `SELECT COUNT(*) FROM deliveries WHERE user_id=?`,
		"pending":       `SELECT COUNT(*) FROM deliveries WHERE user_id=? AND status IN ('pending','retry')`,
		"abandoned":     `SELECT COUNT(*) FROM deliveries WHERE user_id=? AND status='abandoned'`,
	} {
		var n int
		if err := s.db.QueryRow(query, id).Scan(&n); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		counts[key] = n
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "counts": counts})
}

func (s *Server) adminUserSubscriptions(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, ok := s.requireManagedUser(w, id); !ok {
		return
	}
	rows, err := s.db.Query(`SELECT id, url, display_name, source_type, notify_policy, enabled
		FROM subscriptions WHERE user_id=? AND deleted_at IS NULL ORDER BY id`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]adminSubscription, 0)
	for rows.Next() {
		var item adminSubscription
		var enabled int
		if err := rows.Scan(&item.ID, &item.URL, &item.DisplayName, &item.SourceType,
			&item.NotifyPolicy, &enabled); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		item.Enabled = enabled != 0
		item.URL = maskFeedURL(item.URL)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

func (s *Server) adminUserChannels(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, ok := s.requireManagedUser(w, id); !ok {
		return
	}
	items, err := s.channelsFor(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": items})
}

func (s *Server) adminUserDeliveries(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, ok := s.requireManagedUser(w, id); !ok {
		return
	}
	limit := queryInt(r, "limit", 50, 1, 200)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)
	rows, err := s.db.Query(`SELECT d.id, d.cycle_id, d.kind, d.status, d.attempts,
			COALESCE(d.sent_at, ''), COALESCE(g.channel_type, ''), COALESCE(c.name, ''),
			COALESCE(g.last_error, ''), COALESCE(g.created_at, '')
		FROM deliveries d
		LEFT JOIN delivery_groups g ON g.group_id = d.group_id
		LEFT JOIN channels c ON c.id = d.channel_id
		WHERE d.user_id=? ORDER BY d.id DESC LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]adminDelivery, 0)
	for rows.Next() {
		var item adminDelivery
		if err := rows.Scan(&item.ID, &item.CycleID, &item.Kind, &item.Status, &item.Attempts,
			&item.SentAt, &item.ChannelType, &item.ChannelName, &item.LastError, &item.CreatedAt); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		item.LastError = maskErrorText(item.LastError)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out, "limit": limit, "offset": offset})
}

func (s *Server) adminSetSubscriptionEnabled(w http.ResponseWriter, r *http.Request, _ Ctx) {
	userID, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	subID, err := parseID(r, "subID")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, ok := s.requireManagedUser(w, userID); !ok {
		return
	}
	enabled, ok := decodeEnabledOnly(w, r)
	if !ok {
		return
	}
	res, err := s.db.Exec(`UPDATE subscriptions SET enabled=? WHERE id=? AND user_id=? AND deleted_at IS NULL`,
		boolInt(enabled), subID, userID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "subscription")
}

func (s *Server) adminSetChannelEnabled(w http.ResponseWriter, r *http.Request, _ Ctx) {
	userID, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	channelID, err := parseID(r, "chID")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, ok := s.requireManagedUser(w, userID); !ok {
		return
	}
	enabled, ok := decodeEnabledOnly(w, r)
	if !ok {
		return
	}
	if !enabled {
		// Only guard a real disable of a currently-enabled channel; a missing
		// channel is left to the UPDATE below to report as 404.
		current, err := s.loadChannel(userID, channelID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fail(w, http.StatusNotFound, "not_found", "channel not found")
				return
			}
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if current.Enabled && !s.guardEnabledChannelRemoval(w, userID, channelID) {
			return
		}
	}
	res, err := s.db.Exec(`UPDATE channels SET enabled=? WHERE id=? AND user_id=?`,
		boolInt(enabled), channelID, userID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "channel")
}

// decodeEnabledOnly accepts exactly {"enabled": bool}. Any other key is a 400:
// silently ignoring an attempted edit would look like it worked.
func decodeEnabledOnly(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var body map[string]json.RawMessage
	if err := decodeJSON(w, r, &body); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return false, false
	}
	if len(body) != 1 {
		fail(w, http.StatusBadRequest, "admin_readonly",
			"administrators may only enable or disable this resource")
		return false, false
	}
	raw, ok := body["enabled"]
	if !ok {
		fail(w, http.StatusBadRequest, "admin_readonly",
			"administrators may only enable or disable this resource")
		return false, false
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "enabled must be a boolean")
		return false, false
	}
	return enabled, true
}

var (
	errorURLPattern    = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
	errorBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[^\s"',;]+`)
	errorSecretPattern = regexp.MustCompile(`(?i)\b(token|secret|key|password|passwd|signature|sign)=[^\s&"',;]+`)
)

// maskErrorText strips credentials out of vendor error strings before an
// administrator sees them. Some adapters quote the full webhook URL, and those
// URLs usually carry the token in the path.
func maskErrorText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	text = errorURLPattern.ReplaceAllString(text, "[url]")
	text = errorBearerPattern.ReplaceAllString(text, "Bearer [redacted]")
	text = errorSecretPattern.ReplaceAllString(text, "$1=[redacted]")
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return text
}
