//go:build webui

package store

import (
	"database/sql"
	_ "embed"
	"fmt"
)

// schemaSQL holds the latest schema. Every statement is IF NOT EXISTS, so a
// database created by an older release simply gains the tables it lacks. This
// only works for NEW TABLES: adding a column to an existing table needs a real
// incremental step here (ALTER TABLE) plus its own version bump.
//
//go:embed schema.sql
var schemaSQL string

const schemaVersion = 5

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("webui: read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("webui: database schema version %d is newer than this binary supports (%d)", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("webui: begin schema migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaSQL); err != nil {
		return fmt.Errorf("webui: apply schema: %w", err)
	}
	// Columns added to an existing table cannot come from schema.sql, which only
	// ever creates whole tables. Each one is applied here, guarded by a lookup so
	// that a brand-new database (where schema.sql already declared it) is a no-op.
	if err := ensureColumn(tx, "users", "timezone", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// v4 moved the notification shape from each subscription to the account.
	// Any value an old row had in subscriptions.mode is intentionally dropped:
	// it was a per-subscription choice, and guessing an account-wide value from
	// a mix of them would change behaviour for a user who had both. A database
	// migrated from v3 keeps that column, now unused; it has a NOT NULL DEFAULT,
	// so the narrowed INSERT in the API keeps working without a table rebuild.
	if err := ensureColumn(tx, "users", "notify_mode",
		"TEXT NOT NULL DEFAULT 'digest' CHECK (notify_mode IN ('digest', 'per_feed'))"); err != nil {
		return err
	}
	// v5 adds the three catalog tables. They are whole new tables, so the
	// IF NOT EXISTS statements above cover both a fresh database and a v4
	// upgrade; no incremental ALTER step is needed for them.
	// PRAGMA cannot be parameterized, but the value is a compile-time constant.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("webui: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("webui: commit schema migration: %w", err)
	}
	return nil
}

// ensureColumn adds table.column when it is missing. The rows are fully drained
// and closed before the ALTER, because the pool holds a single connection.
func ensureColumn(tx *sql.Tx, table, column, decl string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("webui: inspect %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("webui: inspect %s: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("webui: inspect %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("webui: inspect %s: %w", table, err)
	}
	if found {
		return nil
	}
	if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl); err != nil {
		return fmt.Errorf("webui: add column %s.%s: %w", table, column, err)
	}
	return nil
}
