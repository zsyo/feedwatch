//go:build webui

package api

import (
	"errors"
	"net/http"

	"feedwatch/internal/webui/auth"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request, _ Ctx) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := s.auth.Authenticate(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInactiveUser) {
			fail(w, http.StatusForbidden, "user_disabled", err.Error())
			return
		}
		fail(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	raw, sess, err := s.auth.CreateSession(u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "session_failed", err.Error())
		return
	}
	http.SetCookie(w, s.auth.SessionCookie(raw))
	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 publicUser(u),
		"csrf_token":           sess.CSRFToken,
		"must_change_password": u.MustChangePassword,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ Ctx) {
	if raw := auth.TokenFromRequest(r); raw != "" {
		if err := s.auth.DeleteSession(raw); err != nil {
			s.log.Warn("webui: delete session failed", "error", err)
		}
	}
	http.SetCookie(w, s.auth.ClearCookie())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, c Ctx) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 publicUser(c.User),
		"csrf_token":           c.Session.CSRFToken,
		"must_change_password": c.User.MustChangePassword,
	})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, c Ctx) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(req.NewPassword) < 8 {
		fail(w, http.StatusBadRequest, "weak_password", "new password must be at least 8 characters")
		return
	}
	if err := s.auth.ChangePassword(c.User.ID, req.OldPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			fail(w, http.StatusUnauthorized, "invalid_credentials", "old password is wrong")
			return
		}
		fail(w, http.StatusInternalServerError, "password_failed", err.Error())
		return
	}
	// Invalidate every session of this user, including the current one.
	if _, err := s.db.Exec("DELETE FROM sessions WHERE user_id=?", c.User.ID); err != nil {
		s.log.Warn("webui: invalidate sessions failed", "error", err)
	}
	http.SetCookie(w, s.auth.ClearCookie())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
