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
	Enabled      bool            `json:"enabled"`
	SeededAt     *string         `json:"seeded_at"`
}

// The notification shape (digest / per_feed) is deliberately absent: it is an
// account preference stored on users, so it cannot differ between the rows here.
const subscriptionColumns = `id, url, url_key, display_name, source_type, filter_json,
	notify_policy, enabled, seeded_at`

type scanner interface{ Scan(dest ...any) error }

func scanSubscription(row scanner) (subscription, error) {
	var s subscription
	var enabled int
	var filter string
	var seeded sql.NullString
	err := row.Scan(&s.ID, &s.URL, &s.URLKey, &s.DisplayName, &s.SourceType, &filter,
		&s.NotifyPolicy, &enabled, &seeded)
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
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	key, e := s.subscribeURL(c.User.ID, subscribeInput{
		URL:          req.URL,
		DisplayName:  req.DisplayName,
		SourceType:   req.SourceType,
		Filter:       req.Filter,
		NotifyPolicy: req.NotifyPolicy,
	})
	if e != nil {
		fail(w, e.status, e.code, e.message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "url_key": key})
}

// subscribeInput is what both the manual "add subscription" form and the
// catalog's one-click subscribe pass to subscribeURL.
type subscribeInput struct {
	URL          string
	DisplayName  string
	SourceType   string
	Filter       map[string]any
	NotifyPolicy string
}

// writeError turns a subscribeURL failure into an HTTP-ready error so callers
// cannot disagree about status codes or error codes.
type writeError struct {
	status  int
	code    string
	message string
}

// subscribeURL is the ONLY place allowed to INSERT INTO subscriptions.
//
// Creating a subscription is a three-write transaction: the subscription row,
// the global polling pool, and a copy of the current seen_ids baseline for this
// user. Skipping the last one makes the orchestrator treat every historical
// release as new and replay it on the next cycle. Every subscription entry
// point (manual form, catalog quick subscribe, future imports) must go through
// here.
func (s *Server) subscribeURL(userID int64, in subscribeInput) (string, *writeError) {
	normalized, key, err := webui.URLKey(in.URL)
	if err != nil {
		return "", &writeError{http.StatusBadRequest, "invalid_url", err.Error()}
	}
	policy := in.NotifyPolicy
	if policy == "" {
		policy = "on_update"
	}
	if policy != "always" && policy != "on_update" {
		return "", &writeError{http.StatusBadRequest, "invalid_policy", "notify_policy must be always or on_update"}
	}
	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = "atom"
	}
	filterJSON := []byte("{}")
	if in.Filter != nil {
		filterJSON, err = json.Marshal(in.Filter)
		if err != nil {
			return "", &writeError{http.StatusBadRequest, "invalid_filter", err.Error()}
		}
	}
	now := store.Now()

	tx, err := s.db.Begin()
	if err != nil {
		return "", &writeError{http.StatusInternalServerError, "db_error", err.Error()}
	}
	defer tx.Rollback()
	res, err := tx.ExecIgnore(`INSERT INTO subscriptions
		(user_id, url, url_key, display_name, source_type, filter_json, notify_policy, enabled, seeded_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		userID, normalized, key, in.DisplayName, sourceType, string(filterJSON), policy, 1, now)
	if err != nil {
		return "", &writeError{http.StatusInternalServerError, "db_error", err.Error()}
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return "", &writeError{http.StatusConflict, "duplicate_subscription", "this URL is already subscribed"}
	}
	if _, err := tx.ExecIgnore(`INSERT INTO feed_pool (url_key, url, source_type) VALUES (?,?,?)`,
		key, normalized, sourceType); err != nil {
		return "", &writeError{http.StatusInternalServerError, "db_error", err.Error()}
	}
	// New subscriptions start from the current global baseline: never replay
	// releases published before the user joined.
	if _, err := tx.ExecIgnore(`INSERT INTO user_seen (user_id, url_key, release_id, seen_at)
		SELECT ?, url_key, release_id, ? FROM seen_ids WHERE url_key=?`, userID, now, key); err != nil {
		return "", &writeError{http.StatusInternalServerError, "db_error", err.Error()}
	}
	if err := tx.Commit(); err != nil {
		return "", &writeError{http.StatusInternalServerError, "db_error", err.Error()}
	}
	return key, nil
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
