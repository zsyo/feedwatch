//go:build webui

package store

import (
	"database/sql"
	_ "embed"
	"fmt"
)

// schemaSQL holds the whole version-1 schema. Later feature points add query
// helpers only; a schema change would bump schemaVersion and add a migration.
//
//go:embed schema.sql
var schemaSQL string

const schemaVersion = 1

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
