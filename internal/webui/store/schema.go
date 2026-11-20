//go:build webui

package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

// schemaVersion is the version this binary expects. It is recorded in the
// schema_migrations table, and mirrored into PRAGMA user_version on SQLite so
// databases created by 1.1.x keep migrating through the same path.
const schemaVersion = 9

//go:embed schema_sqlite.sql
var schemaSQLite string

//go:embed schema_mysql.sql
var schemaMySQL string

//go:embed schema_postgres.sql
var schemaPostgres string

// schemaStatements returns the CREATE TABLE statements for a dialect. All three
// files declare the same tables and columns (schema parity is tested); they
// differ only in types and in the MySQL generated columns that stand in for
// partial unique indexes.
func schemaStatements(d Dialect) ([]string, error) {
	var src string
	switch d {
	case MySQL:
		src = schemaMySQL
	case Postgres:
		src = schemaPostgres
	case SQLite:
		src = schemaSQLite
	default:
		return nil, fmt.Errorf("store: no schema for dialect %q", d)
	}
	return splitStatements(src)
}

// indexDef describes one index. MySQL cannot express a partial index, so the
// three "only some rows are unique" indexes name the stored generated column
// that stands in for the predicate (MySQLColumns); every other engine uses
// Columns plus Where directly.
type indexDef struct {
	Name    string
	Table   string
	Columns []string
	Unique  bool
	Where   string
	// MySQLColumns replaces Columns on MySQL; the generated columns they name
	// are declared in schema_mysql.sql.
	MySQLColumns []string
}

var schemaIndexes = []indexDef{
	{Name: "idx_sessions_expires", Table: "sessions", Columns: []string{"expires_at"}},
	{
		// A user may re-subscribe after deleting; only one live row per
		// (user, URL).
		Name: "uq_subscriptions_live", Table: "subscriptions",
		Columns: []string{"user_id", "url_key"}, Unique: true,
		Where: "deleted_at IS NULL", MySQLColumns: []string{"user_id", "live_url_key"},
	},
	{Name: "idx_subscriptions_url_key", Table: "subscriptions", Columns: []string{"url_key"}},
	{Name: "idx_channels_user", Table: "channels", Columns: []string{"user_id"}},
	{Name: "idx_notifications_cycle", Table: "notifications", Columns: []string{"cycle_id"}},
	{Name: "idx_delivery_groups_status", Table: "delivery_groups", Columns: []string{"status"}},
	{Name: "idx_deliveries_status", Table: "deliveries", Columns: []string{"status"}},
	// The admin drill-down lists one user's recent deliveries newest first.
	{Name: "idx_deliveries_user", Table: "deliveries", Columns: []string{"user_id", "id"}},
	{Name: "idx_audit_ts", Table: "audit_log", Columns: []string{"ts"}},
	// The settings page lists one user's linked accounts; the unique index on
	// (provider, user_id) cannot serve a lookup that does not name the provider.
	{Name: "idx_user_identities_user", Table: "user_identities", Columns: []string{"user_id"}},
	// Expired sign-in attempts are purged on the same schedule as sessions.
	{Name: "idx_oauth_flows_expires", Table: "oauth_flows", Columns: []string{"expires_at"}},
	// Disabled entries still claim their URL: re-enable instead of
	// re-submitting.
	{
		Name: "uq_catalog_entries_url_key", Table: "catalog_entries",
		Columns: []string{"url_key"}, Unique: true,
	},
	{Name: "idx_catalog_entries_category", Table: "catalog_entries", Columns: []string{"category_id"}},
	{Name: "idx_catalog_entries_enabled", Table: "catalog_entries", Columns: []string{"enabled", "id"}},
	// At most one pending proposal per URL, whatever its kind. Historical
	// approved/rejected rows may repeat a url_key.
	{
		Name: "uq_submissions_pending_url", Table: "submissions",
		Columns: []string{"url_key"}, Unique: true,
		Where: "status = 'pending'", MySQLColumns: []string{"pending_url_key"},
	},
	{Name: "idx_submissions_status", Table: "submissions", Columns: []string{"status", "id"}},
	{Name: "idx_submissions_user", Table: "submissions", Columns: []string{"submitted_by", "id"}},
	{Name: "idx_submissions_entry", Table: "submissions", Columns: []string{"entry_id"}},
	// Every pre-existing catalog row has preset_key='', so the index must be
	// partial; a plain unique index would reject the second manual entry.
	{
		Name: "uq_catalog_entries_preset_key", Table: "catalog_entries",
		Columns: []string{"preset_key"}, Unique: true,
		Where: "preset_key <> ''", MySQLColumns: []string{"preset_key_live"},
	},
}

