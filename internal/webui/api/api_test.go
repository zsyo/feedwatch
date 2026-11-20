//go:build webui

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/secrets"
	"feedwatch/internal/webui/store"
)

func newTestSealer(t *testing.T) *secrets.Sealer {
	t.Helper()
	s, err := secrets.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// opts let preset-sync tests inject a Loader pointed at a local test server;
// every existing caller keeps working with no options.
func newTestServer(t *testing.T, opts ...Option) (*httptest.Server, *store.DB) {
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
	assets := fstest.MapFS{
		"stub.html": &fstest.MapFile{Data: []byte("<h1>placeholder</h1>")},
	}
	ts := httptest.NewServer(New(st, am, assets, nil, "test-version", newTestSealer(t), opts...).Handler())
	t.Cleanup(ts.Close)
	return ts, db
}

func mustUser(t *testing.T, db *store.DB, name, password, role string, mustChange int) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, name, hash, role, 1, mustChange, store.Now(), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func userIDByName(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username=?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedEnabledChannel inserts an enabled channel directly, bypassing the API.
// Subscription tests need one because subscribeURL requires at least one
// enabled channel before it will create a subscription.
func seedEnabledChannel(t *testing.T, db *store.DB, userID int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO channels (user_id, type, name, config_json, enabled)
		VALUES (?, 'log', 'test', '{}', 1)`, userID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

type testClient struct {
	t      *testing.T
	base   string
	http   *http.Client
	csrf   string
	cookie *http.Cookie
}

func (c *testClient) do(method, path string, body any, wantStatus int) map[string]any {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set(auth.CSRFHeaderName, c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("%s %s = %d (want %d): %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 && strings.Contains(resp.Header.Get("Content-Type"), "json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("decode %s: %v", raw, err)
		}
	}
	// Remember cookie attributes from the login response for assertions.
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.SessionCookieName {
			c.cookie = ck
		}
	}
	return out
}

func login(t *testing.T, ts *httptest.Server, username, password string) *testClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
	out := c.do(http.MethodPost, "/api/auth/login",
		map[string]string{"username": username, "password": password}, http.StatusOK)
	c.csrf, _ = out["csrf_token"].(string)
	if c.csrf == "" {
		t.Fatalf("login did not return a CSRF token: %+v", out)
	}
	return c
}

func TestMetaIsPublic(t *testing.T) {
	ts, _ := newTestServer(t)
	c := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	out := c.do(http.MethodGet, "/api/meta", nil, http.StatusOK)
	if out["mode"] != "webui" || out["version"] != "test-version" || out["build_id"] != "dev" {
		t.Fatalf("unexpected meta: %+v", out)
	}
}

func TestLoginCookieCSRFAndSubscriptionCRUD(t *testing.T) {
	ts, db := newTestServer(t)
	anon := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	anon.do(http.MethodGet, "/api/auth/me", nil, http.StatusUnauthorized)

	seedEnabledChannel(t, db, userIDByName(t, db, "alice"))
	c := login(t, ts, "alice", "alice-pass")
	if c.cookie == nil || !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteStrictMode || c.cookie.Path != "/" {
		t.Fatalf("unexpected session cookie: %+v", c.cookie)
	}
	me := c.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	user, _ := me["user"].(map[string]any)
	if user["username"] != "alice" {
		t.Fatalf("unexpected /me: %+v", me)
	}

	// A write without the CSRF header must be rejected even with a valid cookie.
	noCSRF := &testClient{t: t, base: ts.URL, http: c.http}
	noCSRF.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusForbidden)

	c.do(http.MethodPost, "/api/subscriptions", map[string]any{
		"url": "https://example.com/a.atom", "display_name": "A", "notify_policy": "on_update",
	}, http.StatusCreated)
	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://EXAMPLE.com:443/a.atom#x"}, http.StatusConflict)
	list := c.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
	items, _ := list["subscriptions"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 subscription, got %+v", list)
	}
	first := items[0].(map[string]any)
	id := int64(first["id"].(float64))
	c.do(http.MethodDelete, "/api/subscriptions/9999", nil, http.StatusNotFound)
	c.do(http.MethodPatch, "/api/subscriptions/"+itoa(id),
		map[string]any{"display_name": "renamed"}, http.StatusOK)
	c.do(http.MethodDelete, "/api/subscriptions/"+itoa(id), nil, http.StatusOK)
}

func TestMustChangePasswordBlocksOtherEndpoints(t *testing.T) {
	ts, db := newTestServer(t)
	mustUser(t, db, "bob", "initial-pass", "user", 1)
	c := login(t, ts, "bob", "initial-pass")
	c.do(http.MethodGet, "/api/subscriptions", nil, http.StatusForbidden)
	c.do(http.MethodPost, "/api/auth/password", map[string]string{
		"old_password": "initial-pass", "new_password": "a-brand-new-pass",
	}, http.StatusOK)
	// Changing the password invalidates every session, so log in again.
	fresh := login(t, ts, "bob", "a-brand-new-pass")
	fresh.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
}

func TestRoleGuardAndUserIsolation(t *testing.T) {
	ts, db := newTestServer(t)
	mustUser(t, db, "bob", "bob-pass", "user", 0)
	seedEnabledChannel(t, db, userIDByName(t, db, "alice"))

	alice := login(t, ts, "alice", "alice-pass")
	alice.do(http.MethodGet, "/api/admin/users", nil, http.StatusForbidden)

	// Administrators are an operations role: they must not own subscriptions at
	// all, so the member-only endpoints answer 403 instead of an empty list.
	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodGet, "/api/admin/users", nil, http.StatusOK)
	admin.do(http.MethodGet, "/api/subscriptions", nil, http.StatusForbidden)
	admin.do(http.MethodGet, "/api/channels", nil, http.StatusForbidden)

	alice.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/private.atom"}, http.StatusCreated)
	list := alice.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
	items := list["subscriptions"].([]any)
	id := int64(items[0].(map[string]any)["id"].(float64))

	// Another regular user must not see or touch it.
	bob := login(t, ts, "bob", "bob-pass")
	bob.do(http.MethodPatch, "/api/subscriptions/"+itoa(id),
		map[string]any{"display_name": "hijacked"}, http.StatusNotFound)
	bob.do(http.MethodDelete, "/api/subscriptions/"+itoa(id), nil, http.StatusNotFound)
	mine := bob.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
	if got := len(mine["subscriptions"].([]any)); got != 0 {
		t.Fatalf("bob sees another user's subscription: %+v", mine)
	}
}

func TestChannelSecretsAreSealedAndNeverEchoed(t *testing.T) {
	ts, db := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")

	// Missing required credential.
	c.do(http.MethodPost, "/api/channels", map[string]any{
		"type": "telegram", "fields": map[string]string{"chat_id": "123"},
	}, http.StatusBadRequest)
	// A credential smuggled into a plain field is rejected by name.
	c.do(http.MethodPost, "/api/channels", map[string]any{
		"type": "telegram", "fields": map[string]string{"chat_id": "123", "token": "123:abc"},
	}, http.StatusBadRequest)
	// Undeclared option.
	c.do(http.MethodPost, "/api/channels", map[string]any{
		"type": "telegram", "fields": map[string]string{"chat_id": "123", "nope": "1"},
		"secrets": map[string]string{"token": "123:abc"},
	}, http.StatusBadRequest)

	created := c.do(http.MethodPost, "/api/channels", map[string]any{
		"type":   "telegram",
		"name":   "发布群",
		"fields": map[string]string{"chat_id": "123", "parse_mode": "HTML"},
		"secrets": map[string]string{
			"token": "123:abc",
		},
	}, http.StatusCreated)
	id := int64(created["id"].(float64))

	// The raw response must not contain the secret or the sealed envelope.
	resp, err := c.http.Get(ts.URL + "/api/channels")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "123:abc") || strings.Contains(string(raw), "fwsec:") {
		t.Fatalf("channel list leaked a credential: %s", raw)
	}

	var ciphertext string
	if err := db.QueryRow(`SELECT ciphertext FROM channel_secrets WHERE channel_id=?`, id).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ciphertext, "fwsec:v1:") {
		t.Fatalf("stored value is not a sealed envelope: %q", ciphertext)
	}

	channels := func() []any {
		list := c.do(http.MethodGet, "/api/channels", nil, http.StatusOK)
		return list["channels"].([]any)
	}
	first := channels()[0].(map[string]any)
	if got := first["secret_state"]; got != "ok" {
		t.Fatalf("secret_state = %v, want ok", got)
	}
	fields := first["secret_fields"].([]any)
	if len(fields) != 1 || fields[0] != "token" {
		t.Fatalf("secret_fields = %v, want [token]", fields)
	}

	// Editing everything except the credential must keep the stored token, even
	// when the browser sends an empty string for it.
	c.do(http.MethodPatch, "/api/channels/"+itoa(id), map[string]any{
		"type":   "telegram",
		"name":   "改名",
		"fields": map[string]string{"chat_id": "456"},
		"secrets": map[string]string{
			"token": "",
		},
	}, http.StatusOK)
	first = channels()[0].(map[string]any)
	if got := first["secret_state"]; got != "ok" {
		t.Fatalf("empty secret input wiped the token: secret_state = %v", got)
	}
	cfg := first["config"].(map[string]any)
	if cfg["options"].(map[string]any)["chat_id"] != "456" {
		t.Fatalf("options were not replaced: %+v", cfg)
	}

	// An explicit clear removes it.
	c.do(http.MethodPatch, "/api/channels/"+itoa(id), map[string]any{
		"type": "telegram", "fields": map[string]string{"chat_id": "456"},
		"clear_secrets": []string{"token"},
	}, http.StatusBadRequest) // token is required, so clearing alone is invalid

	// The last enabled channel cannot be disabled; the guard comes from the
	// "at least one enabled channel" invariant.
	c.do(http.MethodPatch, "/api/channels/"+itoa(id), map[string]any{"enabled": false},
		http.StatusBadRequest)

	// Email keeps its API key in the sealed map, exposed only as a field name.
	mail := c.do(http.MethodPost, "/api/channels", map[string]any{
		"type":    "email",
		"fields":  map[string]string{"from": "a@example.com", "to": "b@example.com"},
		"secrets": map[string]string{"api_key": "re_secret"},
	}, http.StatusCreated)
	if mail["id"] == nil {
		t.Fatalf("email channel was not created: %+v", mail)
	}
	list := channels()
	if len(list) != 2 {
		t.Fatalf("channels = %d, want 2", len(list))
	}

	// With a second enabled channel present, toggling one off is allowed.
	c.do(http.MethodPatch, "/api/channels/"+itoa(id), map[string]any{"enabled": false}, http.StatusOK)
	first = channels()[0].(map[string]any)
	if first["enabled"] != false {
		t.Fatalf("enabled was not toggled: %+v", first)
	}
}

func TestStaticFallbackAndUnknownAPI(t *testing.T) {
	ts, _ := newTestServer(t)
	c := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	resp, err := c.http.Get(ts.URL + "/some/spa/route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "placeholder") {
		t.Fatalf("SPA fallback failed: %d %s", resp.StatusCode, body)
	}
	c.do(http.MethodGet, "/api/does-not-exist", nil, http.StatusNotFound)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
