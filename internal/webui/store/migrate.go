//go:build webui

package store

import (
	"fmt"
	"strings"
)

// quoteIdent quotes an identifier for the dialect. Only the migration renames
// need this: the rest of the schema avoids reserved words entirely.
func quoteIdent(d Dialect, name string) string {
	if d == MySQL {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}

// columnDecl is the per-dialect body of an ADD COLUMN. The three schema files
// already declare these columns for a fresh database; the declarations here
// exist so a database created by an older release can be upgraded in place.
type columnDecl struct {
	table, column string
	decl          map[Dialect]string
}

var incrementalColumns = []columnDecl{
	{
		table: "users", column: "timezone",
		decl: map[Dialect]string{
			SQLite:   "TEXT NOT NULL DEFAULT ''",
			MySQL:    "VARCHAR(64) NOT NULL DEFAULT ''",
			Postgres: "TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		// v4 moved the notification shape from each subscription to the
		// account. Any value an old row had in subscriptions.mode is
		// intentionally dropped: it was a per-subscription choice, and guessing
		// an account-wide value from a mix of them would change behaviour for a
		// user who had both. A database migrated from v3 keeps that column, now
		// unused; it has a NOT NULL DEFAULT, so the narrowed INSERT in the API
		// keeps working without a table rebuild.
		table: "users", column: "notify_mode",
		decl: map[Dialect]string{
			SQLite:   "TEXT NOT NULL DEFAULT 'digest' CHECK (notify_mode IN ('digest', 'per_feed'))",
			MySQL:    "VARCHAR(32) NOT NULL DEFAULT 'digest' CHECK (notify_mode IN ('digest', 'per_feed'))",
			Postgres: "TEXT NOT NULL DEFAULT 'digest' CHECK (notify_mode IN ('digest', 'per_feed'))",
		},
	},
	{
		// v6 columns. ALTER TABLE ADD COLUMN cannot declare UNIQUE, and every
		// pre-existing row has preset_key='', so preset_key becomes unique
		// through the partial index in schemaIndexes, not through the column.
		table: "catalog_entries", column: "preset_key",
		decl: map[Dialect]string{
			SQLite:   "TEXT NOT NULL DEFAULT ''",
			MySQL:    "VARCHAR(191) NOT NULL DEFAULT ''",
			Postgres: "TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		table: "catalog_entries", column: "preset_version",
		decl: map[Dialect]string{
			SQLite:   "INTEGER NOT NULL DEFAULT 0",
			MySQL:    "INTEGER NOT NULL DEFAULT 0",
			Postgres: "INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		table: "catalog_entries", column: "preset_edited",
		decl: map[Dialect]string{
			SQLite:   "INTEGER NOT NULL DEFAULT 0",
			MySQL:    "INTEGER NOT NULL DEFAULT 0",
			Postgres: "INTEGER NOT NULL DEFAULT 0",
		},
	},
}

// columnRenames are the v7 changes: two columns were named after a word MySQL
// reserves and a word MySQL treats as a type. Only databases created before
// the multi-backend release still carry the old name.
var columnRenames = []struct{ table, from, to string }{
	{"settings", "key", "setting_key"},
	{"notifications", "text", "body"},
}

// ensureSchema applies every step needed to reach schemaVersion. It runs inside
// a transaction on SQLite and PostgreSQL; on MySQL the caller has to run it
// statement by statement because DDL there implicitly commits.
func ensureSchema(g *gateway) error {
	statements, err := schemaStatements(g.Dialect())
	if err != nil {
		return err
	}
	for _, stmt := range statements {
		if _, err := g.Exec(stmt); err != nil {
			return explain(stmt, err)
		}
	}
	for _, rename := range columnRenames {
		if err := renameColumn(g, rename.table, rename.from, rename.to); err != nil {
			return err
		}
	}
	for _, col := range incrementalColumns {
		decl, ok := col.decl[g.Dialect()]
		if !ok {
			return fmt.Errorf("webui: no %s declaration for %s.%s", g.Dialect(), col.table, col.column)
		}
		if err := ensureColumn(g, col.table, col.column, decl); err != nil {
			return err
		}
	}
	for _, def := range schemaIndexes {
		if err := ensureIndex(g, def); err != nil {
			return err
		}
	}
	return nil
}

// renameColumn renames table.from to table.to on databases that still carry the
// old name, and is a no-op everywhere else.
func renameColumn(g *gateway, table, from, to string) error {
	hasFrom, err := columnExists(g, table, from)
	if err != nil {
		return fmt.Errorf("webui: inspect %s: %w", table, err)
	}
	if !hasFrom {
		return nil
	}
	hasTo, err := columnExists(g, table, to)
	if err != nil {
		return fmt.Errorf("webui: inspect %s: %w", table, err)
	}
	if hasTo {
		return nil
	}
	stmt := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s",
		table, quoteIdent(g.Dialect(), from), to)
	if _, err := g.Exec(stmt); err != nil {
		return explain(stmt, err)
	}
	return nil
}

// ensureColumn adds table.column when it is missing.
func ensureColumn(g *gateway, table, column, decl string) error {
	found, err := columnExists(g, table, column)
	if err != nil {
		return fmt.Errorf("webui: inspect %s: %w", table, err)
	}
	if found {
		return nil
	}
	stmt := "ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl
	if _, err := g.Exec(stmt); err != nil {
		return explain(stmt, err)
	}
	return nil
}

// ensureIndex creates one index from schemaIndexes. SQLite and PostgreSQL could
// use CREATE INDEX IF NOT EXISTS, MySQL cannot, so existence is always checked
// first and the statement never carries the clause.
func ensureIndex(g *gateway, def indexDef) error {
	exists, err := indexExists(g, def.Table, def.Name)
	if err != nil {
		return fmt.Errorf("webui: inspect index %s: %w", def.Name, err)
	}
	if exists {
		return nil
	}
	stmt := def.statement(g.Dialect())
	if _, err := g.Exec(stmt); err != nil {
		return explain(stmt, err)
	}
	return nil
}

// migrate brings the database to schemaVersion, or refuses to touch a database
// written by a newer binary.
func migrate(db *DB) error {
	version, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("webui: database schema version %d is newer than this binary supports (%d)", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	if db.Dialect() == MySQL {
		// MySQL DDL commits implicitly, so a wrapping transaction would give a
		// false sense of atomicity. Each step is individually idempotent and the
		// version is recorded last, so a failed run simply resumes next start.
		if err := ensureSchema(&db.gateway); err != nil {
			return err
		}
		return recordVersion(db)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("webui: begin schema migration: %w", err)
	}
	defer tx.Rollback()
	if err := ensureSchema(&tx.gateway); err != nil {
		return err
	}
	if _, err := tx.ExecIgnore(`INSERT INTO schema_migrations (version, applied_at) VALUES (?,?)`,
		schemaVersion, Now()); err != nil {
		return fmt.Errorf("webui: record schema version: %w", err)
	}
	if tx.Dialect() == SQLite {
		// PRAGMA cannot be parameterized, but the value is a compile-time
		// constant. The legacy marker stays in sync so a pre-1.2 binary still
		// recognises a newer database instead of silently downgrading it.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("webui: set sqlite user_version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("webui: commit schema migration: %w", err)
	}
	return nil
}

// normalizeDSN trims surrounding whitespace so an env var pasted with a
// trailing newline into a compose file does not break the driver.
func normalizeDSN(dsn string) string { return strings.TrimSpace(dsn) }
