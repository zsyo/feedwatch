//go:build webui

package api

import (
	"database/sql"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/store"
)

func stubAssets() fstest.MapFS {
	return fstest.MapFS{
		"stub.html": &fstest.MapFile{Data: []byte("<h1>placeholder</h1>")},
	}
}

func newAdminTestServer(t *testing.T, assets fs.FS) (*httptest.Server, *sql.DB) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	mustUser(t, db, "admin", "admin-pass", "admin", 0)
	mustUser(t, db, "alice", "alice-pass", "user", 0)
	am := auth.New(db, time.Hour, false)
	ts := httptest.NewServer(New(st, am, assets, nil, "test-version").Handler())
	t.Cleanup(ts.Close)
	return ts, db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestAdminEndpointsRequireAdminRole(t *testing.T) {
	ts, _ := newAdminTestServer(t, stubAssets())
	endpoints := []string{
		"/api/admin/overview", "/api/admin/pool", "/api/admin/ratelimit",
		"/api/admin/audit", "/api/admin/settings",
	}
	alice := login(t, ts, "alice", "alice-pass")
	for _, path := range endpoints {
		alice.do(http.MethodGet, path, nil, http.StatusForbidden)
	}
	alice.do(http.MethodPut, "/api/admin/settings", map[string]any{
		"settings": map[string]string{"poll.interval": "5m"},
	}, http.StatusForbidden)

	admin := login(t, ts, "admin", "admin-pass")
	for _, path := range endpoints {
		admin.do(http.MethodGet, path, nil, http.StatusOK)
	}
}

func TestAdminPoolMasksCredentialsAndCountsSubscribers(t *testing.T) {
	ts, db := newAdminTestServer(t, stubAssets())
	mustExec(t, db, `INSERT INTO feed_pool
		(url_key, url, source_type, etag, last_modified, last_checked, last_version)
		VALUES (?,?,?,?,?,?,?)`, "key1",
		"https://user:hunter2@example.com/org/repo/releases.atom?token=SUPERSECRET&page=1#frag",
		"atom", "etag-1", "", "2026-01-01T00:00:00Z", "v1.2.3")
	mustExec(t, db, `INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)`,
		"key1", "tag:v1", store.Now())
	mustExec(t, db, `INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)`,
		"key1", "tag:v2", store.Now())
	mustExec(t, db, `INSERT INTO subscriptions (user_id, url, url_key, enabled) VALUES (?,?,?,?)`,
		1, "https://example.com/org/repo/releases.atom", "key1", 1)
	mustExec(t, db, `INSERT INTO subscriptions (user_id, url, url_key, enabled) VALUES (?,?,?,?)`,
		2, "https://example.com/org/repo/releases.atom", "key1", 0)

	admin := login(t, ts, "admin", "admin-pass")
	out := admin.do(http.MethodGet, "/api/admin/pool", nil, http.StatusOK)
	rows := out["pool"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 pool row, got %+v", out)
	}
	row := rows[0].(map[string]any)
	masked := row["url"].(string)
	for _, secret := range []string{"SUPERSECRET", "hunter2", "frag"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("pool url leaked %q: %s", secret, masked)
		}
	}
	if !strings.Contains(masked, "REDACTED") || !strings.Contains(masked, "page=1") ||
		!strings.Contains(masked, "token=REDACTED") {
		t.Fatalf("pool url lost its useful parts: %s", masked)
	}
	if row["url_key"] != "key1" || row["last_version"] != "v1.2.3" {
		t.Fatalf("unexpected pool row: %+v", row)
	}
	if row["has_etag"] != true || row["has_last_modified"] != false {
		t.Fatalf("unexpected conditional flags: %+v", row)
	}
	if row["subscribers"].(float64) != 1 || row["users"].(float64) != 2 || row["seen_releases"].(float64) != 2 {
		t.Fatalf("unexpected pool aggregates: %+v", row)
	}
}

