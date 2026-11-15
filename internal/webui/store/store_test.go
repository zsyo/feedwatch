//go:build webui

package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMigratesEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedwatch.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{
		"users", "sessions", "subscriptions", "channels", "channel_secrets", "feed_pool",
		"seen_ids", "user_seen", "notifications", "delivery_groups",
		"deliveries", "cycles", "settings", "audit_log",
		"catalog_categories", "catalog_entries", "submissions", "catalog_preset_state",
	} {
		var name string
		err := s.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
	var mode string
	if err := s.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := s.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedwatch.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?,?,?,?,?)",
		"alice", "hash", "admin", Now(), Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path, nil)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	var count int
	if err := again.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reopening wiped data: users = %d", count)
	}
}

func TestOpenUpgradesVersionOneDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedwatch.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?,?,?,?,?)",
		"alice", "hash", "admin", Now(), Now(),
	); err != nil {
		t.Fatal(err)
	}
	// Simulate a database created by the previous release: same tables, no
	// channel_secrets, user_version = 1.
	if _, err := s.DB().Exec(`DROP TABLE channel_secrets`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open(v1 database): %v", err)
	}
	defer again.Close()
	var version int
	if err := again.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	var name string
	if err := again.DB().QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='channel_secrets'").Scan(&name); err != nil {
		t.Fatalf("channel_secrets was not added: %v", err)
	}
	var users int
	if err := again.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("upgrade lost rows: users = %d", users)
	}
}

// A database created before the timezone column existed must gain it on open,
// keep its rows, and land on the current version.
func TestOpenAddsTimezoneColumnToOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedwatch.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?,?,?,?,?)",
		"alice", "hash", "admin", Now(), Now(),
	); err != nil {
		t.Fatal(err)
	}
	// Rebuild the previous release's shape: no timezone column, user_version = 2.
	if _, err := s.DB().Exec(`ALTER TABLE users DROP COLUMN timezone`); err != nil {
		t.Fatalf("simulate v2 schema: %v", err)
	}
	if _, err := s.DB().Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open(v2 database): %v", err)
	}
	defer again.Close()
	var version int
	if err := again.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	var tz string
	if err := again.DB().QueryRow("SELECT timezone FROM users WHERE username='alice'").Scan(&tz); err != nil {
		t.Fatalf("timezone column was not added: %v", err)
	}
	if tz != "" {
		t.Fatalf("existing row should default to the server zone, got %q", tz)
	}
	if _, err := again.DB().Exec("UPDATE users SET timezone='Europe/Berlin' WHERE username='alice'"); err != nil {
		t.Fatalf("new column is not writable: %v", err)
	}
}

// A v4 database has no catalog at all. Reopening it must add the three new
// tables, keep existing rows, and land on the current version.
func TestOpenUpgradesToVersion5CatalogTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedwatch.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?,?,?,?,?)",
		"alice", "hash", "admin", Now(), Now(),
	); err != nil {
		t.Fatal(err)
	}
	// Drop children before the tables they reference, then pretend this is v4.
	for _, stmt := range []string{
		`DROP TABLE submissions`,
		`DROP TABLE catalog_entries`,
		`DROP TABLE catalog_categories`,
		`PRAGMA user_version = 4`,
	} {
		if _, err := s.DB().Exec(stmt); err != nil {
			t.Fatalf("simulate v4 schema (%s): %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open(v4 database): %v", err)
	}
	defer again.Close()
	var version int
	if err := again.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{"catalog_categories", "catalog_entries", "submissions"} {
		var name string
		if err := again.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil {
			t.Errorf("table %s was not added: %v", table, err)
		}
	}
	var users int
	if err := again.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("upgrade lost rows: users = %d", users)
	}
}

