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

const schemaVersion = 2

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
	// PRAGMA cannot be parameterized, but the value is a compile-time constant.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("webui: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("webui: commit schema migration: %w", err)
	}
	return nil
}
