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

// Seed creates the very first administrator, and nothing else.
//
// A fresh WebUI database is intentionally empty: no subscriptions, no
// notification channels, no baseline notification. The administrator is an
// operations role (users, global settings, read-only data); anyone who wants to
// subscribe creates a regular user account and configures it from there.
//
// The gate is "users table is empty", so this runs exactly once per database.
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
func seedSettings(tx *sql.Tx, now string) error {
	cfg := config.Default()
	// Compact durations ("30m", not "30m0s") so the stored value matches what
	// the settings page advertises as the default.
	settings := map[string]string{
		"poll.interval":         CompactDuration(cfg.Poll.Interval.D()),
		"poll.timeout":          CompactDuration(cfg.Poll.Timeout.D()),
		"poll.concurrency":      strconv.Itoa(cfg.Poll.Concurrency),
		"poll.request_spacing":  CompactDuration(cfg.Poll.RequestSpacing.D()),
		"notify.mode":           cfg.Notify.Mode,
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

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webui: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
