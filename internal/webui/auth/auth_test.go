//go:build webui

package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"feedwatch/internal/webui/store"
)

func newTestManager(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s.DB(), time.Hour, false), s.DB()
}

func insertUser(t *testing.T, db *sql.DB, username, password, role string, active bool) int64 {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	activeInt := 0
	if active {
		activeInt = 1
	}
	res, err := db.Exec(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, username, hash, role, activeInt, 1, store.Now(), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHashPasswordAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse" || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password was not bcrypt-hashed: %q", hash)
	}
	if !VerifyPassword(hash, "correct horse") || VerifyPassword(hash, "wrong") {
		t.Fatal("password verification is broken")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password must be rejected")
	}
	// Empty hash must still consume a bcrypt comparison (account enumeration).
	if VerifyPassword("", "anything") {
		t.Fatal("the timing equalizer hash must never validate")
	}
}

func TestAuthenticateIsUniformAndChecksActive(t *testing.T) {
	m, db := newTestManager(t)
	insertUser(t, db, "alice", "s3cret", "admin", true)
	insertUser(t, db, "disabled", "s3cret", "user", false)

	if u, err := m.Authenticate("alice", "s3cret"); err != nil || !IsAdmin(u) {
		t.Fatalf("valid login failed: %+v %v", u, err)
	}
	for _, tc := range []struct{ user, password string }{
		{"alice", "wrong"}, {"missing", "s3cret"}, {"", ""},
	} {
		if _, err := m.Authenticate(tc.user, tc.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q,%q) = %v, want ErrInvalidCredentials", tc.user, tc.password, err)
		}
	}
	if _, err := m.Authenticate("disabled", "s3cret"); !errors.Is(err, ErrInactiveUser) {
		t.Errorf("disabled user = %v, want ErrInactiveUser", err)
	}
}

func TestSessionLifecycleStoresOnlyHash(t *testing.T) {
	m, db := newTestManager(t)
	id := insertUser(t, db, "alice", "s3cret", "user", true)

	raw, s, err := m.CreateSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || s.CSRFToken == "" || s.TokenHash == raw {
		t.Fatal("raw token must differ from the stored hash")
	}
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM sessions").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != s.TokenHash || strings.Contains(stored, raw) {
		t.Fatalf("raw token leaked into SQLite: %q", stored)
	}

	got, u, err := m.LookupSession(raw)
	if err != nil || got.UserID != id || u.Username != "alice" {
		t.Fatalf("LookupSession = %+v %+v %v", got, u, err)
	}
	if _, _, err := m.LookupSession("not-a-token"); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("unknown token = %v", err)
	}
	if err := m.DeleteSession(raw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.LookupSession(raw); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("deleted session = %v", err)
	}
}

func TestExpiredSessionIsPurged(t *testing.T) {
	m, db := newTestManager(t)
	id := insertUser(t, db, "alice", "s3cret", "user", true)
	raw, _, err := m.CreateSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE sessions SET expires_at=?", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.LookupSession(raw); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired session = %v, want ErrSessionExpired", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expired session row was not removed: %d", count)
	}
}

func TestCookieAttributesAndCSRF(t *testing.T) {
	m, _ := newTestManager(t)
	secure := New(nil, time.Hour, true).SessionCookie("raw")
	c := m.SessionCookie("raw")
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Fatalf("unexpected cookie attributes: %+v", c)
	}
	if !secure.Secure || !secure.HttpOnly {
		t.Fatal("HTTPS reverse proxy must be able to enable Secure without losing HttpOnly")
	}
	if clr := m.ClearCookie(); clr.MaxAge != -1 || clr.Value != "" {
		t.Fatalf("clear cookie = %+v", clr)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/subscriptions", nil)
	r.Header.Set(CSRFHeaderName, "abc")
	if !CheckCSRF("abc", CSRFHeader(r)) || CheckCSRF("abc", "") || CheckCSRF("", "abc") {
		t.Fatal("CSRF comparison is wrong")
	}
	if !IsSafeMethod(httptest.NewRequest(http.MethodGet, "/", nil)) ||
		IsSafeMethod(httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("CSRF method classification is wrong")
	}
	if TokenFromRequest(r) != "" {
		t.Fatal("missing cookie should yield an empty token")
	}
}

func TestChangePasswordClearsMustChangeFlag(t *testing.T) {
	m, db := newTestManager(t)
	id := insertUser(t, db, "alice", "old-password", "user", true)
	if err := m.ChangePassword(id, "wrong", "new-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong old password = %v", err)
	}
	if err := m.ChangePassword(id, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if u, err := m.Authenticate("alice", "new-password"); err != nil || u.MustChangePassword {
		t.Fatalf("password change did not take effect: %+v %v", u, err)
	}
}
