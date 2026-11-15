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

// adminCategories returns only real, editable categories. The synthetic
// "uncategorized" row used by public listings has no id and cannot be renamed
// or deleted.
func (s *Server) adminCategories() ([]catalogCategory, error) {
	rows, err := s.db.Query(`SELECT c.id, c.name,
		(SELECT COUNT(*) FROM catalog_entries e WHERE e.category_id=c.id),
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
	return out, rows.Close()
}

func (s *Server) adminListCatalog(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	entries, err := s.loadCatalogEntries(0, true)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	categories, err := s.adminCategories()
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	var pendingCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM submissions WHERE status='pending'`).Scan(&pendingCount); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries, "categories": categories,
		"pending_count": pendingCount, "meta": catalogMeta(),
	})
}

// adminCreateEntry is a direct, trusted write: administrators do not need to
// review their own edits. Only user submissions pass through approval.
func (s *Server) adminCreateEntry(w http.ResponseWriter, r *http.Request, c Ctx) {
	var req struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		CategoryID  *int64 `json:"category_id"`
		Description string `json:"description"`
		Homepage    string `json:"homepage"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
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
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
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
	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM catalog_entries WHERE url_key=?`, fields.URLKey).Scan(&existing); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if existing > 0 {
		fail(w, http.StatusConflict, "duplicate_catalog", "this URL is already in the catalog")
		return
	}
	now := store.Now()
	res, err := tx.Exec(`INSERT INTO catalog_entries
		(url, url_key, name, category_id, description, homepage, source_type, enabled, created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,'atom',?,?,?,?)`,
		fields.URL, fields.URLKey, fields.Name, req.CategoryID, fields.Description,
		fields.Homepage, enabled, c.User.ID, now, now)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

// adminUpdateEntry accepts a partial patch. category_id may be explicitly
// null to leave an entry uncategorized after its category was deleted, so the
// patch is decoded as raw JSON values instead of pointer fields.
func (s *Server) adminUpdateEntry(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var patch map[string]json.RawMessage
	if err := decodeJSON(w, r, &patch); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	allowed := map[string]bool{
		"name": true, "url": true, "description": true, "homepage": true,
		"category_id": true, "enabled": true, "source_type": true,
	}
	for key := range patch {
		if !allowed[key] {
			fail(w, http.StatusBadRequest, "bad_request", "unknown field: "+key)
			return
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	sets := make([]string, 0, 8)
	args := make([]any, 0, 10)
	add := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}
	// Fixed order keeps the generated SQL deterministic regardless of map order.
	for _, key := range []string{"name", "url", "description", "homepage", "category_id", "enabled", "source_type"} {
		raw, present := patch[key]
		if !present {
			continue
		}
		switch key {
		case "name":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				fail(w, http.StatusBadRequest, "bad_request", "name must be a string")
				return
			}
			clean, err := webui.CleanCatalogText(value, webui.MaxCatalogNameLen, "name")
			if err != nil || clean == "" {
				fail(w, http.StatusBadRequest, "invalid_name", "name must be a non-empty string")
				return
			}
			add("name", clean)
		case "url":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				fail(w, http.StatusBadRequest, "bad_request", "url must be a string")
				return
			}
			normalized, key, err := webui.CatalogURLKey(value)
			if err != nil {
				fail(w, http.StatusBadRequest, "invalid_url", err.Error())
				return
			}
			var existing int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM catalog_entries WHERE url_key=? AND id<>?`,
				key, id).Scan(&existing); err != nil {
				fail(w, http.StatusInternalServerError, "db_error", err.Error())
				return
			}
			if existing > 0 {
				fail(w, http.StatusConflict, "duplicate_catalog", "this URL is already in the catalog")
				return
			}
			add("url", normalized)
			add("url_key", key)
		case "description":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				fail(w, http.StatusBadRequest, "bad_request", "description must be a string")
				return
			}
			clean, err := webui.CleanCatalogText(value, webui.MaxCatalogDescriptionLen, "description")
			if err != nil {
				fail(w, http.StatusBadRequest, "invalid_description", err.Error())
				return
			}
			add("description", clean)
		case "homepage":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				fail(w, http.StatusBadRequest, "bad_request", "homepage must be a string")
				return
			}
			clean, err := webui.CleanCatalogHomepage(value)
			if err != nil {
				fail(w, http.StatusBadRequest, "invalid_homepage", err.Error())
				return
			}
			add("homepage", clean)
		case "category_id":
			if string(raw) == "null" {
				add("category_id", nil)
				continue
			}
			var categoryID int64
			if err := json.Unmarshal(raw, &categoryID); err != nil {
				fail(w, http.StatusBadRequest, "invalid_category", "category_id must be an id or null")
				return
			}
			ok, err := categoryExists(tx, categoryID)
			if err != nil {
				fail(w, http.StatusInternalServerError, "db_error", err.Error())
				return
			}
			if !ok {
				fail(w, http.StatusBadRequest, "invalid_category", "category does not exist")
				return
			}
			add("category_id", categoryID)
		case "enabled":
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				fail(w, http.StatusBadRequest, "bad_request", "enabled must be a boolean")
				return
			}
			add("enabled", boolToInt(value))
		case "source_type":
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || value != "atom" {
				fail(w, http.StatusBadRequest, "invalid_source_type", "source_type must be atom")
				return
			}
			add("source_type", value)
		}
	}
	if len(sets) == 0 {
		fail(w, http.StatusBadRequest, "empty_patch", "no fields to update")
		return
	}
	sets = append(sets, "updated_at=?")
	args = append(args, store.Now(), id)
	res, err := tx.Exec(`UPDATE catalog_entries SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		fail(w, http.StatusNotFound, "not_found", "catalog entry not found")
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminDeleteEntry physically removes a template, releasing its URL. Rows
// already copied into users' subscriptions are untouched.
func (s *Server) adminDeleteEntry(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := s.db.Exec(`DELETE FROM catalog_entries WHERE id=?`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "catalog entry")
}

func (s *Server) adminListSubmissions(w http.ResponseWriter, r *http.Request, _ Ctx) {
	status := r.URL.Query().Get("status")
	where := `WHERE sub.status='pending'`
	if status == "all" {
		where = ""
	} else if status != "" && status != "pending" {
		fail(w, http.StatusBadRequest, "bad_request", "status must be pending or all")
		return
	}
	items, err := s.loadSubmissions(where)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	categories, err := s.adminCategories()
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"submissions": items, "categories": categories})
}

