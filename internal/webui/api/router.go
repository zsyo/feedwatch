//go:build webui

package api

import "net/http"

// routes registers every endpoint. Go 1.22+ ServeMux patterns carry the HTTP
// method and support {id} path values, so no third-party router is needed.
// More specific /api/... patterns always win over the "GET /" SPA fallback.
//
// Access levels: accessMember marks the resources an administrator must not own
// (subscriptions, channels); the admin can only inspect them and flip enabled.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Public
	mux.Handle("POST /api/auth/login", s.handle(accessAny, true, s.login))
	mux.Handle("GET /api/meta", s.handle(accessAny, true, s.meta))

	// Authenticated, any role
	mux.Handle("GET /api/auth/me", s.handle(accessAny, false, s.me))
	mux.Handle("POST /api/auth/logout", s.handle(accessAny, false, s.logout))
	mux.Handle("POST /api/auth/password", s.handle(accessAny, false, s.changePassword))
	// Display preferences (currently only the timezone) belong to the account,
	// so both roles may change their own.
	mux.Handle("PATCH /api/me", s.handle(accessAny, false, s.updatePreferences))
	mux.Handle("GET /api/channel-fields", s.handle(accessAny, false, s.channelFields))

	// Regular users only
	mux.Handle("GET /api/subscriptions", s.handle(accessMember, false, s.listSubscriptions))
	mux.Handle("POST /api/subscriptions", s.handle(accessMember, false, s.createSubscription))
	mux.Handle("PATCH /api/subscriptions/{id}", s.handle(accessMember, false, s.updateSubscription))
	mux.Handle("DELETE /api/subscriptions/{id}", s.handle(accessMember, false, s.deleteSubscription))

	mux.Handle("GET /api/channels", s.handle(accessMember, false, s.listChannels))
	mux.Handle("POST /api/channels", s.handle(accessMember, false, s.createChannel))
	mux.Handle("PATCH /api/channels/{id}", s.handle(accessMember, false, s.updateChannel))
	mux.Handle("DELETE /api/channels/{id}", s.handle(accessMember, false, s.deleteChannel))

	// Admin only
	mux.Handle("GET /api/admin/users", s.handle(accessAdmin, false, s.listUsers))
	mux.Handle("POST /api/admin/users", s.handle(accessAdmin, false, s.createUser))
	mux.Handle("GET /api/admin/users/{id}", s.handle(accessAdmin, false, s.adminUserDetail))
	mux.Handle("PATCH /api/admin/users/{id}", s.handle(accessAdmin, false, s.updateUser))
	mux.Handle("DELETE /api/admin/users/{id}", s.handle(accessAdmin, false, s.deleteUser))
	mux.Handle("POST /api/admin/users/{id}/reset-password", s.handle(accessAdmin, false, s.resetPassword))
	mux.Handle("GET /api/admin/users/{id}/subscriptions", s.handle(accessAdmin, false, s.adminUserSubscriptions))
	mux.Handle("PATCH /api/admin/users/{id}/subscriptions/{subID}", s.handle(accessAdmin, false, s.adminSetSubscriptionEnabled))
	mux.Handle("GET /api/admin/users/{id}/channels", s.handle(accessAdmin, false, s.adminUserChannels))
	mux.Handle("PATCH /api/admin/users/{id}/channels/{chID}", s.handle(accessAdmin, false, s.adminSetChannelEnabled))
	mux.Handle("GET /api/admin/users/{id}/deliveries", s.handle(accessAdmin, false, s.adminUserDeliveries))
	mux.Handle("GET /api/admin/overview", s.handle(accessAdmin, false, s.adminOverview))
	mux.Handle("GET /api/admin/pool", s.handle(accessAdmin, false, s.adminPool))
	mux.Handle("GET /api/admin/ratelimit", s.handle(accessAdmin, false, s.adminRateLimit))
	mux.Handle("GET /api/admin/audit", s.handle(accessAdmin, false, s.adminAudit))
	mux.Handle("GET /api/admin/settings", s.handle(accessAdmin, false, s.adminGetSettings))
	mux.Handle("PUT /api/admin/settings", s.handle(accessAdmin, false, s.adminPutSettings))

	// Unknown /api/* paths must be JSON 404s for every method. A single method-
	// less "/api/" pattern would overlap "GET /" without being a strict subset,
	// which Go 1.22 ServeMux rejects at registration time; method-specific
	// catch-alls keep the SPA fallback intact. HEAD is covered by "GET /api/".
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		mux.Handle(method+" /api/", s.handle(accessAny, true, s.apiNotFound))
	}

	// SPA / static fallback.
	mux.Handle("GET /", s.handle(accessAny, true, s.static))
	return mux
}
