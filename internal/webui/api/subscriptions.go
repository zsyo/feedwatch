//go:build webui

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	githubsource "feedwatch/internal/source/github"
	"feedwatch/internal/webui"
	"feedwatch/internal/webui/store"
)

// baselineTimeout bounds the detached baseline fetch so a slow feed can never
// hold a request goroutine hostage. The subscription itself is already durable
// by the time this fires.
const baselineTimeout = 15 * time.Second

// validSourceType reports whether a subscription may use this source type.
// atom is the historical default; github tracks repositories without a feed.
func validSourceType(typ string) bool {
	return typ == "atom" || githubsource.IsValidSourceType(typ)
}

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
	sourceType := in.SourceType
	if strings.HasPrefix(strings.TrimSpace(in.URL), githubsource.StoredPrefix) {
		// A catalog entry may carry a stored "github:owner/repo" URL while its
		// source_type stayed at the DDL default. Trust the URL shape so a
		// one-click subscribe never feeds a non-URL into the atom key rules.
		sourceType = githubsource.SourceType
	}
	if sourceType == "" {
		sourceType = "atom"
	}
	if !validSourceType(sourceType) {
		return "", &writeError{http.StatusBadRequest, "invalid_source_type", "source_type must be atom or github"}
	}

	var normalized, key string
	var err error
	if githubsource.IsValidSourceType(sourceType) {
		normalized, key, err = webui.RepoURLKey(in.URL)
	} else {
		normalized, key, err = webui.URLKey(in.URL)
	}
	if err != nil {
		return "", &writeError{http.StatusBadRequest, "invalid_url", err.Error()}
	}
	// A GitHub subscription's release_scope lives in filter_json; reject bad
	// values here so the orchestrator never has to guess at poll time.
	if githubsource.IsValidSourceType(sourceType) && in.Filter != nil {
		if raw, ok := in.Filter["release_scope"]; ok {
			scope, ok := raw.(string)
			if !ok {
				return "", &writeError{http.StatusBadRequest, "invalid_release_scope", "release_scope must be a string"}
			}
			if _, err := githubsource.ParseScope(scope); err != nil {
				return "", &writeError{http.StatusBadRequest, "invalid_release_scope", err.Error()}
			}
		}
	}
	policy := in.NotifyPolicy
	if policy == "" {
		policy = "on_update"
	}
	if policy != "always" && policy != "on_update" {
		return "", &writeError{http.StatusBadRequest, "invalid_policy", "notify_policy must be always or on_update"}
	}
	filterJSON := []byte("{}")
	if in.Filter != nil {
		filterJSON, err = json.Marshal(in.Filter)
		if err != nil {
			return "", &writeError{http.StatusBadRequest, "invalid_filter", err.Error()}
		}
	}
	// A subscription with no channel to deliver to would silently swallow its
	// first snapshot: the orchestrator commits user_seen but skips rendering
	// when the user has no enabled channel. Require one up front.
	hasChannel, err := s.hasEnabledChannel(userID)
	if err != nil {
		return "", &writeError{http.StatusInternalServerError, "db_error", err.Error()}
	}
	if !hasChannel {
		return "", &writeError{http.StatusConflict, "no_enabled_channel",
			"请先在「通知方式」里添加并启用至少一个通知渠道，再添加订阅"}
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
	// Push the current-version snapshot after the subscription is durable.
	// Fire-and-forget: a slow or broken feed must never fail the subscribe.
	s.triggerBaseline(userID, normalized, key, sourceType, string(filterJSON), in.DisplayName)
	return key, nil
}

// triggerBaseline schedules an immediate baseline snapshot on a detached
// context. It is a no-op when no BaselineSource is configured (tests, or a
// build that opts out). Errors are logged, never surfaced to the subscriber.
func (s *Server) triggerBaseline(userID int64, url, urlKey, sourceType, filterJSON, displayName string) {
	if s.baseline == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), baselineTimeout)
		defer cancel()
		if err := s.baseline.PushBaseline(ctx, userID, url, urlKey, sourceType, filterJSON, displayName); err != nil {
			s.log.Warn("webui: baseline snapshot failed", "user", userID, "url_key", urlKey, "error", err)
		}
	}()
}

func (s *Server) updateSubscription(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var req struct {
		URL          *string         `json:"url"`
		SourceType   *string         `json:"source_type"`
		DisplayName  *string         `json:"display_name"`
		Filter       *map[string]any `json:"filter"`
		NotifyPolicy *string         `json:"notify_policy"`
		Enabled      *bool           `json:"enabled"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// Switching a subscription's source type in place would leave orphaned
	// seen_ids/user_seen rows and stale ETag validators under the old url_key.
	// The UI offers "delete and re-add" instead; reject it at the API too.
	if req.SourceType != nil {
		fail(w, http.StatusBadRequest, "source_type_immutable", "source_type cannot be changed; delete and re-add the subscription")
		return
	}
	sets := make([]string, 0, 6)
	args := make([]any, 0, 8)
	add := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}
	if req.URL != nil {
		// Decide the key namespace from the stored row's source type: an atom
		// URL and a GitHub "owner/repo" must not share identity handling.
		var storedType string
		if err := s.db.QueryRow(`SELECT source_type FROM subscriptions
			WHERE id=? AND user_id=? AND deleted_at IS NULL`, id, c.User.ID).Scan(&storedType); err != nil {
			fail(w, http.StatusNotFound, "not_found", "subscription not found")
			return
		}
		var normalized, key string
		var err error
		if githubsource.IsValidSourceType(storedType) {
			normalized, key, err = webui.RepoURLKey(*req.URL)
		} else {
			normalized, key, err = webui.URLKey(*req.URL)
		}
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
	// A 0->1 flip deserves a fresh baseline snapshot: the user is re-subscribing
	// to notifications after a pause and should see the current version again.
	// Read the old state before the UPDATE; the final row fields are reloaded
	// after commit so any URL/filter/display edits in this same PATCH are used.
	reEnable := false
	if req.Enabled != nil && *req.Enabled {
		var wasEnabled int
		err := s.db.QueryRow(`SELECT enabled FROM subscriptions
			WHERE id=? AND user_id=? AND deleted_at IS NULL`, id, c.User.ID).Scan(&wasEnabled)
		if err != nil {
			fail(w, http.StatusNotFound, "not_found", "subscription not found")
			return
		}
		reEnable = wasEnabled == 0
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
	if reEnable {
		var url, urlKey, sourceType, filterJSON, displayName string
		err := s.db.QueryRow(`SELECT url, url_key, source_type, filter_json, display_name
			FROM subscriptions WHERE id=? AND user_id=?`, id, c.User.ID).
			Scan(&url, &urlKey, &sourceType, &filterJSON, &displayName)
		if err != nil {
			s.log.Warn("webui: reload re-enabled subscription for baseline failed", "id", id, "error", err)
			return
		}
		s.triggerBaseline(c.User.ID, url, urlKey, sourceType, filterJSON, displayName)
	}
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
