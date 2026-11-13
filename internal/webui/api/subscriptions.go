//go:build webui

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"feedwatch/internal/webui"
	"feedwatch/internal/webui/store"
)

type subscription struct {
	ID           int64           `json:"id"`
	URL          string          `json:"url"`
	URLKey       string          `json:"url_key"`
	DisplayName  string          `json:"display_name"`
	SourceType   string          `json:"source_type"`
	Filter       json.RawMessage `json:"filter"`
	NotifyPolicy string          `json:"notify_policy"`
	Mode         string          `json:"mode"`
	Enabled      bool            `json:"enabled"`
	SeededAt     *string         `json:"seeded_at"`
}

const subscriptionColumns = `id, url, url_key, display_name, source_type, filter_json,
	notify_policy, mode, enabled, seeded_at`

type scanner interface{ Scan(dest ...any) error }

func scanSubscription(row scanner) (subscription, error) {
	var s subscription
	var enabled int
	var filter string
	var seeded sql.NullString
	err := row.Scan(&s.ID, &s.URL, &s.URLKey, &s.DisplayName, &s.SourceType, &filter,
		&s.NotifyPolicy, &s.Mode, &enabled, &seeded)
	if err != nil {
		return subscription{}, err
	}
	s.Enabled = enabled != 0
	s.Filter = json.RawMessage(filter)
	if seeded.Valid {
		s.SeededAt = &seeded.String
	}
	return s, nil
}

func (s *Server) listSubscriptions(w http.ResponseWriter, _ *http.Request, c Ctx) {
	rows, err := s.db.Query(`SELECT `+subscriptionColumns+`
		FROM subscriptions WHERE user_id=? AND deleted_at IS NULL ORDER BY id`, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]subscription, 0)
	for rows.Next() {
		item, err := scanSubscription(rows)
		if err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request, c Ctx) {
	var req struct {
		URL          string         `json:"url"`
		DisplayName  string         `json:"display_name"`
		SourceType   string         `json:"source_type"`
		Filter       map[string]any `json:"filter"`
		NotifyPolicy string         `json:"notify_policy"`
		Mode         string         `json:"mode"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	normalized, key, err := webui.URLKey(req.URL)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}
	policy, mode := req.NotifyPolicy, req.Mode
	if policy == "" {
		policy = "on_update"
	}
	if policy != "always" && policy != "on_update" {
		fail(w, http.StatusBadRequest, "invalid_policy", "notify_policy must be always or on_update")
		return
	}
	if mode == "" {
		mode = "digest"
	}
	if mode != "digest" && mode != "per_feed" {
		fail(w, http.StatusBadRequest, "invalid_mode", "mode must be digest or per_feed")
		return
	}
	sourceType := req.SourceType
	if sourceType == "" {
		sourceType = "atom"
	}
	filterJSON := []byte("{}")
	if req.Filter != nil {
		filterJSON, err = json.Marshal(req.Filter)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_filter", err.Error())
			return
		}
	}
	now := store.Now()

	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO subscriptions
		(user_id, url, url_key, display_name, source_type, filter_json, notify_policy, mode, enabled, seeded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.User.ID, normalized, key, req.DisplayName, sourceType, string(filterJSON), policy, mode, 1, now)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		fail(w, http.StatusConflict, "duplicate_subscription", "this URL is already subscribed")
		return
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO feed_pool (url_key, url, source_type) VALUES (?,?,?)`,
		key, normalized, sourceType); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// New subscriptions start from the current global baseline: never replay
	// releases published before the user joined.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO user_seen (user_id, url_key, release_id, seen_at)
		SELECT ?, url_key, release_id, ? FROM seen_ids WHERE url_key=?`, c.User.ID, now, key); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "url_key": key})
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var req struct {
		URL          *string         `json:"url"`
		DisplayName  *string         `json:"display_name"`
		Filter       *map[string]any `json:"filter"`
		NotifyPolicy *string         `json:"notify_policy"`
		Mode         *string         `json:"mode"`
		Enabled      *bool           `json:"enabled"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	sets := make([]string, 0, 6)
	args := make([]any, 0, 8)
	add := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}
	if req.URL != nil {
		normalized, key, err := webui.URLKey(*req.URL)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_url", err.Error())
			return
		}
		add("url", normalized)
		add("url_key", key)
	}
	if req.DisplayName != nil {
		add("display_name", *req.DisplayName)
	}
	if req.Filter != nil {
		b, err := json.Marshal(*req.Filter)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_filter", err.Error())
			return
		}
		add("filter_json", string(b))
	}
	if req.NotifyPolicy != nil {
		if *req.NotifyPolicy != "always" && *req.NotifyPolicy != "on_update" {
			fail(w, http.StatusBadRequest, "invalid_policy", "notify_policy must be always or on_update")
			return
		}
		add("notify_policy", *req.NotifyPolicy)
	}
	if req.Mode != nil {
		if *req.Mode != "digest" && *req.Mode != "per_feed" {
			fail(w, http.StatusBadRequest, "invalid_mode", "mode must be digest or per_feed")
			return
		}
		add("mode", *req.Mode)
	}
	if req.Enabled != nil {
		add("enabled", boolToInt(*req.Enabled))
	}
	if len(sets) == 0 {
		fail(w, http.StatusBadRequest, "empty_patch", "no fields to update")
		return
	}
	args = append(args, id, c.User.ID)
	res, err := s.db.Exec(`UPDATE subscriptions SET `+strings.Join(sets, ", ")+`
		WHERE id=? AND user_id=? AND deleted_at IS NULL`, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "subscription")
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := s.db.Exec(`UPDATE subscriptions SET deleted_at=?
		WHERE id=? AND user_id=? AND deleted_at IS NULL`, store.Now(), id, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "subscription")
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func requireAffected(w http.ResponseWriter, res sql.Result, what string) {
	n, err := res.RowsAffected()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if n == 0 {
		fail(w, http.StatusNotFound, "not_found", fmt.Sprintf("%s not found", what))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
