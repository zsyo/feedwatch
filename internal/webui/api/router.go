//go:build webui

package api

import "net/http"

// routes registers every endpoint. Go 1.22+ ServeMux patterns carry the HTTP
// method and support {id} path values, so no third-party router is needed.
// More specific /api/... patterns always win over the "GET /" SPA fallback.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Public
	mux.Handle("POST /api/auth/login", s.handle("", true, s.login))
	mux.Handle("GET /api/meta", s.handle("", true, s.meta))

	// Authenticated, any role
	mux.Handle("GET /api/auth/me", s.handle("", false, s.me))
	mux.Handle("POST /api/auth/logout", s.handle("", false, s.logout))
	mux.Handle("POST /api/auth/password", s.handle("", false, s.changePassword))

	mux.Handle("GET /api/subscriptions", s.handle("", false, s.listSubscriptions))
	mux.Handle("POST /api/subscriptions", s.handle("", false, s.createSubscription))
	mux.Handle("PATCH /api/subscriptions/{id}", s.handle("", false, s.updateSubscription))
	mux.Handle("DELETE /api/subscriptions/{id}", s.handle("", false, s.deleteSubscription))

	mux.Handle("GET /api/channels", s.handle("", false, s.listChannels))
	mux.Handle("POST /api/channels", s.handle("", false, s.createChannel))
	mux.Handle("PATCH /api/channels/{id}", s.handle("", false, s.updateChannel))
	mux.Handle("DELETE /api/channels/{id}", s.handle("", false, s.deleteChannel))

	// Admin only
	mux.Handle("GET /api/admin/users", s.handle("admin", false, s.listUsers))
	mux.Handle("POST /api/admin/users", s.handle("admin", false, s.createUser))
	mux.Handle("PATCH /api/admin/users/{id}", s.handle("admin", false, s.updateUser))
	mux.Handle("DELETE /api/admin/users/{id}", s.handle("admin", false, s.deleteUser))
	mux.Handle("POST /api/admin/users/{id}/reset-password", s.handle("admin", false, s.resetPassword))
	mux.Handle("GET /api/admin/overview", s.handle("admin", false, s.adminOverview))

	// SPA / static fallback. Unknown /api/* paths are rejected inside static
	// rather than silently returning HTML.
	mux.Handle("GET /", s.handle("", true, s.static))
	return mux
}
