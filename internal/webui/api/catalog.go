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

const uncategorizedLabel = "未分类"

type catalogCategory struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	EntryCount int    `json:"entry_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type catalogEntry struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	URLKey          string `json:"url_key"`
	CategoryID      *int64 `json:"category_id"`
	CategoryName    string `json:"category_name"`
	Description     string `json:"description"`
	Homepage        string `json:"homepage"`
	SourceType      string `json:"source_type"`
	Enabled         bool   `json:"enabled"`
	SubscriberCount int    `json:"subscriber_count"`
	Subscribed      bool   `json:"subscribed"`
	SubscriptionID  *int64 `json:"subscription_id"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	PresetKey       string `json:"preset_key"`
	PresetVersion   int    `json:"preset_version"`
	PresetEdited    bool   `json:"preset_edited"`
}

// catalogSnapshot is what an "update" proposal stores in before_json: the
// entry as it looked when the proposal was filed, for the reviewer's diff.
type catalogSnapshot struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	URLKey       string `json:"url_key"`
	CategoryID   *int64 `json:"category_id"`
	CategoryName string `json:"category_name"`
	Description  string `json:"description"`
	Homepage     string `json:"homepage"`
	Enabled      bool   `json:"enabled"`
}

type catalogSubmission struct {
	ID              int64            `json:"id"`
	Kind            string           `json:"kind"`
	Status          string           `json:"status"`
	EntryID         *int64           `json:"entry_id"`
	Name            string           `json:"name"`
	URL             string           `json:"url"`
	URLKey          string           `json:"url_key"`
	CategoryID      *int64           `json:"category_id"`
	CategoryName    string           `json:"category_name"`
	Description     string           `json:"description"`
	Homepage        string           `json:"homepage"`
	Before          *catalogSnapshot `json:"before_json"`
	SubmittedBy     *int64           `json:"submitted_by"`
	SubmittedByName string           `json:"submitted_by_name"`
	RejectReason    string           `json:"reject_reason"`
	ReviewedBy      *int64           `json:"reviewed_by"`
	ReviewedByName  string           `json:"reviewed_by_name"`
	ReviewedAt      string           `json:"reviewed_at"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
}

// rowQuerier lets the same lookup run on the pool or inside a transaction.
// With SetMaxOpenConns(1) a transaction holds the only connection, so any
// check made between Begin and Commit MUST use tx as the querier.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func nullableID(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

func categoryExists(q rowQuerier, id int64) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM catalog_categories WHERE id=?`, id).Scan(&n)
	return n > 0, err
}

// catalogFields is the validated form shared by user submissions and admin
// edits. URL normalization is stricter here than for ordinary subscriptions
// because catalog URLs are shown to every user and copied verbatim.
type catalogFields struct {
	Name        string
	URL         string
	URLKey      string
	Description string
	Homepage    string
}

func normalizeCatalogFields(name, rawURL, description, homepage string) (catalogFields, *writeError) {
	cleanName, err := webui.CleanCatalogText(name, webui.MaxCatalogNameLen, "name")
	if err != nil {
		return catalogFields{}, &writeError{http.StatusBadRequest, "invalid_name", err.Error()}
	}
	if cleanName == "" {
		return catalogFields{}, &writeError{http.StatusBadRequest, "invalid_name", "name is required"}
	}
	normalized, key, err := webui.CatalogURLKey(rawURL)
	if err != nil {
		return catalogFields{}, &writeError{http.StatusBadRequest, "invalid_url", err.Error()}
	}
	cleanDescription, err := webui.CleanCatalogText(description, webui.MaxCatalogDescriptionLen, "description")
	if err != nil {
		return catalogFields{}, &writeError{http.StatusBadRequest, "invalid_description", err.Error()}
	}
	cleanHomepage, err := webui.CleanCatalogHomepage(homepage)
	if err != nil {
		return catalogFields{}, &writeError{http.StatusBadRequest, "invalid_homepage", err.Error()}
	}
	return catalogFields{
		Name: cleanName, URL: normalized, URLKey: key,
		Description: cleanDescription, Homepage: cleanHomepage,
	}, nil
}

