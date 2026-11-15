//go:build webui

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/store"
)

// settingAllowRegistration is the site-wide switch behind both the sign-up form
// and "a third-party account we have never seen creates an account". It is read
// on every request rather than at startup: closing registrations must take
// effect immediately, without a restart.
const settingAllowRegistration = "webui.allow_registration"

// registrationEnabled treats a missing row as closed. Databases created before
// v8 never opted in, and a closed default is the safe reading of "nobody said
// yes yet".
func (s *Server) registrationEnabled() bool {
	var value string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`, settingAllowRegistration).Scan(&value); err != nil {
		return false
	}
	on, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && on
}

// setRegistrationEnabled writes the switch. An empty string would be a value
// nothing can parse, so only the two documented spellings are accepted.
func (s *Server) setRegistrationEnabled(on bool) error {
	value := "false"
	if on {
		value = "true"
	}
	_, err := s.db.Upsert(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
		"setting_key", []string{"value", "updated_at"}, settingAllowRegistration, value, store.Now())
	return err
}

const (
	minUsernameLen = 2
	maxUsernameLen = 32
	minPasswordLen = 8
)

// validateUsername keeps usernames typable and comparable. Whitespace and
// control characters are refused outright; everything else (including CJK) is
// allowed, because the login form compares exactly what was typed.
func validateUsername(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := len([]rune(name)); n < minUsernameLen || n > maxUsernameLen {
		return "", errors.New("用户名长度需在 2 到 32 个字符之间")
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("用户名不能包含空格或控制字符")
		}
	}
	return name, nil
}

// register creates a regular account from the public sign-up form. The new
// account is active immediately and signed in, exactly like a third-party
// sign-in that had to create its account: the only gate is the site switch.
func (s *Server) register(w http.ResponseWriter, r *http.Request, _ Ctx) {
	if !s.registrationEnabled() {
		fail(w, http.StatusForbidden, "registration_closed", "registration is closed on this instance")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	username, err := validateUsername(req.Username)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	if len(req.Password) < minPasswordLen {
		fail(w, http.StatusBadRequest, "weak_password", "密码至少需要 8 个字符")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	now := store.Now()
	// must_change_password stays 0: the user chose this password themselves, so
	// there is nothing temporary to force them to rotate.
	id, err := s.db.InsertID(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,'user',1,0,?,?)`, username, hash, now, now)
	if err != nil {
		if s.db.Dialect().IsUniqueViolation(err) {
			fail(w, http.StatusConflict, "duplicate_user", "该用户名已被占用")
			return
		}
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	u, err := s.auth.LoadUser(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	raw, sess, err := s.auth.CreateSession(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "session_failed", err.Error())
		return
	}
	http.SetCookie(w, s.auth.SessionCookie(raw))
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":                 publicUser(u),
		"csrf_token":           sess.CSRFToken,
		"must_change_password": u.MustChangePassword,
	})
}
