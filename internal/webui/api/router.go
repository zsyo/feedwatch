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
	// Self-registration is gated by webui.allow_registration, read per request.
	mux.Handle("POST /api/auth/register", s.handle(accessAny, true, s.register))
	// Third-party sign-in: start is a browser navigation, callback is the
	// provider's redirect back. Both must work without a session; mode=bind is
	// the one variant that requires one, and the handler checks that itself.
	mux.Handle("GET /api/auth/oauth/{provider}/start", s.handle(accessAny, true, s.oauthStart))
	mux.Handle("GET /api/auth/oauth/{provider}/callback", s.handle(accessAny, true, s.oauthCallback))
	mux.Handle("GET /api/meta", s.handle(accessAny, true, s.meta))

	// Authenticated, any role
	mux.Handle("GET /api/auth/me", s.handle(accessAny, false, s.me))
	mux.Handle("POST /api/auth/logout", s.handle(accessAny, false, s.logout))
	mux.Handle("POST /api/auth/password", s.handle(accessAny, false, s.changePassword))
	// Display preferences (currently only the timezone) belong to the account,
	// so both roles may change their own.
	mux.Handle("PATCH /api/me", s.handle(accessAny, false, s.updatePreferences))
	// Linked sign-in providers. accessMember on purpose: an administrator
	// account signs in with its password only, so it gets role_not_allowed
	// here instead of a page it must not use.
	mux.Handle("GET /api/me/identities", s.handle(accessMember, false, s.listIdentities))
	mux.Handle("DELETE /api/me/identities/{provider}", s.handle(accessMember, false, s.unlinkIdentity))
	mux.Handle("GET /api/channel-fields", s.handle(accessAny, false, s.channelFields))
	// The live sample uses the channel form's values but never a credential.
	// accessMember matches channel CRUD: administrators do not own channels.
	mux.Handle("POST /api/channels/preview", s.handle(accessMember, false, s.channelPreview))

	// Regular users only
	mux.Handle("GET /api/subscriptions", s.handle(accessMember, false, s.listSubscriptions))
	mux.Handle("POST /api/subscriptions", s.handle(accessMember, false, s.createSubscription))
	mux.Handle("PATCH /api/subscriptions/{id}", s.handle(accessMember, false, s.updateSubscription))
	mux.Handle("DELETE /api/subscriptions/{id}", s.handle(accessMember, false, s.deleteSubscription))

	mux.Handle("GET /api/channels", s.handle(accessMember, false, s.listChannels))
	mux.Handle("POST /api/channels", s.handle(accessMember, false, s.createChannel))
	mux.Handle("PATCH /api/channels/{id}", s.handle(accessMember, false, s.updateChannel))
	mux.Handle("DELETE /api/channels/{id}", s.handle(accessMember, false, s.deleteChannel))

	// Subscription center: a browsable template catalog plus user proposals.
	// Like subscriptions themselves, these are member-only resources.
	mux.Handle("GET /api/catalog", s.handle(accessMember, false, s.listCatalog))
	mux.Handle("GET /api/catalog/pending", s.handle(accessMember, false, s.listPendingSubmissions))
	mux.Handle("GET /api/catalog/submissions/mine", s.handle(accessMember, false, s.mySubmissions))
	mux.Handle("POST /api/catalog/submissions", s.handle(accessMember, false, s.createSubmission))
	mux.Handle("DELETE /api/catalog/submissions/{id}", s.handle(accessMember, false, s.withdrawSubmission))
	mux.Handle("POST /api/catalog/{id}/subscribe", s.handle(accessMember, false, s.quickSubscribe))

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
	// Sign-in policy: the registration switch plus one row per provider.
	mux.Handle("GET /api/admin/github-token", s.handle(accessAdmin, false, s.adminGetGithubToken))
	mux.Handle("PUT /api/admin/github-token", s.handle(accessAdmin, false, s.adminPutGithubToken))
	mux.Handle("GET /api/admin/auth", s.handle(accessAdmin, false, s.adminGetAuth))
	mux.Handle("PUT /api/admin/auth", s.handle(accessAdmin, false, s.adminPutAuth))
	mux.Handle("DELETE /api/admin/auth/{provider}", s.handle(accessAdmin, false, s.adminClearProvider))

	// Catalog administration: direct CRUD, review queue, categories, transfer.
	mux.Handle("GET /api/admin/catalog", s.handle(accessAdmin, false, s.adminListCatalog))
	mux.Handle("POST /api/admin/catalog", s.handle(accessAdmin, false, s.adminCreateEntry))
	mux.Handle("PATCH /api/admin/catalog/{id}", s.handle(accessAdmin, false, s.adminUpdateEntry))
	mux.Handle("DELETE /api/admin/catalog/{id}", s.handle(accessAdmin, false, s.adminDeleteEntry))
	mux.Handle("GET /api/admin/catalog/submissions", s.handle(accessAdmin, false, s.adminListSubmissions))
	mux.Handle("POST /api/admin/catalog/submissions/{id}/approve", s.handle(accessAdmin, false, s.approveSubmission))
	mux.Handle("POST /api/admin/catalog/submissions/{id}/reject", s.handle(accessAdmin, false, s.rejectSubmission))
	mux.Handle("GET /api/admin/catalog/categories", s.handle(accessAdmin, false, s.listCategories))
	mux.Handle("POST /api/admin/catalog/categories", s.handle(accessAdmin, false, s.createCategory))
	mux.Handle("PATCH /api/admin/catalog/categories/{id}", s.handle(accessAdmin, false, s.renameCategory))
	mux.Handle("DELETE /api/admin/catalog/categories/{id}", s.handle(accessAdmin, false, s.deleteCategory))
	mux.Handle("GET /api/admin/catalog/export", s.handle(accessAdmin, false, s.exportCatalog))
	mux.Handle("POST /api/admin/catalog/import", s.handle(accessAdmin, false, s.importCatalog))
	mux.Handle("POST /api/admin/catalog/presets/sync", s.handle(accessAdmin, false, s.syncPresets))
	mux.Handle("POST /api/admin/catalog/{id}/accept-remote", s.handle(accessAdmin, false, s.acceptRemote))

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