// A v5 database has the catalog but none of the remote-preset columns, state
// table or partial index. Reopening it must add all of them, keep existing
// rows, and land on the current version.
func TestOpenUpgradesToVersion6PresetColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedwatch.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := Now()
	if _, err := s.DB().Exec(
		`INSERT INTO catalog_entries (url, url_key, name, created_at, updated_at)
		 VALUES (?,?,?,?,?)`, "https://example.com/a.atom", "key-a", "A", now, now); err != nil {
		t.Fatal(err)
	}
	// Rewind to the v5 shape. The partial index references preset_key, so it
	// has to go before the column can be dropped.
	for _, stmt := range []string{
		`DROP INDEX uq_catalog_entries_preset_key`,
		`ALTER TABLE catalog_entries DROP COLUMN preset_key`,
		`ALTER TABLE catalog_entries DROP COLUMN preset_version`,
		`ALTER TABLE catalog_entries DROP COLUMN preset_edited`,
		`DROP TABLE catalog_preset_state`,
		`PRAGMA user_version = 5`,
	} {
		if _, err := s.DB().Exec(stmt); err != nil {
			t.Fatalf("simulate v5 schema (%s): %v", stmt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open(v5 database): %v", err)
	}
	defer again.Close()

	var version int
	if err := again.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, col := range []string{"preset_key", "preset_version", "preset_edited"} {
		var name string
		if err := again.DB().QueryRow(
			`SELECT name FROM pragma_table_info('catalog_entries') WHERE name=?`, col,
		).Scan(&name); err != nil {
			t.Errorf("column %s was not added: %v", col, err)
		}
	}
	var key string
	var ver, edited int
	if err := again.DB().QueryRow(
		`SELECT preset_key, preset_version, preset_edited FROM catalog_entries WHERE url_key='key-a'`,
	).Scan(&key, &ver, &edited); err != nil {
		t.Fatal(err)
	}
	if key != "" || ver != 0 || edited != 0 {
		t.Fatalf("old row defaults = (%q,%d,%d), want (\"\",0,0)", key, ver, edited)
	}
	for _, obj := range []struct {
		kind, name string
	}{
		{"table", "catalog_preset_state"},
		{"index", "uq_catalog_entries_preset_key"},
	} {
		var name string
		if err := again.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type=? AND name=?", obj.kind, obj.name,
		).Scan(&name); err != nil {
			t.Errorf("%s %s missing: %v", obj.kind, obj.name, err)
		}
	}

	// The index is partial: any number of local rows may keep preset_key='',
	// but two rows cannot claim the same remote preset.
	if _, err := again.DB().Exec(
		`INSERT INTO catalog_entries (url, url_key, name, created_at, updated_at)
		 VALUES ('https://example.com/b.atom','key-b','B',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := again.DB().Exec(
		`UPDATE catalog_entries SET preset_key='lang-nodejs' WHERE url_key IN ('key-a','key-b')`); err == nil {
		t.Error("partial unique index did not reject duplicate preset_key")
	}
}

// Deleting a category must turn every referencing entry AND pending proposal
// into "uncategorized" in one step, via ON DELETE SET NULL.
func TestCatalogForeignKeySetNullOnDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := Now()
	if _, err := s.DB().Exec(
		`INSERT INTO catalog_categories (name, created_at, updated_at) VALUES (?,?,?)`,
		"tools", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO catalog_entries (url, url_key, name, category_id, created_at, updated_at)
		 VALUES (?,?,?,?,?,?)`, "https://example.com/a.atom", "key-a", "A", 1, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO submissions (kind, status, url, url_key, name, category_id, created_at, updated_at)
		 VALUES ('new','pending',?,?,?,?,?,?)`, "https://example.com/b.atom", "key-b", "B", 1, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM catalog_categories WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var entryCat, submissionCat sql.NullInt64
	if err := s.DB().QueryRow(`SELECT category_id FROM catalog_entries WHERE id=1`).Scan(&entryCat); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT category_id FROM submissions WHERE id=1`).Scan(&submissionCat); err != nil {
		t.Fatal(err)
	}
	if entryCat.Valid || submissionCat.Valid {
		t.Fatalf("category was not cleared: entry=%+v submission=%+v", entryCat, submissionCat)
	}
}

// The pending-URL index is partial: only live proposals are deduplicated, so a
// rejected proposal does not block a later resubmission of the same URL.
func TestSubmissionsPendingURLIndexIsPartial(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := Now()
	insert := func(status string) error {
		_, err := s.DB().Exec(
			`INSERT INTO submissions (kind, status, url, url_key, name, created_at, updated_at)
			 VALUES ('new',?,?,?,?,?,?)`, status, "https://example.com/a.atom", "key-a", "A", now, now)
		return err
	}
	if err := insert("pending"); err != nil {
		t.Fatal(err)
	}
	if err := insert("pending"); err == nil {
		t.Fatal("a second pending proposal for the same URL should be rejected")
	}
	if _, err := s.DB().Exec(`UPDATE submissions SET status='rejected' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := insert("pending"); err != nil {
		t.Fatalf("a rejected proposal must not block resubmission: %v", err)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.DB().Exec(
		"INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at, last_seen_at) VALUES (?,?,?,?,?)",
		"deadbeef", 999, "csrf", Now(), Now(),
	)
	if err == nil {
		t.Fatal("inserting a session for a missing user should fail")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
		t.Fatalf("want a foreign key error, got %v", err)
	}
}
