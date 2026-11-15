//go:build webui

package store

import (
	"os"
	"strings"
	"testing"
)

// backendDSN reads the opt-in DSN for a network backend. These tests create and
// drop tables, so they only run when an operator points them at a scratch
// database:
//
//	FEEDWATCH_TEST_MYSQL_DSN='fw:fw@tcp(127.0.0.1:3306)/feedwatch_test?parseTime=false' \
//	FEEDWATCH_TEST_POSTGRES_DSN='postgres://fw:fw@127.0.0.1:5432/feedwatch_test?sslmode=disable' \
//	go test -tags webui -run TestNetworkBackend -v ./internal/webui/store/
func backendDSN(t *testing.T, dialect Dialect) string {
	t.Helper()
	key := "FEEDWATCH_TEST_" + strings.ToUpper(dialect.String()) + "_DSN"
	dsn := strings.TrimSpace(os.Getenv(key))
	if dsn == "" {
		t.Skipf("%s not set; skipping the %s integration test", key, dialect)
	}
	return dsn
}

// dropAllTables removes every table this schema owns, children first, so a
// previous failed run cannot block the next one.
func dropAllTables(t *testing.T, db *DB) {
	t.Helper()
	for _, table := range []string{
		"deliveries", "delivery_groups", "notifications", "user_seen", "seen_ids",
		"feed_pool", "channel_secrets", "channels", "subscriptions",
		"submissions", "catalog_entries", "catalog_categories", "catalog_preset_state",
		"audit_log", "cycles", "sessions", "settings", "users", "schema_migrations",
	} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}

// TestNetworkBackend exercises every statement shape that differs between the
// three dialects against a real server.
func TestNetworkBackend(t *testing.T) {
	for _, dialect := range []Dialect{MySQL, Postgres} {
		t.Run(dialect.String(), func(t *testing.T) {
			dsn := backendDSN(t, dialect)
			st, err := OpenConfig(Config{Dialect: dialect, DSN: dsn}, nil)
			if err != nil {
				t.Fatalf("OpenConfig: %v", err)
			}
			defer st.Close()
			db := st.DB()
			dropAllTables(t, db)
			if err := migrate(db); err != nil {
				t.Fatalf("migrate on a clean database: %v", err)
			}

			version, err := readSchemaVersion(db)
			if err != nil {
				t.Fatal(err)
			}
			if version != schemaVersion {
				t.Fatalf("schema version = %d, want %d", version, schemaVersion)
			}
			// Opening twice must not re-run anything destructive.
			if err := migrate(db); err != nil {
				t.Fatalf("migrate is not idempotent: %v", err)
			}

			now := Now()
			userID, err := db.InsertID(`INSERT INTO users
				(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?)`, "seed-admin", "hash", "admin", 1, 1, now, now)
			if err != nil {
				t.Fatalf("InsertID: %v", err)
			}
			if userID <= 0 {
				t.Fatalf("InsertID returned %d", userID)
			}

			// A duplicate username must be classified as a unique violation, not
			// as a generic error: the API turns it into 409.
			_, err = db.InsertID(`INSERT INTO users
				(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?)`, "seed-admin", "hash", "admin", 1, 1, now, now)
			if err == nil {
				t.Fatal("a duplicate username should fail")
			}
			if !dialect.IsUniqueViolation(err) {
				t.Fatalf("duplicate username not recognised as a unique violation: %v", err)
			}

			// insert-or-skip: the second attempt must report zero rows and leave
			// the stored value alone.
			if _, err := db.ExecIgnore(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
				"poll.interval", "30m", now); err != nil {
				t.Fatalf("ExecIgnore insert: %v", err)
			}
			res, err := db.ExecIgnore(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
				"poll.interval", "99m", now)
			if err != nil {
				t.Fatalf("ExecIgnore duplicate: %v", err)
			}
			if n, _ := res.RowsAffected(); n != 0 {
				t.Fatalf("ExecIgnore duplicate affected %d rows, want 0", n)
			}
			var stored string
			if err := db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`, "poll.interval").Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != "30m" {
				t.Fatalf("settings value = %q, want 30m", stored)
			}

			// upsert replaces the named columns.
			if _, err := db.Upsert(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
				"setting_key", []string{"value", "updated_at"}, "poll.interval", "45m", now); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			if err := db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`, "poll.interval").Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != "45m" {
				t.Fatalf("settings value after upsert = %q, want 45m", stored)
			}

			// replace overwrites the whole row.
			if _, err := db.ExecReplace(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
				"setting_key", "poll.interval", "60m", now); err != nil {
				t.Fatalf("ExecReplace: %v", err)
			}
			if err := db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`, "poll.interval").Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != "60m" {
				t.Fatalf("settings value after replace = %q, want 60m", stored)
			}

			// The partial unique index must allow one live row per URL and any
			// number of soft-deleted ones. This is the MySQL generated-column
			// workaround under test.
			insertSub := func(deleted any) error {
				_, err := db.Exec(`INSERT INTO subscriptions
					(user_id, url, url_key, display_name, source_type, filter_json, notify_policy, enabled, seeded_at, deleted_at)
					VALUES (?,?,?,?,?,?,?,?,?,?)`,
					userID, "https://example.com/a.atom", "key-a", "A", "atom", "{}", "on_update", 1, now, deleted)
				return err
			}
			if err := insertSub(nil); err != nil {
				t.Fatalf("first live subscription: %v", err)
			}
			if err := insertSub(nil); err == nil {
				t.Fatal("a second live subscription for the same URL should be rejected")
			}
			if err := insertSub(now); err != nil {
				t.Fatalf("a soft-deleted duplicate must be allowed: %v", err)
			}
			if err := insertSub(now); err != nil {
				t.Fatalf("a second soft-deleted duplicate must be allowed: %v", err)
			}

			// Unicode round-trip: utf8mb4 on MySQL, UTF-8 everywhere.
			const body = "发布说明：修复登录问题 ✅"
			if _, err := db.Exec(`INSERT INTO notifications
				(cycle_id, merge_key, kind, subject, body, html, markdown, created_at)
				VALUES (?,?,?,?,?,?,?,?)`, "cycle-1", "pending", "digest", "subject", body, "<p>x</p>", "md", now); err != nil {
				t.Fatalf("insert notification: %v", err)
			}
			var got string
			if err := db.QueryRow(`SELECT body FROM notifications WHERE cycle_id=?`, "cycle-1").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != body {
				t.Fatalf("notification body = %q, want %q", got, body)
			}
		})
	}
}
