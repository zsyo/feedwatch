//go:build webui

// Package api implements the WebUI HTTP surface. Every handler lives behind
// the webui build tag, so the base binary never links net/http's server side.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/store"
)

type Ctx struct {
	RequestID string
	User      auth.User
	Session   auth.Session
}

type Server struct {
	db      *sql.DB
	auth    *auth.Manager
	assets  fs.FS
	log     *slog.Logger
	version string
	buildID string
	limiter *rateLimiter
}

func New(st *store.Store, am *auth.Manager, assets fs.FS, log *slog.Logger, version string) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		db: st.DB(), auth: am, assets: assets, log: log,
		version: version, buildID: readBuildID(assets), limiter: newRateLimiter(),
	}
}

// Handler builds the full middleware chain. Public routes (login, meta, static)
// skip session authentication; everything else requires a valid session.
func (s *Server) Handler() http.Handler { return s.routes() }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		// Nothing useful can be done once the response has started.
		return
	}
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func fail(w http.ResponseWriter, status int, code, message string) {
	var e apiError
	e.Error.Code = code
	e.Error.Message = message
	writeJSON(w, status, e)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}

func publicUser(u auth.User) map[string]any {
	return map[string]any{
		"id":                   u.ID,
		"username":             u.Username,
		"role":                 u.Role,
		"must_change_password": u.MustChangePassword,
	}
}

func parseID(r *http.Request, field string) (int64, error) {
	var id int64
	if _, err := fmt.Sscan(r.PathValue(field), &id); err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func isAPI(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/") }
