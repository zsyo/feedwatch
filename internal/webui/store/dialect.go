//go:build webui

package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// Dialect names one of the three SQL engines this package speaks. Everything
// that differs between them is funnelled through this file: placeholder syntax,
// the upsert/ignore shapes, auto-increment ids and unique-violation errors. The
// rest of the WebUI keeps writing ordinary SQL, so a new query is portable by
// default and only the handful of divergent statements below need attention.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	MySQL    Dialect = "mysql"
	Postgres Dialect = "postgres"
)

// ParseDialect accepts the driver names operators actually type, including the
// common aliases, and maps them onto the three supported engines.
func ParseDialect(raw string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "sqlite", "sqlite3":
		return SQLite, nil
	case "mysql", "mariadb":
		return MySQL, nil
	case "postgres", "postgresql", "pg", "pgx":
		return Postgres, nil
	}
	return "", fmt.Errorf("store: unknown database driver %q (want sqlite, mysql or postgres)", raw)
}

// IsNetwork reports whether the dialect lives behind a TCP connection, i.e.
// whether the data directory, WAL and single-connection PRAGMAs still apply.
func (d Dialect) IsNetwork() bool { return d == MySQL || d == Postgres }

func (d Dialect) String() string { return string(d) }

// rebind rewrites the positional "?" placeholders used throughout this package
// into the $1..$n form PostgreSQL expects. Quoted literals and quoted
// identifiers are skipped so a "?" inside them stays data. SQLite and MySQL
// keep the statement untouched.
func (d Dialect) rebind(query string) string {
	if d != Postgres || !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case inSingle:
			b.WriteByte(c)
			if c == '\'' {
				// '' is an escaped quote inside a literal, not the end of it.
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte(query[i+1])
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
			b.WriteByte(c)
		case c == '"':
			inDouble = true
			b.WriteByte(c)
		case c == '?':
			n++
			b.WriteString("$")
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// insertPrefix is the one spelling every caller uses. The dialect-specific
// "ignore"/"replace"/"returning" variants are built from it, so call sites stay
// plain SQL and the divergence stays in this file.
const insertPrefix = "INSERT INTO "

// insertBody strips the leading INSERT INTO so the per-dialect builders can
// re-emit it with their own prefix and suffix.
func insertBody(stmt string) (string, error) {
	if len(stmt) < len(insertPrefix) || !strings.EqualFold(stmt[:len(insertPrefix)], insertPrefix) {
		return "", fmt.Errorf("store: statement must start with %q, got %q", insertPrefix, stmt)
	}
	return stmt[len(insertPrefix):], nil
}

// insertColumns parses the column list of an INSERT so the upsert builders can
// name the conflict target and the columns taken from the proposed row.
// "table (a, b) VALUES ..." yields ("table", ["a", "b"]).
func insertColumns(body string) (string, []string, error) {
	open := strings.IndexByte(body, '(')
	if open < 0 {
		return "", nil, fmt.Errorf("store: INSERT has no column list: %q", body)
	}
	closeAt := strings.IndexByte(body[open:], ')')
	if closeAt < 0 {
		return "", nil, fmt.Errorf("store: INSERT has an unterminated column list: %q", body)
	}
	table := strings.TrimSpace(body[:open])
	raw := body[open+1 : open+closeAt]
	cols := make([]string, 0, 8)
	for _, field := range strings.Split(raw, ",") {
		name := strings.Trim(strings.TrimSpace(field), "\"`")
		if name != "" {
			cols = append(cols, name)
		}
	}
	if table == "" || len(cols) == 0 {
		return "", nil, fmt.Errorf("store: cannot parse INSERT target: %q", body)
	}
	return table, cols, nil
}

// insertIgnore renders "insert, but an existing row wins".
func (d Dialect) insertIgnore(stmt string) (string, error) {
	body, err := insertBody(stmt)
	if err != nil {
		return "", err
	}
	switch d {
	case MySQL:
		// Deliberately NOT "INSERT IGNORE": that downgrades every error to a
		// warning, so a foreign-key or truncation problem would silently skip
		// the row. A no-op ON DUPLICATE KEY UPDATE only absorbs duplicate keys.
		_, cols, err := insertColumns(body)
		if err != nil {
			return "", err
		}
		return insertPrefix + body + " ON DUPLICATE KEY UPDATE " + cols[0] + "=" + cols[0], nil
	case Postgres:
		return insertPrefix + body + " ON CONFLICT DO NOTHING", nil
	default:
		return "INSERT OR IGNORE INTO " + body, nil
	}
}

// insertReplace renders "insert, and the proposed row overwrites the stored one".
// conflictKey names the unique column that identifies the row.
func (d Dialect) insertReplace(stmt, conflictKey string) (string, error) {
	body, err := insertBody(stmt)
	if err != nil {
		return "", err
	}
	switch d {
	case MySQL:
		// REPLACE deletes then inserts; that is only safe because the two
		// callers (settings, cycles) are leaves with no child rows.
		return "REPLACE INTO " + body, nil
	case Postgres:
		_, cols, err := insertColumns(body)
		if err != nil {
			return "", err
		}
		sets := make([]string, 0, len(cols))
		for _, c := range cols {
			if c == conflictKey {
				continue
			}
			sets = append(sets, c+"=excluded."+c)
		}
		return insertPrefix + body + " ON CONFLICT (" + conflictKey + ") DO UPDATE SET " + strings.Join(sets, ", "), nil
	default:
		return "INSERT OR REPLACE INTO " + body, nil
	}
}

// upsert renders "insert, and the proposed values replace these columns".
func (d Dialect) upsert(stmt, conflictKey string, updateCols ...string) (string, error) {
	body, err := insertBody(stmt)
	if err != nil {
		return "", err
	}
	if len(updateCols) == 0 {
		return "", fmt.Errorf("store: upsert needs at least one column to update")
	}
	sets := make([]string, 0, len(updateCols))
	for _, c := range updateCols {
		if d == MySQL {
			sets = append(sets, c+"=VALUES("+c+")")
		} else {
			sets = append(sets, c+"=excluded."+c)
		}
	}
	switch d {
	case MySQL:
		return insertPrefix + body + " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", "), nil
	case Postgres:
		return insertPrefix + body + " ON CONFLICT (" + conflictKey + ") DO UPDATE SET " + strings.Join(sets, ", "), nil
	default:
		return insertPrefix + body + " ON CONFLICT(" + conflictKey + ") DO UPDATE SET " + strings.Join(sets, ", "), nil
	}
}

// insertReturningID renders an INSERT that reports the new auto-increment id.
// SQLite and MySQL answer through LastInsertId; PostgreSQL needs RETURNING.
func (d Dialect) insertReturningID(stmt string) (string, bool, error) {
	body, err := insertBody(stmt)
	if err != nil {
		return "", false, err
	}
	if d == Postgres {
		return insertPrefix + body + " RETURNING id", true, nil
	}
	return insertPrefix + body, false, nil
}

// IsUniqueViolation reports whether err is a unique/primary-key conflict, so
// callers can answer 409 instead of 500. Each driver words this differently
// ("UNIQUE constraint failed", "Duplicate entry", "duplicate key value
// violates unique constraint"), hence the per-driver check plus a textual
// fallback for wrapped or translated errors.
func (d Dialect) IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	switch d {
	case MySQL:
		var me *mysql.MySQLError
		if errors.As(err, &me) {
			return me.Number == 1062
		}
	case Postgres:
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return pe.Code == "23505"
		}
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE CONSTRAINT") ||
		strings.Contains(msg, "DUPLICATE ENTRY") ||
		strings.Contains(msg, "DUPLICATE KEY")
}
