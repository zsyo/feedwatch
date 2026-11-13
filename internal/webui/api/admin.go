//go:build webui

package api

import "net/http"

// adminOverview is the minimal admin surface for this feature point. The pool,
// rate-limit, settings and audit endpoints are added with the management API.
func (s *Server) adminOverview(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	counts := make(map[string]int, 6)
	for key, query := range map[string]string{
		"users":         `SELECT COUNT(*) FROM users`,
		"sessions":      `SELECT COUNT(*) FROM sessions`,
		"subscriptions": `SELECT COUNT(*) FROM subscriptions WHERE deleted_at IS NULL`,
		"channels":      `SELECT COUNT(*) FROM channels`,
		"feed_pool":     `SELECT COUNT(*) FROM feed_pool`,
		"pending":       `SELECT COUNT(*) FROM delivery_groups WHERE status='pending'`,
	} {
		var n int
		if err := s.db.QueryRow(query).Scan(&n); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		counts[key] = n
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}
