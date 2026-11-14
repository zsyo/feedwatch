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

	// A fresh WebUI database must be empty apart from the admin and the
	// built-in global settings: no feeds, no channels, no baseline notification.
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

func TestSeedWritesBuiltInDefaults(t *testing.T) {
	s := openTestStore(t)
	if _, err := Seed(s, nil); err != nil {
		t.Fatal(err)
	}
	defaults := config.Default()
	settings := map[string]string{}
	rows, err := s.DB().Query("SELECT key, value FROM settings")
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
		"poll.interval":         defaults.Poll.Interval.String(),
		"notify.subject_prefix": defaults.Notify.SubjectPrefix,
		"notify.send_spacing":   defaults.Notify.SendSpacing.String(),
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
