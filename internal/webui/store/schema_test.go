//go:build webui

package store

import (
	"strings"
	"testing"
)

// TestSchemaFilesAgree is the guard against the three DDL files drifting. It
// compares the table/column sets, ignoring the MySQL stored generated columns
// that stand in for partial unique indexes.
func TestSchemaFilesAgree(t *testing.T) {
	mysqlOnly := map[string]bool{
		"subscriptions.live_url_key":      true,
		"submissions.pending_url_key":     true,
		"catalog_entries.preset_key_live": true,
	}
	reference := tableColumns(t, SQLite)
	if len(reference) == 0 {
		t.Fatal("parsed no tables from the SQLite schema")
	}
	for _, dialect := range []Dialect{MySQL, Postgres} {
		got := tableColumns(t, dialect)
		for table, cols := range reference {
			other, ok := got[table]
			if !ok {
				t.Errorf("%s is missing table %s", dialect, table)
				continue
			}
			for col := range cols {
				if !other[col] {
					t.Errorf("%s: %s is missing column %s", dialect, table, col)
				}
			}
			for col := range other {
				if cols[col] {
					continue
				}
				if dialect == MySQL && mysqlOnly[table+"."+col] {
					continue
				}
				t.Errorf("%s: %s has extra column %s", dialect, table, col)
			}
		}
		for table := range got {
			if _, ok := reference[table]; !ok {
				t.Errorf("%s has an extra table %s", dialect, table)
			}
		}
	}
}

// tableColumns extracts table -> column names from one dialect's DDL. The
// schema files keep exactly one column per line, which is what makes this
// line-based parser trustworthy enough for a drift test.
func tableColumns(t *testing.T, dialect Dialect) map[string]map[string]bool {
	t.Helper()
	stmts, err := schemaStatements(dialect)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]map[string]bool)
	for _, stmt := range stmts {
		table := createTableName(stmt)
		if table == "" {
			continue
		}
		cols := make(map[string]bool)
		body := stmt[strings.IndexByte(stmt, '(')+1:]
		if end := strings.LastIndexByte(body, ')'); end >= 0 {
			body = body[:end]
		}
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			// Strip trailing comma and any inline comment.
			line = strings.TrimSuffix(line, ",")
			if i := strings.Index(line, " --"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "(") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch strings.ToUpper(fields[0]) {
			case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "KEY", "INDEX", "REFERENCES":
				continue
			}
			cols[strings.Trim(fields[0], "\"`")] = true
		}
		out[table] = cols
	}
	return out
}

func createTableName(stmt string) string {
	const prefix = "CREATE TABLE IF NOT EXISTS "
	if !strings.HasPrefix(stmt, prefix) {
		return ""
	}
	rest := stmt[len(prefix):]
	if i := strings.IndexAny(rest, " ("); i > 0 {
		return rest[:i]
	}
	return ""
}

func TestSplitStatements(t *testing.T) {
	src := `
-- leading comment with a ; semicolon
CREATE TABLE IF NOT EXISTS a (x TEXT NOT NULL DEFAULT ';');
/* block ; comment */
CREATE TABLE IF NOT EXISTS b (y TEXT);
`
	stmts, err := splitStatements(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("got %d statements: %q", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "DEFAULT ';'") {
		t.Fatalf("semicolon inside a literal was split: %q", stmts[0])
	}
	if _, err := splitStatements("CREATE TABLE t (a TEXT DEFAULT 'x);"); err == nil {
		t.Fatal("unterminated literal must be reported")
	}
}

func TestIndexStatementMatchesDialect(t *testing.T) {
	live := indexDef{
		Name: "uq_subscriptions_live", Table: "subscriptions",
		Columns: []string{"user_id", "url_key"}, Unique: true,
		Where: "deleted_at IS NULL", MySQLColumns: []string{"user_id", "live_url_key"},
	}
	const want = "CREATE UNIQUE INDEX IF NOT EXISTS uq_subscriptions_live ON subscriptions(user_id, url_key) WHERE deleted_at IS NULL"
	if got := live.statement(SQLite); got != want {
		t.Errorf("sqlite: %q", got)
	}
	if got := live.statement(Postgres); got != want {
		t.Errorf("postgres: %q", got)
	}
	// MySQL cannot express the predicate, so it indexes the generated column.
	if got := live.statement(MySQL); got != "CREATE UNIQUE INDEX uq_subscriptions_live ON subscriptions(user_id, live_url_key)" {
		t.Errorf("mysql: %q", got)
	}
}

func TestEveryDialectHasASchema(t *testing.T) {
	for _, dialect := range []Dialect{SQLite, MySQL, Postgres} {
		stmts, err := schemaStatements(dialect)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		if len(stmts) < 21 {
			t.Fatalf("%s declares only %d tables", dialect, len(stmts))
		}
	}
	if _, err := schemaStatements(Dialect("oracle")); err == nil {
		t.Fatal("unknown dialect must fail")
	}
}