func TestAdminSettingsValidationAndAtomicUpdate(t *testing.T) {
	ts, db := newAdminTestServer(t, stubAssets())
	mustExec(t, db, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)`,
		"poll.interval", "30m", store.Now())
	mustExec(t, db, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)`,
		"webui.cookie_secure", "false", store.Now())

	admin := login(t, ts, "admin", "admin-pass")
	get := admin.do(http.MethodGet, "/api/admin/settings", nil, http.StatusOK)
	if get["applies"] != "restart" {
		t.Fatalf("settings must state that a restart is required: %+v", get)
	}
	hints := get["editable"].(map[string]any)
	if _, ok := hints["poll.interval"]; !ok {
		t.Fatalf("poll.interval must be editable: %+v", hints)
	}
	if _, ok := hints["webui.cookie_secure"]; ok {
		t.Fatal("env-only settings must not be editable from the UI")
	}
	if !containsString(get["env_only"].([]any), "webui.cookie_secure") ||
		!containsString(get["informational"].([]any), "notify.mode") {
		t.Fatalf("unexpected settings classification: %+v", get)
	}

	valid := map[string]string{
		"poll.interval":         "45m",
		"poll.concurrency":      "6",
		"notify.send_spacing":   "0s",
		"http.conditional":      "false",
		"notify.subject_prefix": "[FW]",
	}
	admin.do(http.MethodPut, "/api/admin/settings", map[string]any{"settings": valid}, http.StatusOK)
	for k, want := range valid {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, k).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("setting %s = %q, want %q", k, got, want)
		}
	}

	// Every rejection below must leave the stored value untouched.
	bad := []map[string]string{
		{"unknown.key": "x"},
		{"poll.interval": "not-a-duration"},
		{"poll.interval": "-5s"},
		{"poll.concurrency": "999"},
		{"http.conditional": "maybe"},
		{"notify.subject_prefix": "line\nbreak"},
		{"poll.interval": "50m", "poll.concurrency": "999"}, // atomicity
	}
	for _, patch := range bad {
		admin.do(http.MethodPut, "/api/admin/settings",
			map[string]any{"settings": patch}, http.StatusBadRequest)
	}
	admin.do(http.MethodPut, "/api/admin/settings",
		map[string]any{"settings": map[string]string{}}, http.StatusBadRequest)
	var interval string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='poll.interval'`).Scan(&interval); err != nil {
		t.Fatal(err)
	}
	if interval != "45m" {
		t.Fatalf("rejected patch changed poll.interval to %q", interval)
	}

	// A write without the CSRF header is rejected even for an administrator.
	noCSRF := &testClient{t: t, base: ts.URL, http: admin.http}
	noCSRF.do(http.MethodPut, "/api/admin/settings",
		map[string]any{"settings": map[string]string{"poll.interval": "1h"}}, http.StatusForbidden)
}

func containsString(values []any, want string) bool {
	for _, v := range values {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

func TestAdminAuditListsWriteEvents(t *testing.T) {
	ts, _ := newAdminTestServer(t, stubAssets())
	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPost, "/api/admin/users", map[string]string{
		"username": "carol", "password": "carol-pass", "role": "user",
	}, http.StatusCreated)

	out := admin.do(http.MethodGet, "/api/admin/audit?limit=1", nil, http.StatusOK)
	if out["total"].(float64) < 1 || out["limit"].(float64) != 1 {
		t.Fatalf("unexpected audit envelope: %+v", out)
	}
	events := out["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("limit was ignored: %+v", out)
	}
	first := events[0].(map[string]any)
	if first["action"] != "POST /api/admin/users" || first["actor_username"] != "admin" {
		t.Fatalf("unexpected audit row: %+v", first)
	}
}

func TestAdminRateLimitShowsChannelGatesWithoutIPs(t *testing.T) {
	ts, db := newAdminTestServer(t, stubAssets())
	mustExec(t, db, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)`,
		"notify.send_spacing", "250ms", store.Now())
	mustExec(t, db, `INSERT INTO delivery_groups
		(group_id, cycle_id, merge_key, channel_type, idempotency_key, status, created_at)
		VALUES (?,?,?,?,?,?,?)`, "g1", "c1", "k1", "email", "i1", "pending", store.Now())
	mustExec(t, db, `INSERT INTO delivery_groups
		(group_id, cycle_id, merge_key, channel_type, idempotency_key, status, created_at)
		VALUES (?,?,?,?,?,?,?)`, "g2", "c1", "k2", "telegram", "i2", "sent", store.Now())

	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPost, "/api/admin/users", map[string]string{
		"username": "dave", "password": "dave-pass", "role": "user",
	}, http.StatusCreated)
	out := admin.do(http.MethodGet, "/api/admin/ratelimit", nil, http.StatusOK)
	notify := out["notify"].(map[string]any)
	if notify["gate_granularity"] != "channel_type" || notify["send_spacing"] != "250ms" {
		t.Fatalf("unexpected notify gate info: %+v", notify)
	}
	httpLimiter := out["http_limiter"].(map[string]any)
	if httpLimiter["active_buckets"].(float64) < 1 {
		t.Fatalf("expected live HTTP buckets after writes: %+v", httpLimiter)
	}
	buckets := httpLimiter["buckets"].([]any)
	for _, b := range buckets {
		key := b.(map[string]any)["key"].(string)
		if !strings.HasPrefix(key, "***|") || strings.Contains(key, "127.0.0.1") {
			t.Fatalf("rate-limit bucket leaked a client IP: %q", key)
		}
	}
	byChannel := out["delivery_groups_by_channel"].(map[string]any)
	if byChannel["email"].(map[string]any)["pending"].(float64) != 1 ||
		byChannel["telegram"].(map[string]any)["sent"].(float64) != 1 {
		t.Fatalf("unexpected per-channel delivery status: %+v", byChannel)
	}
	if out["applies"] != "restart" {
		t.Fatalf("rate-limit view must state restart semantics: %+v", out)
	}
}

func TestStaticAssetHeadersAndMethodAgnosticAPINotFound(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<html>spa</html>")},
		"stub.html":        &fstest.MapFile{Data: []byte("<html>stub</html>")},
		"assets/app-a1.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		"BUILD_ID":         &fstest.MapFile{Data: []byte("build-1\n")},
	}
	ts, _ := newAdminTestServer(t, assets)
	client := &http.Client{}

	resp, err := client.Get(ts.URL + "/assets/app-a1.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "console.log(1)" {
		t.Fatalf("asset serving failed: %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("unexpected js content type: %q", got)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("hashed asset is not cached immutably: %q", resp.Header.Get("Cache-Control"))
	}

	resp, err = client.Get(ts.URL + "/some/spa/route")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "spa") {
		t.Fatalf("SPA fallback failed: %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("index.html must not be cached: %q", got)
	}

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD / returned a body: %d %q", resp.StatusCode, body)
	}

	// Unknown API paths are JSON 404s for every method, not HTML or a bare 405.
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		c := &testClient{t: t, base: ts.URL, http: client}
		out := c.do(method, "/api/nope", nil, http.StatusNotFound)
		errBody := out["error"].(map[string]any)
		if errBody["code"] != "not_found" {
			t.Fatalf("%s /api/nope returned %+v", method, out)
		}
	}
}
