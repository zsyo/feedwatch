//go:build webui

package store

import (
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
		"users", "sessions", "subscriptions", "channels", "feed_pool",
		"seen_ids", "user_seen", "notifications", "delivery_groups",
		"deliveries", "cycles", "settings", "audit_log",
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
