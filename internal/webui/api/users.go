//go:build webui

package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/store"
)

type managedUser struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	Role               string `json:"role"`
	IsActive           bool   `json:"is_active"`
	MustChangePassword bool   `json:"must_change_password"`
	CreatedAt          string `json:"created_at"`
}

func (s *Server) listUsers(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	rows, err := s.db.Query(`SELECT id, username, role, is_active, must_change_password, created_at
		FROM users ORDER BY id`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]managedUser, 0)
	for rows.Next() {
		var u managedUser
		var active, must int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &active, &must, &u.CreatedAt); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		u.IsActive, u.MustChangePassword = active != 0, must != 0
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, _ Ctx) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "bad_request", "username and password are required")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if req.Role != "admin" && req.Role != "user" {
		fail(w, http.StatusBadRequest, "invalid_role", "role must be admin or user")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	now := store.Now()
	res, err := s.db.Exec(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, req.Username, hash, req.Role, 1, 1, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			fail(w, http.StatusConflict, "duplicate_user", "username already exists")
			return
		}
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var req struct {
		Role     *string `json:"role"`
		IsActive *bool   `json:"is_active"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	sets, args := make([]string, 0, 2), make([]any, 0, 4)
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "user" {
			fail(w, http.StatusBadRequest, "invalid_role", "role must be admin or user")
			return
		}
		sets, args = append(sets, "role=?"), append(args, *req.Role)
	}
	if req.IsActive != nil {
		sets, args = append(sets, "is_active=?"), append(args, boolToInt(*req.IsActive))
	}
	if len(sets) == 0 {
		fail(w, http.StatusBadRequest, "empty_patch", "no fields to update")
		return
	}
	if id == c.User.ID && ((req.IsActive != nil && !*req.IsActive) || (req.Role != nil && *req.Role != "admin")) {
		fail(w, http.StatusBadRequest, "self_lockout", "cannot demote or disable your own account")
		return
	}
	demoting := (req.Role != nil && *req.Role != "admin") || (req.IsActive != nil && !*req.IsActive)
	if demoting {
		var admins int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND is_active=1 AND id<>?`, id).Scan(&admins); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if admins == 0 {
			fail(w, http.StatusBadRequest, "last_admin", "cannot remove the last active administrator")
			return
		}
	}
	args = append(args, store.Now(), id)
	res, err := s.db.Exec(`UPDATE users SET `+strings.Join(sets, ", ")+`, updated_at=? WHERE id=?`, args...)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "user")
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if id == c.User.ID {
		fail(w, http.StatusBadRequest, "self_lockout", "cannot delete your own account")
		return
	}
	var admins int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND is_active=1 AND id<>?`, id).Scan(&admins); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var role string
	if err := s.db.QueryRow(`SELECT role FROM users WHERE id=?`, id).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if role == "admin" && admins == 0 {
		fail(w, http.StatusBadRequest, "last_admin", "cannot delete the last active administrator")
		return
	}
	res, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "user")
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, _ Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	password, err := generatePassword()
	if err != nil {
		fail(w, http.StatusInternalServerError, "password_failed", err.Error())
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		fail(w, http.StatusInternalServerError, "password_failed", err.Error())
		return
	}
	res, err := s.db.Exec(`UPDATE users SET password_hash=?, must_change_password=1, updated_at=? WHERE id=?`,
		hash, store.Now(), id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id); err != nil {
		s.log.Warn("webui: invalidate sessions failed", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"password": password, "note": "shown once; user must change it at next login"})
}

func generatePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