// createIndexStatement renders the CREATE INDEX for a dialect. MySQL has no
// IF NOT EXISTS for indexes, so the caller checks existence first (ensureIndex)
// and this function never emits the clause.
func (def indexDef) statement(d Dialect) string {
	cols := def.Columns
	if d == MySQL && len(def.MySQLColumns) > 0 {
		cols = def.MySQLColumns
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	if def.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if d != MySQL {
		b.WriteString("IF NOT EXISTS ")
	}
	b.WriteString(def.Name)
	b.WriteString(" ON ")
	b.WriteString(def.Table)
	b.WriteString("(")
	b.WriteString(strings.Join(cols, ", "))
	b.WriteString(")")
	if def.Where != "" && d != MySQL {
		b.WriteString(" WHERE ")
		b.WriteString(def.Where)
	}
	return b.String()
}

// splitStatements breaks a schema file into single statements. database/sql
// sends one statement per call here: pgx's extended protocol rejects multiple
// statements, and per-statement execution is what makes a MySQL DDL error
// report the offending table instead of "syntax error near ...".
//
// The splitter understands '...' literals (with ” escapes) and -- / /* */
// comments, which is everything the schema files use.
func splitStatements(src string) ([]string, error) {
	var (
		out      []string
		cur      strings.Builder
		inSingle bool
	)
	flush := func() {
		stmt := strings.TrimSpace(cur.String())
		if stmt != "" {
			out = append(out, stmt)
		}
		cur.Reset()
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inSingle:
			cur.WriteByte(c)
			if c == '\'' {
				if i+1 < len(src) && src[i+1] == '\'' {
					cur.WriteByte(src[i+1])
					i++
				} else {
					inSingle = false
				}
			}
		case c == '\'':
			inSingle = true
			cur.WriteByte(c)
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			cur.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("store: unterminated block comment in schema")
			}
			i += 2 + end + 1
		case c == ';':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	if inSingle {
		return nil, fmt.Errorf("store: unterminated string literal in schema")
	}
	flush()
	return out, nil
}

// schemaMigrationStmt creates the version table. It is dialect-neutral on
// purpose: this is the one table every engine must create before it can even
// tell which version it is on.
const schemaMigrationStmt = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER NOT NULL PRIMARY KEY,
  applied_at TEXT NOT NULL
)`

// readSchemaVersion returns the version stored in schema_migrations. SQLite
// databases written by 1.1.x only have PRAGMA user_version, so the larger of
// the two wins and an upgrade keeps its data.
func readSchemaVersion(db *DB) (int, error) {
	if _, err := db.Exec(schemaMigrationStmt); err != nil {
		return 0, explain(schemaMigrationStmt, err)
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("webui: read schema version: %w", err)
	}
	if db.Dialect() == SQLite {
		var pragma int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&pragma); err != nil {
			return 0, fmt.Errorf("webui: read sqlite user_version: %w", err)
		}
		if pragma > version {
			version = pragma
		}
	}
	return version, nil
}

// recordVersion marks the schema as current. SQLite also keeps the legacy
// PRAGMA in sync so an older binary still recognises a newer database.
func recordVersion(db *DB) error {
	now := Now()
	if _, err := db.ExecIgnore(`INSERT INTO schema_migrations (version, applied_at) VALUES (?,?)`,
		schemaVersion, now); err != nil {
		return fmt.Errorf("webui: record schema version: %w", err)
	}
	if db.Dialect() == SQLite {
		// PRAGMA cannot be parameterized, but the value is a compile-time
		// constant.
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("webui: set sqlite user_version: %w", err)
		}
	}
	return nil
}

// columnExists reports whether table.column is present.
func columnExists(g *gateway, table, column string) (bool, error) {
	var count int
	switch g.Dialect() {
	case MySQL:
		err := g.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
			table, column).Scan(&count)
		return count > 0, err
	case Postgres:
		err := g.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			table, column).Scan(&count)
		return count > 0, err
	default:
		rows, err := g.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		found := false
		for rows.Next() {
			var (
				cid      int
				name     string
				typ      string
				notNull  int
				dflt     sql.NullString
				primaryK int
			)
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &primaryK); err != nil {
				return false, err
			}
			if name == column {
				found = true
			}
		}
		return found, rows.Err()
	}
}

// indexExists reports whether an index with this name already exists.
func indexExists(g *gateway, table, name string) (bool, error) {
	var count int
	switch g.Dialect() {
	case MySQL:
		err := g.QueryRow(`SELECT COUNT(*) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`,
			table, name).Scan(&count)
		return count > 0, err
	case Postgres:
		err := g.QueryRow(`SELECT COUNT(*) FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&count)
		return count > 0, err
	default:
		err := g.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`,
			name).Scan(&count)
		return count > 0, err
	}
}
