//go:build webui

package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"feedwatch/internal/config"
	"feedwatch/internal/webui/store"
	"golang.org/x/crypto/bcrypt"
)

func testSeedConfig() config.Config {
	cfg := config.Default()
	cfg.Feeds = []config.FeedConfig{
		{Name: "golang/go", Type: "atom", URL: "https://github.com/golang/go/releases.atom", Enabled: true},
		{Name: "rust-lang/rust", Type: "atom", URL: "https://github.com/rust-lang/rust/releases.atom", Enabled: true},
		{Name: "disabled", Type: "atom", URL: "https://example.invalid/feed.atom", Enabled: false},
	}
	cfg.Notify.Channels = []config.ChannelConfig{
		{
			Type: "email", Enabled: true,
			Email: config.EmailConfig{From: "FeedWatch <noreply@example.com>", To: []string{"me@example.com"}, APIKeyEnv: "RESEND_API_KEY"},
		},
		{
			Type: "telegram", Enabled: true,
			Options: map[string]string{"url": "https://api.telegram.org/bot${TG_TOKEN}/sendMessage", "token": "plaintext-secret"},
		},
	}
	return cfg
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLoadSeedConfigMissingFileUsesDefaults(t *testing.T) {
	cfg, err := LoadSeedConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("a missing seed config must not be fatal: %v", err)
	}
	if cfg.Poll.Interval.D() != config.Default().Poll.Interval.D() {
		t.Fatal("defaults were not applied")
	}
}

func TestLoadSeedConfigSkipsValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("feeds: []\nnotify:\n  channels: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadSeedConfig(path)
	if err != nil {
		t.Fatalf("WebUI must not run config.Validate on the DB-backed path: %v", err)
	}
	if len(cfg.Feeds) != 0 || len(cfg.Notify.Channels) != 0 {
		t.Fatalf("unexpected seeds: %+v", cfg)
	}
}

func TestSeedCreatesAdminAndImportsOnce(t *testing.T) {
	s := openTestStore(t)
	cfg := testSeedConfig()

	first, err := Seed(s, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Username != "admin" || first.Password == "" {
		t.Fatalf("admin was not generated: %+v", first)
	}
	if first.Feeds != 2 || first.Channels != 2 {
		t.Fatalf("seed counts = %d feeds / %d channels, want 2/2", first.Feeds, first.Channels)
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
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"SELECT COUNT(*) FROM users", 1},
		{"SELECT COUNT(*) FROM subscriptions", 2},
		{"SELECT COUNT(*) FROM channels", 2},
		{"SELECT COUNT(*) FROM feed_pool", 2},
		{"SELECT COUNT(*) FROM notifications WHERE kind='baseline'", 1},
		{"SELECT COUNT(*) FROM delivery_groups WHERE status='pending'", 2},
		{"SELECT COUNT(*) FROM deliveries WHERE kind='baseline'", 2},
	} {
		var got int
		if err := s.DB().QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}

	var markdown string
	if err := s.DB().QueryRow(`SELECT markdown FROM notifications WHERE kind='baseline'`).Scan(&markdown); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		UserID     int64   `json:"user_id"`
		ChannelIDs []int64 `json:"channel_ids"`
		Note       struct {
			Kind string `json:"Kind"`
		} `json:"note"`
	}
	if err := json.Unmarshal([]byte(markdown), &envelope); err != nil {
		t.Fatalf("baseline must carry a delivery envelope: %v (%q)", err, markdown)
	}
	if envelope.Note.Kind != "baseline" || len(envelope.ChannelIDs) != 2 {
		t.Fatalf("unexpected baseline envelope: %+v", envelope)
	}

	// A second start must not regenerate the password or duplicate any seed.
	cfg.Feeds = append(cfg.Feeds, config.FeedConfig{Name: "new", Type: "atom", URL: "https://example.com/a.atom", Enabled: true})
	second, err := Seed(s, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Password != "" {
		t.Fatalf("second Seed must be a no-op, got %+v", second)
	}
}

func TestSeedNeverPersistsPlaintextSecret(t *testing.T) {
	s := openTestStore(t)
	if _, err := Seed(s, testSeedConfig(), nil); err != nil {
		t.Fatal(err)
	}
	var raw string
	err := s.DB().QueryRow("SELECT config_json FROM channels WHERE type='telegram'").Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "plaintext-secret") {
		t.Fatalf("plaintext secret leaked into SQLite: %s", raw)
	}
	var spec ChannelSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Options["url"] != "https://api.telegram.org/bot${TG_TOKEN}/sendMessage" {
		t.Errorf("placeholder must survive verbatim, got %q", spec.Options["url"])
	}
	if spec.Options["token"] != "" {
		t.Errorf("plaintext token should be dropped, got %q", spec.Options["token"])
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
