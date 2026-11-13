//go:build webui

package api

import (
	"bytes"
	"database/sql"
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
	"feedwatch/internal/webui/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
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
	ts := httptest.NewServer(New(st, am, assets, nil, "test-version").Handler())
	t.Cleanup(ts.Close)
	return ts, db
}

func mustUser(t *testing.T, db *sql.DB, name, password, role string, mustChange int) int64 {
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
	ts, _ := newTestServer(t)
	anon := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	anon.do(http.MethodGet, "/api/auth/me", nil, http.StatusUnauthorized)

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
	ts, _ := newTestServer(t)
	alice := login(t, ts, "alice", "alice-pass")
	alice.do(http.MethodGet, "/api/admin/users", nil, http.StatusForbidden)
	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodGet, "/api/admin/users", nil, http.StatusOK)

	alice.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/private.atom"}, http.StatusCreated)
	list := alice.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
	items := list["subscriptions"].([]any)
	id := int64(items[0].(map[string]any)["id"].(float64))

	// Another user's id must not be visible, let alone writable.
	other := login(t, ts, "admin", "admin-pass")
	other.do(http.MethodPatch, "/api/subscriptions/"+itoa(id),
		map[string]any{"display_name": "hijacked"}, http.StatusNotFound)
	other.do(http.MethodDelete, "/api/subscriptions/"+itoa(id), nil, http.StatusNotFound)
	mine := other.do(http.MethodGet, "/api/subscriptions", nil, http.StatusOK)
	if got := len(mine["subscriptions"].([]any)); got != 0 {
		t.Fatalf("admin sees another user's subscription: %+v", mine)
	}
}

func TestChannelRejectsAndMasksPlaintextSecrets(t *testing.T) {
	ts, _ := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")
	c.do(http.MethodPost, "/api/channels", map[string]any{
		"type": "telegram", "options": map[string]string{"token": "plaintext-secret"},
	}, http.StatusBadRequest)
	c.do(http.MethodPost, "/api/channels", map[string]any{
		"type": "telegram", "options": map[string]string{"url": "https://api.example/bot${TG_TOKEN}/send"},
	}, http.StatusCreated)
	list := c.do(http.MethodGet, "/api/channels", nil, http.StatusOK)
	items := list["channels"].([]any)
	if len(items) != 1 {
		t.Fatalf("unexpected channel list: %+v", list)
	}
	cfg := items[0].(map[string]any)["config"].(map[string]any)
	if !strings.Contains(cfg["options"].(map[string]any)["url"].(string), "${TG_TOKEN}") {
		t.Fatalf("placeholder was not preserved: %+v", cfg)
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
