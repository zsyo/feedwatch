//go:build webui

package api

import (
	"database/sql"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"feedwatch/internal/config"
	"feedwatch/internal/webui"
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
	ts := httptest.NewServer(New(st, am, assets, nil, "test-version", newTestSealer(t)).Handler())
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
	hints := get["docs"].(map[string]any)
	if _, ok := hints["poll.interval"]; !ok {
		t.Fatalf("poll.interval must be documented: %+v", hints)
	}
	if _, ok := editableSettings["webui.cookie_secure"]; ok {
		t.Fatal("env-only settings must not be editable from the UI")
	}
	// Read-only keys still need a purpose line so the page can explain them.
	if _, ok := hints["webui.cookie_secure"]; !ok {
		t.Fatal("read-only settings must carry documentation too")
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
		{"presets.base_url": "http://example.com/presets"},
		{"presets.base_url": "https://user:pass@example.com/presets"},
		{"presets.base_url": "https://192.168.1.1/presets"},
		{"presets.base_url": "https://example.com/presets?x=1"},
		{"presets.timeout": "not-a-duration"},
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

func TestAdminUserDrillDownIsReadOnly(t *testing.T) {
	ts, db := newAdminTestServer(t, stubAssets())
	var aliceID int64
	if err := db.QueryRow("SELECT id FROM users WHERE username='alice'").Scan(&aliceID); err != nil {
		t.Fatal(err)
	}

	alice := login(t, ts, "alice", "alice-pass")
	alice.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusCreated)
	created := alice.do(http.MethodPost, "/api/channels", map[string]any{
		"type": "telegram", "fields": map[string]string{"chat_id": "1"},
		"secrets": map[string]string{"token": "123:abc"},
	}, http.StatusCreated)
	channelID := int64(created["id"].(float64))

	// A regular user must not reach the drill-down.
	alice.do(http.MethodGet, "/api/admin/users/"+itoa(aliceID), nil, http.StatusForbidden)

	admin := login(t, ts, "admin", "admin-pass")
	detail := admin.do(http.MethodGet, "/api/admin/users/"+itoa(aliceID), nil, http.StatusOK)
	if detail["user"].(map[string]any)["username"] != "alice" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	counts := detail["counts"].(map[string]any)
	if counts["subscriptions"].(float64) != 1 || counts["channels"].(float64) != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}

	subs := admin.do(http.MethodGet, "/api/admin/users/"+itoa(aliceID)+"/subscriptions", nil, http.StatusOK)
	subItems := subs["subscriptions"].([]any)
	if len(subItems) != 1 {
		t.Fatalf("subscriptions = %+v", subs)
	}
	subID := int64(subItems[0].(map[string]any)["id"].(float64))

	// Channel metadata is visible, credentials are not.
	resp, err := admin.http.Get(ts.URL + "/api/admin/users/" + itoa(aliceID) + "/channels")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "123:abc") || strings.Contains(string(raw), "fwsec:") {
		t.Fatalf("admin drill-down leaked a credential: %s", raw)
	}
	if !strings.Contains(string(raw), `"secret_fields":["token"]`) {
		t.Fatalf("admin drill-down should list secret field names: %s", raw)
	}

	// Enable/disable is allowed; anything else is a 400.
	admin.do(http.MethodPatch, "/api/admin/users/"+itoa(aliceID)+"/channels/"+itoa(channelID),
		map[string]any{"enabled": false}, http.StatusOK)
	admin.do(http.MethodPatch, "/api/admin/users/"+itoa(aliceID)+"/channels/"+itoa(channelID),
		map[string]any{"name": "renamed"}, http.StatusBadRequest)
	admin.do(http.MethodPatch, "/api/admin/users/"+itoa(aliceID)+"/channels/"+itoa(channelID),
		map[string]any{"enabled": true, "name": "renamed"}, http.StatusBadRequest)
	admin.do(http.MethodPatch, "/api/admin/users/"+itoa(9999)+"/channels/"+itoa(channelID),
		map[string]any{"enabled": true}, http.StatusNotFound)
	admin.do(http.MethodPatch, "/api/admin/users/"+itoa(aliceID)+"/subscriptions/"+itoa(subID),
		map[string]any{"enabled": false}, http.StatusOK)

	// The admin cannot create or delete somebody else's resources at all.
	admin.do(http.MethodPost, "/api/admin/users/"+itoa(aliceID)+"/channels",
		map[string]any{"type": "log"}, http.StatusNotFound)
	admin.do(http.MethodDelete, "/api/admin/users/"+itoa(aliceID)+"/channels/"+itoa(channelID),
		nil, http.StatusNotFound)

	deliveries := admin.do(http.MethodGet,
		"/api/admin/users/"+itoa(aliceID)+"/deliveries?limit=10", nil, http.StatusOK)
	if _, ok := deliveries["deliveries"].([]any); !ok {
		t.Fatalf("unexpected deliveries payload: %+v", deliveries)
	}

	// Alice sees the enforced state.
	channels := alice.do(http.MethodGet, "/api/channels", nil, http.StatusOK)
	if channels["channels"].([]any)[0].(map[string]any)["enabled"] != false {
		t.Fatalf("admin toggle was not applied: %+v", channels)
	}
}

