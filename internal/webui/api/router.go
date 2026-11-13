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
	mux.Handle("GET /api/admin/pool", s.handle("admin", false, s.adminPool))
	mux.Handle("GET /api/admin/ratelimit", s.handle("admin", false, s.adminRateLimit))
	mux.Handle("GET /api/admin/audit", s.handle("admin", false, s.adminAudit))
	mux.Handle("GET /api/admin/settings", s.handle("admin", false, s.adminGetSettings))
	mux.Handle("PUT /api/admin/settings", s.handle("admin", false, s.adminPutSettings))

	// Unknown /api/* paths must be JSON 404s for every method. A single method-
	// less "/api/" pattern would overlap "GET /" without being a strict subset,
	// which Go 1.22 ServeMux rejects at registration time; method-specific
	// catch-alls keep the SPA fallback intact. HEAD is covered by "GET /api/".
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		mux.Handle(method+" /api/", s.handle("", true, s.apiNotFound))
	}

	// SPA / static fallback.
	mux.Handle("GET /", s.handle("", true, s.static))
	return mux
}
