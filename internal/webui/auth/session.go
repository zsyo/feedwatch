//go:build webui

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	SessionCookieName = "fw_session"
	CSRFHeaderName    = "X-CSRF-Token"
)

var (
	ErrSessionInvalid = errors.New("auth: missing or invalid session")
	ErrSessionExpired = errors.New("auth: session expired")
)

type Session struct {
	TokenHash  string
	UserID     int64
	CSRFToken  string
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// Manager owns session persistence. The raw token exists only in the browser's
// HttpOnly cookie and in the返回值 of CreateSession; SQLite stores its hash.
type Manager struct {
	db     *sql.DB
	ttl    time.Duration
	secure bool
}

func New(db *sql.DB, ttl time.Duration, secure bool) *Manager {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &Manager{db: db, ttl: ttl, secure: secure}
}

func (m *Manager) TTL() time.Duration { return m.ttl }

func nowStr() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Authenticate always performs a bcrypt comparison, even for unknown users, so
// login timing does not reveal which usernames exist.
func (m *Manager) Authenticate(username, password string) (User, error) {
	var u User
	var active, mustChange int
	err := m.db.QueryRow(`SELECT id, username, password_hash, role, is_active, must_change_password
		FROM users WHERE username=?`, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &active, &mustChange,
	)
	if errors.Is(err, sql.ErrNoRows) {
		VerifyPassword("", password)
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: load user: %w", err)
	}
	u.IsActive = active != 0
	u.MustChangePassword = mustChange != 0
	if !VerifyPassword(u.PasswordHash, password) {
		return User{}, ErrInvalidCredentials
	}
	if !u.IsActive {
		return User{}, ErrInactiveUser
	}
	return u, nil
}

// CreateSession returns the raw cookie value plus the stored session. A new
// CSRF token is bound to every session and is returned to the SPA via /api/auth/me.
func (m *Manager) CreateSession(userID int64) (string, Session, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	now := time.Now().UTC()
	s := Session{
		TokenHash:  tokenHash(raw),
		UserID:     userID,
		CSRFToken:  csrf,
		ExpiresAt:  now.Add(m.ttl),
		LastSeenAt: now,
	}
	if _, err := m.db.Exec(`INSERT INTO sessions
		(token_hash, user_id, csrf_token, expires_at, last_seen_at) VALUES (?,?,?,?,?)`,
		s.TokenHash, s.UserID, s.CSRFToken, s.ExpiresAt.Format(time.RFC3339Nano), nowStr(),
	); err != nil {
		return "", Session{}, fmt.Errorf("auth: create session: %w", err)
	}
	return raw, s, nil
}

// LookupSession validates the cookie token, checks expiry/active status and
// refreshes last_seen_at. Expired rows are removed immediately.
func (m *Manager) LookupSession(raw string) (Session, User, error) {
	if raw == "" {
		return Session{}, User{}, ErrSessionInvalid
	}
	hash := tokenHash(raw)
	var s Session
	var u User
	var expires, lastSeen string
	var active, mustChange int
	err := m.db.QueryRow(`SELECT s.token_hash, s.user_id, s.csrf_token, s.expires_at, s.last_seen_at,
		u.id, u.username, u.password_hash, u.role, u.is_active, u.must_change_password
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash=?`, hash).Scan(
		&s.TokenHash, &s.UserID, &s.CSRFToken, &expires, &lastSeen,
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &active, &mustChange,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrSessionInvalid
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("auth: load session: %w", err)
	}
	s.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return Session{}, User{}, fmt.Errorf("auth: parse session expiry: %w", err)
	}
	s.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
	u.IsActive = active != 0
	u.MustChangePassword = mustChange != 0
	if time.Now().UTC().After(s.ExpiresAt) {
		m.DeleteSession(raw)
		return Session{}, User{}, ErrSessionExpired
	}
	if !u.IsActive {
		return Session{}, User{}, ErrInactiveUser
	}
	if _, err := m.db.Exec("UPDATE sessions SET last_seen_at=? WHERE token_hash=?", nowStr(), hash); err != nil {
		return Session{}, User{}, fmt.Errorf("auth: touch session: %w", err)
	}
	return s, u, nil
}

func (m *Manager) DeleteSession(raw string) error {
	_, err := m.db.Exec("DELETE FROM sessions WHERE token_hash=?", tokenHash(raw))
	return err
}

func (m *Manager) PurgeExpired() (int64, error) {
	res, err := m.db.Exec("DELETE FROM sessions WHERE expires_at < ?", nowStr())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (m *Manager) SessionCookie(raw string) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().UTC().Add(m.ttl),
	}
}

func (m *Manager) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: m.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	}
}

func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (m *Manager) FromRequest(r *http.Request) (Session, User, error) {
	return m.LookupSession(TokenFromRequest(r))
}