func TestMaskErrorTextStripsCredentials(t *testing.T) {
	in := `telegram error 404: https://api.telegram.org/bot123:abc/sendMessage failed; ` +
		`Authorization: Bearer sekrit; webhook?key=abcdef`
	out := maskErrorText(in)
	for _, leak := range []string{"123:abc", "sekrit", "abcdef"} {
		if strings.Contains(out, leak) {
			t.Fatalf("maskErrorText leaked %q: %s", leak, out)
		}
	}
}

func TestSettingsDocsDescribeEveryKey(t *testing.T) {
	docs := settingDocs()
	for key := range editableSettings {
		doc, ok := docs[key]
		if !ok {
			t.Fatalf("%s has no documentation", key)
		}
		if doc.Summary == "" || doc.Format == "" || doc.Default == "" || doc.Detail == "" {
			t.Fatalf("%s documentation is incomplete: %+v", key, doc)
		}
		// The summary sits on one line next to the key; anything longer wraps
		// and drags the table row height around.
		if n := len([]rune(doc.Summary)); n > 20 {
			t.Errorf("%s summary is %d runes, keep it under 20: %q", key, n, doc.Summary)
		}
		// A documented default must be a value the validator actually accepts,
		// otherwise the page would advertise an unsavable setting.
		if err := validateSetting(key, doc.Default); err != nil {
			t.Errorf("%s default %q fails validation: %v", key, doc.Default, err)
		}
	}
	for key := range readonlyDocs {
		if _, ok := docs[key]; !ok {
			t.Errorf("%s is read-only but undocumented", key)
		}
	}
}

// TestSettingDefaultsMatchConfig guards against the docs drifting away from the
// values config.Default seeds: the page shows `Default` as "what the process
// would use", so it has to be the same number.
func TestSettingDefaultsMatchConfig(t *testing.T) {
	c := config.Default()
	want := map[string]string{
		"poll.interval":         c.Poll.Interval.String(),
		"poll.timeout":          c.Poll.Timeout.String(),
		"poll.concurrency":      strconv.Itoa(c.Poll.Concurrency),
		"poll.request_spacing":  c.Poll.RequestSpacing.String(),
		"notify.send_spacing":   c.Notify.SendSpacing.String(),
		"notify.subject_prefix": c.Notify.SubjectPrefix,
		"http.timeout":          c.HTTP.Timeout.String(),
		"http.user_agent":       c.HTTP.UserAgent,
		"http.retry_max":        strconv.Itoa(c.HTTP.RetryMax),
		"http.retry_base_delay": c.HTTP.RetryBaseDelay.String(),
		"http.conditional":      strconv.FormatBool(c.HTTP.Conditional),
	}
	for key, expected := range want {
		rule, ok := editableSettings[key]
		if !ok {
			t.Errorf("%s is seeded by config.Default but not editable", key)
			continue
		}
		if rule.kind == "duration" {
			// "30m" and "30m0s" are the same duration; compare parsed values.
			got, err1 := time.ParseDuration(rule.def)
			exp, err2 := time.ParseDuration(expected)
			if err1 != nil || err2 != nil || got != exp {
				t.Errorf("%s default = %q, config.Default = %q", key, rule.def, expected)
			}
			continue
		}
		if rule.def != expected {
			t.Errorf("%s default = %q, config.Default = %q", key, rule.def, expected)
		}
	}
}

// TestPresetSettingDefaultsMatchSeed keeps the documented defaults, the
// seeded values and the shared constants in sync. Unlike the polling knobs,
// these two are WebUI-only and are not seeded by config.Default().
func TestPresetSettingDefaultsMatchSeed(t *testing.T) {
	for key, want := range map[string]string{
		"presets.base_url": webui.DefaultPresetBaseURL,
		"presets.timeout":  webui.DefaultPresetTimeout,
	} {
		rule, ok := editableSettings[key]
		if !ok {
			t.Fatalf("%s is not editable", key)
		}
		if rule.def != want {
			t.Errorf("%s default = %q, want %q", key, rule.def, want)
		}
		if err := validateSetting(key, rule.def); err != nil {
			t.Errorf("%s default fails validation: %v", key, err)
		}
	}
}