// approveSubmission is the only place a proposal becomes catalog content.
// Everything happens in one transaction, including a second URL-uniqueness
// check: an administrator may have added the same URL while it sat in review.
func (s *Server) approveSubmission(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	var kind, status, rawURL, urlKey, name, description, homepage string
	var categoryID, entryID, submittedBy sql.NullInt64
	err = tx.QueryRow(`SELECT kind, status, url, url_key, name, category_id,
		description, homepage, entry_id, submitted_by FROM submissions WHERE id=?`, id).
		Scan(&kind, &status, &rawURL, &urlKey, &name, &categoryID,
			&description, &homepage, &entryID, &submittedBy)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "not_found", "submission not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if status != "pending" {
		fail(w, http.StatusConflict, "not_pending", "only pending submissions can be approved")
		return
	}

	var targetID int64
	conflictQuery := `SELECT COUNT(*) FROM catalog_entries WHERE url_key=?`
	conflictArgs := []any{urlKey}
	if kind == "update" {
		if !entryID.Valid {
			fail(w, http.StatusConflict, "entry_gone", "the entry this proposal updates no longer exists")
			return
		}
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM catalog_entries WHERE id=?`, entryID.Int64).Scan(&exists); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if exists == 0 {
			fail(w, http.StatusConflict, "entry_gone", "the entry this proposal updates no longer exists")
			return
		}
		targetID = entryID.Int64
		conflictQuery += ` AND id<>?`
		conflictArgs = append(conflictArgs, targetID)
	}
	var conflicts int
	if err := tx.QueryRow(conflictQuery, conflictArgs...).Scan(&conflicts); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if conflicts > 0 {
		fail(w, http.StatusConflict, "conflict_url",
			"an entry with this URL already exists; reject the proposal or remove that entry first")
		return
	}

	now := store.Now()
	if kind == "new" {
		res, err := tx.Exec(`INSERT INTO catalog_entries
			(url, url_key, name, category_id, description, homepage, source_type, enabled, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,'atom',1,?,?,?)`,
			rawURL, urlKey, name, categoryID, description, homepage, submittedBy, now, now)
		if err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		targetID, _ = res.LastInsertId()
	} else {
		if _, err := tx.Exec(`UPDATE catalog_entries SET
			url=?, url_key=?, name=?, category_id=?, description=?, homepage=?, updated_at=? WHERE id=?`,
			rawURL, urlKey, name, categoryID, description, homepage, now, targetID); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	if _, err := tx.Exec(`UPDATE submissions SET status='approved', reviewed_by=?, reviewed_at=?, updated_at=?
		WHERE id=? AND status='pending'`, c.User.ID, now, now, id); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entry_id": targetID})
}

func (s *Server) rejectSubmission(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	reason, err := webui.CleanCatalogText(req.Reason, webui.MaxCatalogRejectReason, "reason")
	if err != nil || reason == "" {
		fail(w, http.StatusBadRequest, "bad_request", "a non-empty rejection reason is required")
		return
	}
	var status string
	err = s.db.QueryRow(`SELECT status FROM submissions WHERE id=?`, id).Scan(&status)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "not_found", "submission not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if status != "pending" {
		fail(w, http.StatusConflict, "not_pending", "only pending submissions can be rejected")
		return
	}
	now := store.Now()
	if _, err := s.db.Exec(`UPDATE submissions SET status='rejected', reject_reason=?,
		reviewed_by=?, reviewed_at=?, updated_at=? WHERE id=? AND status='pending'`,
		reason, c.User.ID, now, now, id); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listCategories(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	categories, err := s.adminCategories()
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": categories, "meta": catalogMeta()})
}

func cleanCategoryName(raw string) (string, error) {
	return webui.CleanCatalogText(raw, webui.MaxCatalogCategoryLen, "category")
}

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request, _ Ctx) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	name, err := cleanCategoryName(req.Name)
	if err != nil || name == "" {
		fail(w, http.StatusBadRequest, "invalid_name", "a non-empty category name is required")
		return
	}
	now := store.Now()
	res, err := s.db.Exec(`INSERT INTO catalog_categories (name, created_at, updated_at) VALUES (?,?,?)`,
		name, now, now)
	if err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			fail(w, http.StatusConflict, "duplicate_category", "this category already exists")
		} else {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
		}
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) renameCategory(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	name, err := cleanCategoryName(req.Name)
	if err != nil || name == "" {
		fail(w, http.StatusBadRequest, "invalid_name", "a non-empty category name is required")
		return
	}
	res, err := s.db.Exec(`UPDATE catalog_categories SET name=?, updated_at=? WHERE id=?`,
		name, store.Now(), id)
	if err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			fail(w, http.StatusConflict, "duplicate_category", "this category already exists")
		} else {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
		}
		return
	}
	requireAffected(w, res, "category")
}

// deleteCategory relies on ON DELETE SET NULL to move every referencing entry
// and proposal to "uncategorized" in the same transaction.
func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	var entries, proposals int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM catalog_entries WHERE category_id=?`, id).Scan(&entries); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM submissions WHERE category_id=?`, id).Scan(&proposals); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	res, err := tx.Exec(`DELETE FROM catalog_categories WHERE id=?`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		fail(w, http.StatusNotFound, "not_found", "category not found")
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reassigned": entries + proposals})
}

// exportCatalog produces a portable document with no ids, credentials or user
// data. GET is a safe method: it is neither rate limited nor audit logged.
func (s *Server) exportCatalog(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	categories, err := s.adminCategories()
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	entries, err := s.loadCatalogEntries(0, true)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	doc := webui.CatalogDocument{
		Format:     webui.CatalogDocumentFormat,
		Version:    webui.CatalogDocumentV1,
		ExportedAt: store.Now(),
		Categories: make([]webui.CatalogDocumentCategory, 0, len(categories)),
		Entries:    make([]webui.CatalogDocumentEntry, 0, len(entries)),
	}
	for _, c := range categories {
		doc.Categories = append(doc.Categories, webui.CatalogDocumentCategory{Name: c.Name})
	}
	for _, e := range entries {
		category := e.CategoryName
		if category == uncategorizedLabel || e.CategoryID == nil {
			category = ""
		}
		doc.Entries = append(doc.Entries, webui.CatalogDocumentEntry{
			Name: e.Name, URL: e.URL, Category: category,
			Description: e.Description, Homepage: e.Homepage, Enabled: e.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, doc)
}

type preparedImportEntry struct {
	fields   catalogFields
	category string
	enabled  bool
}

// importCatalog validates the whole document before touching the database,
// then applies it in a single transaction so a conflict or bad URL leaves no
// half-imported catalog. It never writes subscriptions, feed_pool or user_seen.
func (s *Server) importCatalog(w http.ResponseWriter, r *http.Request, c Ctx) {
	var doc webui.CatalogDocument
	if err := decodeJSON(w, r, &doc); err != nil {
		fail(w, http.StatusBadRequest, "invalid_import", err.Error())
		return
	}
	if doc.Format != webui.CatalogDocumentFormat || doc.Version != webui.CatalogDocumentV1 {
		fail(w, http.StatusBadRequest, "invalid_import", "unsupported catalog document")
		return
	}
	strategy := doc.Strategy
	if strategy == "" {
		strategy = webui.ImportStrategySkip
	}
	if !webui.ValidImportStrategy(strategy) {
		fail(w, http.StatusBadRequest, "invalid_import", "strategy must be skip, overwrite or error")
		return
	}

	categoryNames := make(map[string]bool)
	for _, cat := range doc.Categories {
		name, err := cleanCategoryName(cat.Name)
		if err != nil || name == "" {
			fail(w, http.StatusBadRequest, "invalid_import", "invalid category name in document")
			return
		}
		categoryNames[name] = true
	}
	prepared := make([]preparedImportEntry, 0, len(doc.Entries))
	for i, entry := range doc.Entries {
		fields, e := normalizeCatalogFields(entry.Name, entry.URL, entry.Description, entry.Homepage)
		if e != nil {
			fail(w, http.StatusBadRequest, "invalid_import", fmt.Sprintf("entry %d: %s", i+1, e.message))
			return
		}
		category := strings.TrimSpace(entry.Category)
		if category != "" && category != uncategorizedLabel {
			name, err := cleanCategoryName(category)
			if err != nil {
				fail(w, http.StatusBadRequest, "invalid_import", fmt.Sprintf("entry %d: invalid category", i+1))
				return
			}
			category = name
			categoryNames[name] = true
		} else {
			category = ""
		}
		prepared = append(prepared, preparedImportEntry{fields: fields, category: category, enabled: entry.Enabled})
	}

	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	now := store.Now()
	for name := range categoryNames {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO catalog_categories (name, created_at, updated_at) VALUES (?,?,?)`,
			name, now, now); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	created, updated, skipped := 0, 0, 0
	for _, item := range prepared {
		var existingID int64
		err := tx.QueryRow(`SELECT id FROM catalog_entries WHERE url_key=?`, item.fields.URLKey).Scan(&existingID)
		if err != nil && err != sql.ErrNoRows {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		categoryArg := any(nil)
		if item.category != "" {
			categoryArg = item.category
		}
		if err == sql.ErrNoRows {
			if _, err := tx.Exec(`INSERT INTO catalog_entries
				(url, url_key, name, category_id, description, homepage, source_type, enabled, created_by, created_at, updated_at)
				VALUES (?,?,?,(SELECT id FROM catalog_categories WHERE name=?),?,?,'atom',?,?,?,?)`,
				item.fields.URL, item.fields.URLKey, item.fields.Name, categoryArg,
				item.fields.Description, item.fields.Homepage,
				boolToInt(item.enabled), c.User.ID, now, now); err != nil {
				fail(w, http.StatusInternalServerError, "db_error", err.Error())
				return
			}
			created++
			continue
		}
		switch strategy {
		case webui.ImportStrategyError:
			fail(w, http.StatusConflict, "conflict",
				fmt.Sprintf("entry %q already exists", item.fields.Name))
			return
		case webui.ImportStrategySkip:
			skipped++
		case webui.ImportStrategyOverwrite:
			if _, err := tx.Exec(`UPDATE catalog_entries SET
				name=?, category_id=(SELECT id FROM catalog_categories WHERE name=?),
				description=?, homepage=?, enabled=?, url=?, updated_at=? WHERE id=?`,
				item.fields.Name, categoryArg, item.fields.Description, item.fields.Homepage,
				boolToInt(item.enabled), item.fields.URL, now, existingID); err != nil {
				fail(w, http.StatusInternalServerError, "db_error", err.Error())
				return
			}
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "created": created, "updated": updated, "skipped": skipped,
	})
}
