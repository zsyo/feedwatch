//go:build webui

package store

import (
	"strings"
	"testing"
)

func TestRebindPlaceholders(t *testing.T) {
	cases := []struct {
		name    string
		dialect Dialect
		in      string
		want    string
	}{
		{
			name:    "sqlite untouched",
			dialect: SQLite,
			in:      "SELECT a FROM t WHERE b=? AND c=?",
			want:    "SELECT a FROM t WHERE b=? AND c=?",
		},
		{
			name:    "mysql untouched",
			dialect: MySQL,
			in:      "INSERT INTO t (a, b) VALUES (?,?)",
			want:    "INSERT INTO t (a, b) VALUES (?,?)",
		},
		{
			name:    "postgres numbers placeholders",
			dialect: Postgres,
			in:      "SELECT a FROM t WHERE b=? AND c=?",
			want:    "SELECT a FROM t WHERE b=$1 AND c=$2",
		},
		{
			name:    "postgres skips string literals",
			dialect: Postgres,
			in:      `SELECT * FROM t WHERE a='?' AND b=?`,
			want:    `SELECT * FROM t WHERE a='?' AND b=$1`,
		},
		{
			name:    "postgres skips doubled quotes inside a literal",
			dialect: Postgres,
			in:      `SELECT * FROM t WHERE a='it''s ?' AND b=?`,
			want:    `SELECT * FROM t WHERE a='it''s ?' AND b=$1`,
		},
		{
			name:    "postgres skips quoted identifiers",
			dialect: Postgres,
			in:      `SELECT "weird?col" FROM t WHERE a=?`,
			want:    `SELECT "weird?col" FROM t WHERE a=$1`,
		},
		{
			name:    "postgres keeps literals after a placeholder",
			dialect: Postgres,
			in:      `SELECT * FROM t WHERE a=? AND b='x'`,
			want:    `SELECT * FROM t WHERE a=$1 AND b='x'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dialect.rebind(tc.in); got != tc.want {
				t.Fatalf("rebind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestInsertColumnsParsesTarget(t *testing.T) {
	table, cols, err := insertColumns("settings (setting_key, value, updated_at) VALUES (?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	if table != "settings" {
		t.Fatalf("table = %q, want settings", table)
	}
	if strings.Join(cols, ",") != "setting_key,value,updated_at" {
		t.Fatalf("cols = %v", cols)
	}
}

func TestInsertIgnoreStatements(t *testing.T) {
	const stmt = "INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)"
	cases := map[Dialect]string{
		SQLite:   "INSERT OR IGNORE INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)",
		Postgres: "INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?) ON CONFLICT DO NOTHING",
		// MySQL must not use INSERT IGNORE: that would swallow foreign-key and
		// truncation errors too. A no-op assignment only absorbs duplicates.
		MySQL: "INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?) ON DUPLICATE KEY UPDATE url_key=url_key",
	}
	for dialect, want := range cases {
		got, err := dialect.insertIgnore(stmt)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", dialect, got, want)
		}
	}
}

func TestInsertReplaceStatements(t *testing.T) {
	const stmt = "INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)"
	cases := map[Dialect]string{
		SQLite:   "INSERT OR REPLACE INTO settings (setting_key, value, updated_at) VALUES (?,?,?)",
		MySQL:    "REPLACE INTO settings (setting_key, value, updated_at) VALUES (?,?,?)",
		Postgres: "INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?) ON CONFLICT (setting_key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
	}
	for dialect, want := range cases {
		got, err := dialect.insertReplace(stmt, "setting_key")
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", dialect, got, want)
		}
	}
}

func TestUpsertStatements(t *testing.T) {
	const stmt = "INSERT INTO channel_secrets (channel_id, ciphertext, updated_at) VALUES (?,?,?)"
	cases := map[Dialect]string{
		MySQL:    "INSERT INTO channel_secrets (channel_id, ciphertext, updated_at) VALUES (?,?,?) ON DUPLICATE KEY UPDATE ciphertext=VALUES(ciphertext), updated_at=VALUES(updated_at)",
		Postgres: "INSERT INTO channel_secrets (channel_id, ciphertext, updated_at) VALUES (?,?,?) ON CONFLICT (channel_id) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at",
		SQLite:   "INSERT INTO channel_secrets (channel_id, ciphertext, updated_at) VALUES (?,?,?) ON CONFLICT(channel_id) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at",
	}
	for dialect, want := range cases {
		got, err := dialect.upsert(stmt, "channel_id", "ciphertext", "updated_at")
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", dialect, got, want)
		}
	}
	if _, err := MySQL.upsert(stmt, "channel_id"); err == nil {
		t.Fatal("upsert without columns should fail")
	}
}

func TestInsertReturningID(t *testing.T) {
	const stmt = "INSERT INTO users (username) VALUES (?)"
	if stmt, returning, err := Postgres.insertReturningID(stmt); err != nil || !returning || !strings.HasSuffix(stmt, " RETURNING id") {
		t.Fatalf("postgres: stmt=%q returning=%v err=%v", stmt, returning, err)
	}
	for _, dialect := range []Dialect{SQLite, MySQL} {
		if stmt, returning, err := dialect.insertReturningID(stmt); err != nil || returning || strings.Contains(stmt, "RETURNING") {
			t.Fatalf("%s: stmt=%q returning=%v err=%v", dialect, stmt, returning, err)
		}
	}
	if _, _, err := SQLite.insertReturningID("SELECT 1"); err == nil {
		t.Fatal("a non-INSERT statement must be rejected")
	}
}
func TestParseDialect(t *testing.T) {
	cases := map[string]Dialect{
		"": SQLite, "sqlite": SQLite, "sqlite3": SQLite,
		"mysql": MySQL, "MariaDB": MySQL,
		"postgres": Postgres, "postgresql": Postgres, "pg": Postgres, "PGX": Postgres,
	}
	for raw, want := range cases {
		got, err := ParseDialect(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Errorf("ParseDialect(%q) = %s, want %s", raw, got, want)
		}
	}
	if _, err := ParseDialect("oracle"); err == nil {
		t.Fatal("unknown driver must fail")
	}
}