func catalogMeta() map[string]any {
	return map[string]any{
		"max_name_len":         webui.MaxCatalogNameLen,
		"max_description_len":  webui.MaxCatalogDescriptionLen,
		"max_homepage_len":     webui.MaxCatalogHomepageLen,
		"max_url_len":          webui.MaxCatalogURLLen,
		"max_category_len":     webui.MaxCatalogCategoryLen,
		"max_pending_per_user": webui.MaxPendingPerUser,
		"uncategorized_label":  uncategorizedLabel,
	}
}

// loadCatalogCategories returns every real category plus a synthetic
// "uncategorized" row (id 0). enabledOnly is used by the public listing so
// counts agree with what a regular user can actually see.
func (s *Server) loadCatalogCategories(enabledOnly bool) ([]catalogCategory, error) {
	filter := ""
	if enabledOnly {
		filter = ` AND e.enabled=1`
	}
	rows, err := s.db.Query(`SELECT c.id, c.name,
		(SELECT COUNT(*) FROM catalog_entries e WHERE e.category_id=c.id` + filter + `),
		c.created_at, c.updated_at FROM catalog_categories c ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	out := make([]catalogCategory, 0)
	for rows.Next() {
		var item catalogCategory
		if err := rows.Scan(&item.ID, &item.Name, &item.EntryCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Fully drain the query above before issuing another one: the pool has one
	// connection. The synthetic row is never written to the database.
	var loose int
	query := `SELECT COUNT(*) FROM catalog_entries WHERE category_id IS NULL`
	if enabledOnly {
		query += ` AND enabled=1`
	}
	if err := s.db.QueryRow(query).Scan(&loose); err != nil {
		return nil, err
	}
	out = append(out, catalogCategory{ID: 0, Name: uncategorizedLabel, EntryCount: loose})
	return out, nil
}

// loadCatalogEntries builds the browsable list in ONE query: the per-URL
// subscriber count and the caller's own subscription id come from correlated
// subqueries, never from a query issued inside the rows loop.
func (s *Server) loadCatalogEntries(userID int64, includeDisabled bool) ([]catalogEntry, error) {
	query := `SELECT e.id, e.name, e.url, e.url_key, e.category_id,
		COALESCE(c.name, ''), e.description, e.homepage, e.source_type, e.enabled,
		(SELECT COUNT(DISTINCT s.user_id) FROM subscriptions s
		   WHERE s.url_key=e.url_key AND s.deleted_at IS NULL),
		(SELECT s.id FROM subscriptions s
		   WHERE s.user_id=? AND s.url_key=e.url_key AND s.deleted_at IS NULL
		   ORDER BY s.id LIMIT 1),
		e.created_at, e.updated_at, e.preset_key, e.preset_version, e.preset_edited
		FROM catalog_entries e
		LEFT JOIN catalog_categories c ON c.id=e.category_id`
	if !includeDisabled {
		query += ` WHERE e.enabled=1`
	}
	query += ` ORDER BY e.name, e.id`
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	out := make([]catalogEntry, 0)
	for rows.Next() {
		var item catalogEntry
		var enabled, presetEdited int
		var categoryID, subscriptionID sql.NullInt64
		var categoryName string
		if err := rows.Scan(&item.ID, &item.Name, &item.URL, &item.URLKey, &categoryID,
			&categoryName, &item.Description, &item.Homepage, &item.SourceType, &enabled,
			&item.SubscriberCount, &subscriptionID, &item.CreatedAt, &item.UpdatedAt,
			&item.PresetKey, &item.PresetVersion, &presetEdited); err != nil {
			rows.Close()
			return nil, err
		}
		item.CategoryID = nullableID(categoryID)
		item.CategoryName = categoryName
		if item.CategoryName == "" {
			item.CategoryName = uncategorizedLabel
		}
		item.Enabled = enabled != 0
		item.PresetEdited = presetEdited != 0
		item.SubscriptionID = nullableID(subscriptionID)
		item.Subscribed = item.SubscriptionID != nil
		// Defense in depth: catalog URLs are validated at write time, but a
		// future relaxation must not leak credentials through a public list.
		item.URL = maskFeedURL(item.URL)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return out, rows.Close()
}

// loadSubmissions is shared by the public pending list, "my submissions" and
// the admin review queue. where must be a constant SQL fragment, never user
// input.
func (s *Server) loadSubmissions(where string, args ...any) ([]catalogSubmission, error) {
	query := `SELECT sub.id, sub.kind, sub.status, sub.entry_id, sub.name,
		sub.url, sub.url_key, sub.category_id, COALESCE(c.name, ''),
		sub.description, sub.homepage, sub.before_json,
		sub.submitted_by, COALESCE(u.username, ''), sub.reject_reason,
		sub.reviewed_by, COALESCE(r.username, ''), COALESCE(sub.reviewed_at, ''),
		sub.created_at, sub.updated_at
		FROM submissions sub
		LEFT JOIN catalog_categories c ON c.id=sub.category_id
		LEFT JOIN users u ON u.id=sub.submitted_by
		LEFT JOIN users r ON r.id=sub.reviewed_by
		` + where + ` ORDER BY sub.id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]catalogSubmission, 0)
	for rows.Next() {
		var item catalogSubmission
		var entryID, categoryID, submittedBy, reviewedBy sql.NullInt64
		var categoryName, beforeJSON string
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &entryID, &item.Name,
			&item.URL, &item.URLKey, &categoryID, &categoryName,
			&item.Description, &item.Homepage, &beforeJSON,
			&submittedBy, &item.SubmittedByName, &item.RejectReason,
			&reviewedBy, &item.ReviewedByName, &item.ReviewedAt,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		item.EntryID = nullableID(entryID)
		item.CategoryID = nullableID(categoryID)
		item.CategoryName = categoryName
		if item.CategoryName == "" {
			item.CategoryName = uncategorizedLabel
		}
		item.SubmittedBy = nullableID(submittedBy)
		item.ReviewedBy = nullableID(reviewedBy)
		item.URL = maskFeedURL(item.URL)
		if beforeJSON != "" {
			var snap catalogSnapshot
			if err := json.Unmarshal([]byte(beforeJSON), &snap); err == nil {
				item.Before = &snap
			}
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return out, rows.Close()
}

func (s *Server) listCatalog(w http.ResponseWriter, _ *http.Request, c Ctx) {
	entries, err := s.loadCatalogEntries(c.User.ID, false)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	categories, err := s.loadCatalogCategories(true)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries, "categories": categories,
		"total": len(entries), "meta": catalogMeta(),
	})
}

