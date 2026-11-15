//go:build webui

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"feedwatch/internal/webui/presets"
	"feedwatch/internal/webui/store"
)

type presetFileResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Skipped   int    `json:"skipped"`
	Protected int    `json:"protected"`
	Adopted   int    `json:"adopted"`
}

type presetSyncResult struct {
	OK        bool               `json:"ok"`
	Created   int                `json:"created"`
	Updated   int                `json:"updated"`
	Skipped   int                `json:"skipped"`
	Protected int                `json:"protected"`
	Adopted   int                `json:"adopted"`
	Files     []presetFileResult `json:"files"`
	SyncedAt  string             `json:"synced_at"`
}

// syncPresets fetches every remote category file and applies the result in a
// single transaction. All network IO and document validation happen BEFORE
// Begin(): with SetMaxOpenConns(1) a transaction holds the only connection.
func (s *Server) syncPresets(w http.ResponseWriter, r *http.Request, _ Ctx) {
	if s.presetLoader == nil {
		failSync(w, http.StatusInternalServerError, "db_error", "preset loader is not configured", emptyPresetFiles())
		return
	}
	// Copy so per-request values never mutate the shared loader.
	loader := *s.presetLoader
	timeout := loader.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if loader.BaseURL == "" {
		failSync(w, http.StatusBadRequest, "preset_base_url_invalid",
			"presets.base_url 未配置；请在「全局设置」里填写后重启进程", emptyPresetFiles())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	snap, err := loader.Fetch(ctx, loader.BaseURL)
	if err != nil {
		failSync(w, http.StatusBadGateway, "preset_fetch_failed", err.Error(), presetStates(snap))
		return
	}
	prepared, err := presets.Prepare(snap)
	if err != nil {
		failSync(w, http.StatusBadRequest, "preset_invalid_document", err.Error(), presetStates(snap))
		return
	}
	result, code, message := s.applyPresets(prepared, snap)
	if code != "" {
		status := http.StatusInternalServerError
		if code == "preset_conflict" {
			status = http.StatusConflict
		}
		failSync(w, status, code, message, result.Files)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// acceptRemote clears the local-edit guard so the next sync may apply a newer
// remote version. It deliberately does not change preset_version: the version
// comparison in the following sync still decides whether anything is written.
func (s *Server) acceptRemote(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := s.db.Exec(
		`UPDATE catalog_entries SET preset_edited=0, updated_at=? WHERE id=? AND preset_key<>''`,
		store.Now(), id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "preset entry")
}

func emptyPresetFiles() []presetFileResult { return make([]presetFileResult, 0) }

func presetStates(snap *presets.Snapshot) []presetFileResult {
	out := emptyPresetFiles()
	if snap == nil {
		return out
	}
	for _, f := range snap.States {
		out = append(out, presetFileResult{Name: f.Name, Status: f.Status, Reason: f.Reason})
	}
	return out
}

func failSync(w http.ResponseWriter, status int, code, message string, files []presetFileResult) {
	if files == nil {
		files = emptyPresetFiles()
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
		"files": files,
	})
}

func bumpPresetCount(f *presetFileResult, field string) {
	if f == nil {
		return
	}
	switch field {
	case "created":
		f.Created++
	case "updated":
		f.Updated++
	case "skipped":
		f.Skipped++
	case "protected":
		f.Protected++
	}
}

// upsertPresetState records the version we just saw. It never touches the
// deleted column: a tombstone written by adminDeleteEntry must stay set.
func upsertPresetState(tx *store.Tx, key string, version int, now string) error {
	// The counter only ever moves forward: the same preset can appear in two
	// remote files, and a lower version in the second one must not win. Every
	// engine spells "the larger of the two" differently, so this is one of the
	// few statements written per dialect.
	const insert = `INSERT INTO catalog_preset_state (preset_key, last_version, deleted, updated_at) VALUES (?,?,0,?)`
	var stmt string
	switch tx.Dialect() {
	case store.MySQL:
		stmt = insert + ` ON DUPLICATE KEY UPDATE
			last_version = GREATEST(last_version, VALUES(last_version)),
			updated_at = VALUES(updated_at)`
	case store.Postgres:
		stmt = insert + ` ON CONFLICT (preset_key) DO UPDATE SET
			last_version = GREATEST(catalog_preset_state.last_version, excluded.last_version),
			updated_at = excluded.updated_at`
	default:
		stmt = insert + ` ON CONFLICT(preset_key) DO UPDATE SET
			last_version = MAX(catalog_preset_state.last_version, excluded.last_version),
			updated_at = excluded.updated_at`
	}
	_, err := tx.Exec(stmt, key, version, now)
	return err
}

// applyPresets is the whole write side of a sync. It returns a result (with
// per-file counters) even on failure, plus an error code/message. Every early
// return leaves the deferred Rollback in charge, so a bad remote document can
// never produce a half-updated catalog.
func (s *Server) applyPresets(items []presets.Prepared, snap *presets.Snapshot) (*presetSyncResult, string, string) {
	now := store.Now()
	result := &presetSyncResult{OK: true, Files: presetStates(snap), SyncedAt: now}
	fileByName := make(map[string]*presetFileResult, len(result.Files))
	for i := range result.Files {
		fileByName[result.Files[i].Name] = &result.Files[i]
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, "db_error", err.Error()
	}
	defer tx.Rollback()

	seenCategories := make(map[string]bool, len(items))
	for _, p := range items {
		if seenCategories[p.Category] {
			continue
		}
		seenCategories[p.Category] = true
		if _, err := tx.ExecIgnore(
			`INSERT INTO catalog_categories (name, created_at, updated_at) VALUES (?,?,?)`,
			p.Category, now, now); err != nil {
			return result, "db_error", err.Error()
		}
	}

	for _, p := range items {
		f := fileByName[p.FileName]

		// 1. A tombstone means the administrator deliberately deleted this
		// preset. Skip it forever instead of resurrecting it as "missing".
		var deleted int
		err := tx.QueryRow(`SELECT deleted FROM catalog_preset_state WHERE preset_key=?`, p.PresetKey).Scan(&deleted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, "db_error", err.Error()
		}
		if deleted == 1 {
			result.Skipped++
			bumpPresetCount(f, "skipped")
			continue
		}

		// 2. Locate the local row by its stable preset_key first, then adopt a
		// pre-existing row with the same URL (seed examples, manual entries).
		var rowID int64
		var localVersion, edited int
		found, adopted := false, false
		err = tx.QueryRow(
			`SELECT id, preset_version, preset_edited FROM catalog_entries WHERE preset_key=?`,
			p.PresetKey).Scan(&rowID, &localVersion, &edited)
		if err == nil {
			found = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return result, "db_error", err.Error()
		}
		if !found {
			err = tx.QueryRow(
				`SELECT id, preset_version, preset_edited FROM catalog_entries WHERE url_key=?`,
				p.URLKey).Scan(&rowID, &localVersion, &edited)
			if err == nil {
				found, adopted = true, true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return result, "db_error", err.Error()
			}
		}

		if found {
			if adopted {
				// Claim the row without touching any business field. Whether it
				// is protected or updated is decided by the checks below.
				if _, err := tx.Exec(`UPDATE catalog_entries SET preset_key=? WHERE id=?`,
					p.PresetKey, rowID); err != nil {
					return result, "db_error", err.Error()
				}
				result.Adopted++
				if f != nil {
					f.Adopted++
				}
			}
			// 3. An administrator edited this row: never overwrite it.
			if edited == 1 {
				if err := upsertPresetState(tx, p.PresetKey, p.Version, now); err != nil {
					return result, "db_error", err.Error()
				}
				result.Protected++
				bumpPresetCount(f, "protected")
				continue
			}
			// 4. Only a strictly newer remote version may be applied.
			if p.Version <= localVersion {
				if err := upsertPresetState(tx, p.PresetKey, p.Version, now); err != nil {
					return result, "db_error", err.Error()
				}
				result.Skipped++
				bumpPresetCount(f, "skipped")
				continue
			}
			var conflicts int
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM catalog_entries WHERE url_key=? AND id<>?`,
				p.URLKey, rowID).Scan(&conflicts); err != nil {
				return result, "db_error", err.Error()
			}
			if conflicts > 0 {
				return result, "preset_conflict",
					fmt.Sprintf("预设 %q 的 URL 与另一条目录条目冲突，本次同步未写入任何数据", p.PresetKey)
			}
			// NEVER set preset_edited here: doing so would make every future
			// version comparison permanently short-circuit as "protected".
			if _, err := tx.Exec(`UPDATE catalog_entries SET
				url=?, url_key=?, name=?,
				category_id=(SELECT id FROM catalog_categories WHERE name=?),
				description=?, homepage=?, enabled=?, preset_key=?, preset_version=?,
				preset_edited=0, updated_at=? WHERE id=?`,
				p.URL, p.URLKey, p.Name, p.Category, p.Description, p.Homepage,
				boolToInt(p.Enabled), p.PresetKey, p.Version, now, rowID); err != nil {
				return result, "db_error", err.Error()
			}
			if err := upsertPresetState(tx, p.PresetKey, p.Version, now); err != nil {
				return result, "db_error", err.Error()
			}
			result.Updated++
			bumpPresetCount(f, "updated")
			continue
		}

		// 5. Genuinely missing: insert a new template. This never creates a
		// subscription, a feed_pool row or a user_seen baseline.
		if _, err := tx.Exec(`INSERT INTO catalog_entries
			(url, url_key, name, category_id, description, homepage, source_type, enabled,
			 created_by, created_at, updated_at, preset_key, preset_version, preset_edited)
			VALUES (?,?,?,(SELECT id FROM catalog_categories WHERE name=?),?,?,'atom',?,NULL,?,?,?,?,0)`,
			p.URL, p.URLKey, p.Name, p.Category, p.Description, p.Homepage,
			boolToInt(p.Enabled), now, now, p.PresetKey, p.Version); err != nil {
			return result, "db_error", err.Error()
		}
		if err := upsertPresetState(tx, p.PresetKey, p.Version, now); err != nil {
			return result, "db_error", err.Error()
		}
		result.Created++
		bumpPresetCount(f, "created")
	}

	if err := tx.Commit(); err != nil {
		return result, "db_error", err.Error()
	}
	return result, "", ""
}
