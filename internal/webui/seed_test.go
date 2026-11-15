//go:build webui

package webui

import (
	"path/filepath"
	"testing"

	"feedwatch/internal/config"
	"feedwatch/internal/webui/store"
	"golang.org/x/crypto/bcrypt"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The remote preset knobs are WebUI-only: they are seeded from the shared
// constants in this package, not from config.Default().
func TestSeedWritesPresetDefaults(t *testing.T) {
	s := openTestStore(t)
	if _, err := Seed(s, nil); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"presets.base_url": DefaultPresetBaseURL,
		"presets.timeout":  DefaultPresetTimeout,
	} {
		var got string
		if err := s.DB().QueryRow(`SELECT value FROM settings WHERE setting_key=?`, key).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("seeded %s = %q, want %q", key, got, want)
		}
	}
}

func TestSeedCreatesOnlyTheAdministrator(t *testing.T) {
	s := openTestStore(t)
	first, err := Seed(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Username != "admin" || first.Password == "" {
		t.Fatalf("admin was not generated: %+v", first)
	}

	var hash string
	if err := s.DB().QueryRow("SELECT password_hash FROM users WHERE username='admin'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(first.Password)); err != nil {
		t.Fatalf("password is not stored as a bcrypt hash: %v", err)
	}
	var mustChange int
	if err := s.DB().QueryRow("SELECT must_change_password FROM users WHERE username='admin'").Scan(&mustChange); err != nil {
		t.Fatal(err)
	}
	if mustChange != 1 {
		t.Fatal("first login must force a password change")
	}

	// A fresh WebUI database must be empty of real data apart from the admin
	// and the built-in global settings: no feeds, no channels, no baseline
	// notification. The example catalog is metadata, so it may be non-empty.
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"SELECT COUNT(*) FROM users", 1},
		{"SELECT COUNT(*) FROM subscriptions", 0},
		{"SELECT COUNT(*) FROM channels", 0},
		{"SELECT COUNT(*) FROM channel_secrets", 0},
		{"SELECT COUNT(*) FROM feed_pool", 0},
		{"SELECT COUNT(*) FROM notifications", 0},
		{"SELECT COUNT(*) FROM delivery_groups", 0},
		{"SELECT COUNT(*) FROM deliveries", 0},
		{"SELECT COUNT(*) FROM user_seen", 0},
		{"SELECT COUNT(*) FROM seen_ids", 0},
		{"SELECT COUNT(*) FROM catalog_categories", len(ExampleCategoryNames())},
		{"SELECT COUNT(*) FROM catalog_entries", len(ExampleCatalog)},
		{"SELECT COUNT(*) FROM submissions", 0},
	} {
		var got int
		if err := s.DB().QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}

	// Second start must be a no-op and must not print a second password.
	second, err := Seed(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Password != "" {
		t.Fatalf("second Seed must be a no-op, got %+v", second)
	}
}

// The example catalog is a set of templates. Seeding it must not create a
// subscription, a polling-pool row, or any baseline seen ID; otherwise a brand
// new user would replay historical releases on their first cycle.
func TestSeedCatalogExamplesAreTemplates(t *testing.T) {
	s := openTestStore(t)
	if _, err := Seed(s, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"SELECT COUNT(*) FROM catalog_entries WHERE enabled<>1 OR created_by IS NOT NULL", 0},
		{"SELECT COUNT(*) FROM catalog_entries WHERE url NOT LIKE 'https://%'", 0},
		{"SELECT COUNT(*) FROM subscriptions", 0},
		{"SELECT COUNT(*) FROM feed_pool", 0},
		{"SELECT COUNT(*) FROM user_seen", 0},
		{"SELECT COUNT(*) FROM seen_ids", 0},
	} {
		var got int
		if err := s.DB().QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}
	var rawURL, storedKey string
	if err := s.DB().QueryRow(`SELECT url, url_key FROM catalog_entries LIMIT 1`).Scan(&rawURL, &storedKey); err != nil {
		t.Fatal(err)
	}
	_, wantKey, err := URLKey(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if storedKey != wantKey {
		t.Fatalf("catalog url_key = %q, want sha256 of normalized URL %q", storedKey, wantKey)
	}

	// Seeding twice (or restarting) must not duplicate the examples.
	if _, err := Seed(s, nil); err != nil {
		t.Fatal(err)
	}
	var entries int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM catalog_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != len(ExampleCatalog) {
		t.Fatalf("catalog_entries after second Seed = %d, want %d", entries, len(ExampleCatalog))
	}
}

// An upgraded database already has users, so Seed is a no-op and the examples
// are not injected. README tells administrators to import them instead.
func TestSeedSkipsExamplesOnExistingDatabase(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.DB().Exec(
		"INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?,?,?,?,?)",
		"legacy", "hash", "admin", store.Now(), store.Now()); err != nil {
		t.Fatal(err)
	}
	result, err := Seed(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatal("Seed must not create a second administrator")
	}
	var entries int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM catalog_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 0 {
		t.Fatalf("upgraded database gained %d example entries", entries)
	}
}

func TestSeedWritesBuiltInDefaults(t *testing.T) {
	s := openTestStore(t)
	if _, err := Seed(s, nil); err != nil {
		t.Fatal(err)
	}
	defaults := config.Default()
	settings := map[string]string{}
	rows, err := s.DB().Query("SELECT setting_key, value FROM settings")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		settings[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"poll.interval":         CompactDuration(defaults.Poll.Interval.D()),
		"notify.subject_prefix": defaults.Notify.SubjectPrefix,
		"notify.send_spacing":   CompactDuration(defaults.Notify.SendSpacing.D()),
		"http.user_agent":       defaults.HTTP.UserAgent,
	} {
		if got := settings[key]; got != want {
			t.Errorf("settings[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestNormalizeURLDeduplicatesEquivalentSubscriptions(t *testing.T) {
	a, ka, err := URLKey("HTTPS://GitHub.com:443/golang/go/releases.atom?b=2&a=1#frag")
	if err != nil {
		t.Fatal(err)
	}
	_, kb, err := URLKey("https://github.com/golang/go/releases.atom?a=1&b=2")
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("equivalent URLs got different keys: %q vs %q (normalized %q)", ka, kb, a)
	}
	if _, _, err := URLKey("/relative/path"); err == nil {
		t.Fatal("relative URL must be rejected")
	}
}
