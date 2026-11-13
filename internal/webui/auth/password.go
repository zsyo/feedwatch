//go:build webui

package auth

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	ErrInactiveUser       = errors.New("auth: user is disabled")
)

// User is the subset of the users table the auth layer needs.
type User struct {
	ID                 int64
	Username           string
	PasswordHash       string
	Role               string
	IsActive           bool
	MustChangePassword bool
}

func IsAdmin(u User) bool { return u.Role == "admin" }

// HashPassword uses bcrypt with the library default cost.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(b), nil
}

var (
	dummyOnce  sync.Once
	dummyValue string
)

// dummyHash is a real bcrypt hash. Comparing against it for unknown usernames
// keeps the login timing indistinguishable from a wrong password, so the
// endpoint cannot be used to enumerate accounts. It is generated once at
// runtime rather than hard-coded, so it can never drift out of sync with the
// bcrypt format the library expects.
func dummyHash() string {
	dummyOnce.Do(func() {
		b, err := bcrypt.GenerateFromPassword([]byte("feedwatch-timing-equalizer"), bcrypt.DefaultCost)
		if err != nil {
			panic(err)
		}
		dummyValue = string(b)
	})
	return dummyValue
}

// VerifyPassword never short-circuits on an empty hash.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		hash = dummyHash()
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ChangePassword verifies the old password before replacing it. The API layer
// additionally clears must_change_password on success.
func (m *Manager) ChangePassword(userID int64, oldPassword, newPassword string) error {
	var hash string
	if err := m.db.QueryRow("SELECT password_hash FROM users WHERE id=?", userID).Scan(&hash); err != nil {
		return fmt.Errorf("auth: load user %d: %w", userID, err)
	}
	if !VerifyPassword(hash, oldPassword) {
		return ErrInvalidCredentials
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := m.db.Exec(
		"UPDATE users SET password_hash=?, must_change_password=0, updated_at=? WHERE id=?",
		newHash, nowStr(), userID,
	); err != nil {
		return fmt.Errorf("auth: update password: %w", err)
	}
	return nil
}
