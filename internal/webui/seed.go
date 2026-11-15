//go:build webui

package webui

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"

	"feedwatch/internal/config"
	"feedwatch/internal/webui/store"
	"golang.org/x/crypto/bcrypt"
)

// SeedResult reports what the first-start bootstrap did. Password is non-empty
// only when an administrator was actually generated.
type SeedResult struct {
	Created  bool
	Username string
	Password string
}

// Seed creates the very first administrator plus the built-in example catalog.
//
// A fresh WebUI database is intentionally empty of real data: no subscriptions,
// no notification channels, no baseline notification. The administrator is an
// operations role (users, global settings, read-only data); anyone who wants to
// subscribe creates a regular user account and configures it from there. The
// example catalog is metadata (browsable templates), not a subscription: it is
// written so the subscription center is not blank on first login, and it never
// creates a feed_pool or user_seen row.
//
// The gate is "users table is empty", so this runs exactly once per database.
// Databases upgraded from an earlier version keep their existing users and
// therefore do not gain the examples; an administrator can import them.
func Seed(st *store.Store, log *slog.Logger) (SeedResult, error) {
	if log == nil {
		log = slog.Default()
	}
	var users int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		return SeedResult{}, fmt.Errorf("webui: count users: %w", err)
	}
	if users > 0 {
		return SeedResult{}, nil
	}

	password, err := randomToken(24)
	if err != nil {
		return SeedResult{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return SeedResult{}, fmt.Errorf("webui: hash admin password: %w", err)
	}

	tx, err := st.DB().Begin()
	if err != nil {
		return SeedResult{}, fmt.Errorf("webui: begin seed: %w", err)
	}
	defer tx.Rollback()

	now := store.Now()
	if _, err := tx.Exec(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, "admin", string(hash), "admin", 1, 1, now, now); err != nil {
		return SeedResult{}, fmt.Errorf("webui: create admin: %w", err)
	}
	if err := seedSettings(tx, now); err != nil {
		return SeedResult{}, err
	}
	if err := seedCatalogExamples(tx, now); err != nil {
		return SeedResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SeedResult{}, fmt.Errorf("webui: commit seed: %w", err)
	}

	// Printed exactly once, ever. The admin is forced to change it on first
	// login. Warn level so a raised log level cannot swallow the only copy.
	log.Warn("webui: initial administrator created (shown once)",
		"username", "admin", "password", password,
		"note", "change this password at first login; it is never shown again")
	return SeedResult{Created: true, Username: "admin", Password: password}, nil
}

// seedSettings writes the built-in defaults. config.yaml is deliberately not
// consulted: the WebUI variant has no configuration file, and an empty database
// must behave exactly like the documented defaults.
//
// notify.mode is absent on purpose: in the WebUI it is an account preference
// (users.notify_mode) set from the notification page, so a global row would be
// a value nothing reads. Databases seeded by earlier versions keep theirs and
// the settings page labels it as informational.
func seedSettings(tx *sql.Tx, now string) error {
	cfg := config.Default()
	// Compact durations ("30m", not "30m0s") so the stored value matches what
	// the settings page advertises as the default.
	settings := map[string]string{
		"poll.interval":         CompactDuration(cfg.Poll.Interval.D()),
		"poll.timeout":          CompactDuration(cfg.Poll.Timeout.D()),
		"poll.concurrency":      strconv.Itoa(cfg.Poll.Concurrency),
		"poll.request_spacing":  CompactDuration(cfg.Poll.RequestSpacing.D()),
		"notify.send_spacing":   CompactDuration(cfg.Notify.SendSpacing.D()),
		"notify.subject_prefix": cfg.Notify.SubjectPrefix,
		"http.timeout":          CompactDuration(cfg.HTTP.Timeout.D()),
		"http.user_agent":       cfg.HTTP.UserAgent,
		"http.retry_max":        strconv.Itoa(cfg.HTTP.RetryMax),
		"http.retry_base_delay": CompactDuration(cfg.HTTP.RetryBaseDelay.D()),
		"http.conditional":      strconv.FormatBool(cfg.HTTP.Conditional),
		"webui.cookie_secure":   "false",
		"webui.session_ttl":     "168h",
	}
	for k, v := range settings {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value, updated_at) VALUES (?,?,?)`, k, v, now); err != nil {
			return fmt.Errorf("webui: seed setting %q: %w", k, err)
		}
	}
	return nil
}

// seedCatalogExamples fills the subscription center on a brand-new database.
// Everything is INSERT OR IGNORE (categories by name, entries by url_key) so a
// repeated seed, or an administrator who already edited an example, is left
// alone. Only the three catalog tables are touched: a template must never turn
// into a real subscription or a polling-pool row.
func seedCatalogExamples(tx *sql.Tx, now string) error {
	for _, name := range ExampleCategoryNames() {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO catalog_categories (name, created_at, updated_at) VALUES (?,?,?)`,
			name, now, now); err != nil {
			return fmt.Errorf("webui: seed catalog category %q: %w", name, err)
		}
	}
	for _, e := range ExampleCatalog {
		normalized, key, err := CatalogURLKey(e.URL)
		if err != nil {
			return fmt.Errorf("webui: seed catalog entry %q: %w", e.Name, err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO catalog_entries
			(url, url_key, name, category_id, description, homepage, source_type, enabled, created_by, created_at, updated_at)
			VALUES (?,?,?,(SELECT id FROM catalog_categories WHERE name=?),?,?,'atom',1,NULL,?,?)`,
			normalized, key, e.Name, e.Category, e.Description, e.Homepage, now, now); err != nil {
			return fmt.Errorf("webui: seed catalog entry %q: %w", e.Name, err)
		}
	}
	return nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webui: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