func (s *Server) listPendingSubmissions(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	items, err := s.loadSubmissions(`WHERE sub.status='pending'`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	categories, err := s.loadCatalogCategories(true)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"submissions": items, "categories": categories, "meta": catalogMeta(),
	})
}

func (s *Server) mySubmissions(w http.ResponseWriter, _ *http.Request, c Ctx) {
	items, err := s.loadSubmissions(`WHERE sub.submitted_by=?`, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"submissions": items})
}

// createSubmission files a "new" entry or an "update" to an existing one. It
// never writes to catalog_entries: only an administrator's approval does that.
func (s *Server) createSubmission(w http.ResponseWriter, r *http.Request, c Ctx) {
	var req struct {
		Kind        string `json:"kind"`
		EntryID     *int64 `json:"entry_id"`
		Name        string `json:"name"`
		URL         string `json:"url"`
		CategoryID  *int64 `json:"category_id"`
		Description string `json:"description"`
		Homepage    string `json:"homepage"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Kind != "new" && req.Kind != "update" {
		fail(w, http.StatusBadRequest, "invalid_kind", "kind must be new or update")
		return
	}
	if (req.Kind == "update") != (req.EntryID != nil) {
		fail(w, http.StatusBadRequest, "bad_request", "update requires entry_id; new must not carry one")
		return
	}
	fields, e := normalizeCatalogFields(req.Name, req.URL, req.Description, req.Homepage)
	if e != nil {
		fail(w, e.status, e.code, e.message)
		return
	}
	if req.CategoryID == nil {
		fail(w, http.StatusBadRequest, "invalid_category", "please choose a category")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	ok, err := categoryExists(tx, *req.CategoryID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !ok {
		fail(w, http.StatusBadRequest, "invalid_category", "category does not exist")
		return
	}
	var pending int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM submissions WHERE submitted_by=? AND status='pending'`,
		c.User.ID).Scan(&pending); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if pending >= webui.MaxPendingPerUser {
		fail(w, http.StatusTooManyRequests, "pending_limit",
			fmt.Sprintf("at most %d pending submissions per account", webui.MaxPendingPerUser))
		return
	}

	beforeJSON := ""
	if req.Kind == "update" {
		var snap catalogSnapshot
		var categoryID sql.NullInt64
		var categoryName string
		var enabled int
		err := tx.QueryRow(`SELECT e.name, e.url, e.url_key, e.category_id,
			COALESCE(c.name, ''), e.description, e.homepage, e.enabled
			FROM catalog_entries e LEFT JOIN catalog_categories c ON c.id=e.category_id
			WHERE e.id=?`, *req.EntryID).Scan(&snap.Name, &snap.URL, &snap.URLKey,
			&categoryID, &categoryName, &snap.Description, &snap.Homepage, &enabled)
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "not_found", "catalog entry not found")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		snap.CategoryID = nullableID(categoryID)
		snap.CategoryName = categoryName
		if snap.CategoryName == "" {
			snap.CategoryName = uncategorizedLabel
		}
		snap.Enabled = enabled != 0
		b, err := json.Marshal(snap)
		if err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		beforeJSON = string(b)
		var sameEntry int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM submissions WHERE entry_id=? AND status='pending'`,
			*req.EntryID).Scan(&sameEntry); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if sameEntry > 0 {
			fail(w, http.StatusConflict, "duplicate_pending", "this entry already has a pending proposal")
			return
		}
	}

	// A proposed URL must not collide with an existing catalog entry (other
	// than the very entry an update is about) or with any live proposal.
	conflictQuery := `SELECT COUNT(*) FROM catalog_entries WHERE url_key=?`
	conflictArgs := []any{fields.URLKey}
	if req.Kind == "update" {
		conflictQuery += ` AND id<>?`
		conflictArgs = append(conflictArgs, *req.EntryID)
	}
	var existing int
	if err := tx.QueryRow(conflictQuery, conflictArgs...).Scan(&existing); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing > 0 {
		fail(w, http.StatusConflict, "duplicate_catalog", "this URL is already in the catalog")
		return
	}
	var pendingSameURL int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM submissions WHERE url_key=? AND status='pending'`,
		fields.URLKey).Scan(&pendingSameURL); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if pendingSameURL > 0 {
		fail(w, http.StatusConflict, "duplicate_pending", "this URL already has a pending proposal")
		return
	}

	now := store.Now()
	res, err := tx.Exec(`INSERT INTO submissions
		(kind, status, entry_id, url, url_key, name, category_id, description, homepage,
		 before_json, submitted_by, created_at, updated_at)
		VALUES (?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Kind, req.EntryID, fields.URL, fields.URLKey, fields.Name, req.CategoryID,
		fields.Description, fields.Homepage, beforeJSON, c.User.ID, now, now)
	if err != nil {
		// The partial unique index is the last line of defence against two
		// simultaneous proposals for the same URL.
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			fail(w, http.StatusConflict, "duplicate_pending", "this URL already has a pending proposal")
		} else {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
		}
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

// withdrawSubmission lets an author pull back their own pending proposal. The
// row stays as history with status "withdrawn" and releases the URL.
func (s *Server) withdrawSubmission(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	now := store.Now()
	res, err := s.db.Exec(`UPDATE submissions
		SET status='withdrawn', reviewed_by=?, reviewed_at=?, updated_at=?
		WHERE id=? AND submitted_by=? AND status='pending'`, c.User.ID, now, now, id, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "pending submission")
}

// quickSubscribe copies an approved catalog template into the caller's own
// subscriptions. It goes through subscribeURL so the user_seen baseline is
// copied too: otherwise the next cycle would replay every historical release.
func (s *Server) quickSubscribe(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var name, rawURL, sourceType string
	var enabled int
	err = s.db.QueryRow(`SELECT name, url, source_type, enabled FROM catalog_entries WHERE id=?`, id).
		Scan(&name, &rawURL, &sourceType, &enabled)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "not_found", "catalog entry not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if enabled == 0 {
		fail(w, http.StatusConflict, "disabled", "this catalog entry is disabled")
		return
	}
	key, e := s.subscribeURL(c.User.ID, subscribeInput{
		URL: rawURL, DisplayName: name, SourceType: sourceType,
	})
	if e != nil {
		fail(w, e.status, e.code, e.message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "url_key": key})
}
