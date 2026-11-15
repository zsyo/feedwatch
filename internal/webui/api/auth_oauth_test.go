//go:build webui

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/oauth"
)

// fakeProvider is a stand-in for GitHub / linux.do: it never leaves the test
// process, so the flow can be exercised end to end without the network.
type fakeProvider struct {
	server   *httptest.Server
	provider string
	// subject/login are what /user answers.
	subject int64
	login   string
	secrets []string // every code_verifier / client_secret seen, for assertions
	lastURI string
}

func newFakeProvider(t *testing.T, key string, subject int64, login string) *fakeProvider {
	t.Helper()
	f := &fakeProvider{provider: key, subject: subject, login: login}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			f.secrets = append(f.secrets, r.Form.Get("client_secret"), r.Form.Get("code_verifier"))
			f.lastURI = r.Form.Get("redirect_uri")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
		case "/user":
			if f.provider == oauth.LinuxDo {
				_, _ = w.Write([]byte(`{"id":` + itoa(f.subject) + `,"username":"` + f.login + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":` + itoa(f.subject) + `,"login":"` + f.login + `","email":"` + f.login + `@example.com"}`))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeProvider) table(t *testing.T) map[string]oauth.Provider {
	t.Helper()
	table := oauth.Defaults()
	p := table[f.provider]
	// The authorize endpoint is never fetched; only its URL is inspected.
	p.AuthorizeURL = "https://provider.test/oauth2/authorize"
	p.TokenURL = f.server.URL + "/token"
	p.UserURL = f.server.URL + "/user"
	p.EmailsURL = ""
	table[f.provider] = p
	return table
}

// noRedirectClient never follows a redirect: the OAuth endpoints answer with
// 303/302 and the test needs the Location header itself. Cookies are still
// stored, because net/http jars them before the redirect decision.
func noRedirectClient(t *testing.T) *testClient {
	t.Helper()
	return &testClient{t: t, http: &http.Client{
		Jar:           mustJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *testClient) send(method, path string, body any, wantStatus int) *http.Response {
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
	if resp.StatusCode != wantStatus {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.t.Fatalf("%s %s = %d (want %d): %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	return resp
}

func mustJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

// stateFromAuthorize pulls the state out of the provider redirect.
func stateFromAuthorize(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("bad authorize URL %q: %v", location, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize URL carries no state: %s", location)
	}
	return state
}

func enableProvider(t *testing.T, ts *httptest.Server, key, clientID, secret string) {
	t.Helper()
	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPut, "/api/admin/auth", map[string]any{
		"providers": map[string]any{
			key: map[string]any{"enabled": true, "client_id": clientID, "client_secret": secret},
		},
	}, http.StatusOK)
}

func TestRegisterRespectsTheSwitch(t *testing.T) {
	ts, _ := newTestServer(t)
	anon := &testClient{t: t, base: ts.URL, http: &http.Client{Jar: mustJar(t)}}

	// Closed until an administrator says otherwise, and the login page learns
	// that from /api/meta.
	meta := anon.do(http.MethodGet, "/api/meta", nil, http.StatusOK)
	if meta["registration_enabled"] != false {
		t.Fatalf("registration must start closed: %+v", meta)
	}
	anon.do(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "newbie", "password": "long-enough"}, http.StatusForbidden)

	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPut, "/api/admin/auth",
		map[string]any{"registration_enabled": true}, http.StatusOK)

	meta = anon.do(http.MethodGet, "/api/meta", nil, http.StatusOK)
	if meta["registration_enabled"] != true {
		t.Fatalf("meta did not pick up the switch: %+v", meta)
	}

	// A sign-up returns a session immediately, so the SPA can land on the
	// dashboard instead of bouncing through the login form.
	created := anon.do(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "newbie", "password": "long-enough"}, http.StatusCreated)
	// Signing up signs in, so the CSRF token now guards the rest of the test.
	anon.csrf, _ = created["csrf_token"].(string)
	user, _ := created["user"].(map[string]any)
	if user["username"] != "newbie" || user["role"] != "user" {
		t.Fatalf("unexpected registered user: %+v", created)
	}
	me := anon.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	if me["user"].(map[string]any)["username"] != "newbie" {
		t.Fatalf("registration did not sign the user in: %+v", me)
	}

	// A self-chosen password is not temporary, so nothing forces a change.
	if user["must_change_password"] != false || user["password_set"] != true {
		t.Fatalf("registered user flags: %+v", user)
	}

	anon.do(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "newbie", "password": "another-pass"}, http.StatusConflict)
	anon.do(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "short-pass", "password": "1234"}, http.StatusBadRequest)
	anon.do(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "spaced name", "password": "long-enough"}, http.StatusBadRequest)
	anon.do(http.MethodPost, "/api/auth/register",
		map[string]string{"username": strings.Repeat("x", 40), "password": "long-enough"}, http.StatusBadRequest)
}

func TestOAuthAdminConfigNeverReturnsTheSecret(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, _ := newTestServer(t, WithOAuthProviders(fake.table(t)))
	admin := login(t, ts, "admin", "admin-pass")

	// Enabling without credentials must fail rather than produce a dead button.
	admin.do(http.MethodPut, "/api/admin/auth", map[string]any{
		"providers": map[string]any{oauth.GitHub: map[string]any{"enabled": true}},
	}, http.StatusBadRequest)

	// An empty secret field keeps whatever is stored, so the form can be
	// re-saved without re-typing it.
	fake.secrets = nil
	admin.do(http.MethodPut, "/api/admin/auth", map[string]any{
		"providers": map[string]any{oauth.GitHub: map[string]any{
			"enabled": true, "client_id": "the-client-id", "client_secret": "the-secret",
		}},
	}, http.StatusOK)

	raw, err := json.Marshal(admin.do(http.MethodGet, "/api/admin/auth", nil, http.StatusOK))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "the-secret") {
		t.Fatalf("admin response leaked the client secret: %s", raw)
	}
	if !strings.Contains(string(raw), `"secret_set":true`) {
		t.Fatalf("secret_set not reported: %s", raw)
	}
	if !strings.Contains(string(raw), "/api/auth/oauth/github/callback") {
		t.Fatalf("callback URL not shown for copying: %s", raw)
	}

	// Saving again with an empty secret must not wipe the stored one.
	admin.do(http.MethodPut, "/api/admin/auth", map[string]any{
		"providers": map[string]any{oauth.GitHub: map[string]any{"client_id": "the-client-id-2"}},
	}, http.StatusOK)
	out := admin.do(http.MethodGet, "/api/admin/auth", nil, http.StatusOK)
	provider := out["providers"].([]any)[0].(map[string]any)
	if provider["secret_set"] != true || provider["enabled"] != true || provider["client_id"] != "the-client-id-2" {
		t.Fatalf("second save lost state: %+v", provider)
	}

	// Clearing forgets the credentials and switches the provider off.
	admin.do(http.MethodDelete, "/api/admin/auth/"+oauth.GitHub, nil, http.StatusOK)
	out = admin.do(http.MethodGet, "/api/admin/auth", nil, http.StatusOK)
	provider = out["providers"].([]any)[0].(map[string]any)
	if provider["secret_set"] != false || provider["enabled"] != false || provider["available"] != false {
		t.Fatalf("clear did not reset the provider: %+v", provider)
	}
}

func TestOAuthStartIsInertUntilConfigured(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, _ := newTestServer(t, WithOAuthProviders(fake.table(t)))
	anon := noRedirectClient(t)
	anon.base = ts.URL

	resp := anon.send(http.MethodGet, "/api/auth/oauth/github/start", nil, http.StatusSeeOther)
	if loc := resp.Header.Get("Location"); loc != "/login?oauth_error=not_configured" {
		t.Fatalf("Location = %q", loc)
	}
	// The login page must not render a button for it either.
	meta := anon.do(http.MethodGet, "/api/meta", nil, http.StatusOK)
	if enabled := meta["oauth"].(map[string]any)["github"]; enabled != false {
		t.Fatalf("github reported available before configuration: %+v", meta)
	}
}

func TestOAuthLoginCreatesAccountThenReusesIt(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, db := newTestServer(t, WithOAuthProviders(fake.table(t)))
	enableProvider(t, ts, oauth.GitHub, "cid", "csecret")

	guest := noRedirectClient(t)
	guest.base = ts.URL

	startFlow := func() string {
		resp := guest.send(http.MethodGet, "/api/auth/oauth/github/start", nil, http.StatusFound)
		loc := resp.Header.Get("Location")
		if !strings.HasPrefix(loc, "https://provider.test/oauth2/authorize?") {
			t.Fatalf("unexpected authorize redirect: %s", loc)
		}
		if !strings.Contains(loc, "code_challenge_method=S256") {
			t.Fatalf("github flow must use PKCE: %s", loc)
		}
		return stateFromAuthorize(t, loc)
	}

	// Registration still closed: a stranger gets a clear refusal, not a user.
	resp := guest.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(startFlow()), nil, http.StatusSeeOther)
	if loc := resp.Header.Get("Location"); loc != "/login?oauth_error=registration_closed" {
		t.Fatalf("Location = %q, want registration_closed", loc)
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, "octocat").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Fatal("a closed site must not create accounts")
	}

	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPut, "/api/admin/auth", map[string]any{"registration_enabled": true}, http.StatusOK)

	// The flow is single use: replaying the same state must fail.
	state := startFlow()
	guest.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil, http.StatusSeeOther)
	replay := guest.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil, http.StatusSeeOther)
	if loc := replay.Header.Get("Location"); loc != "/login?oauth_error=state_invalid" {
		t.Fatalf("replay Location = %q", loc)
	}

	me := guest.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	user := me["user"].(map[string]any)
	if user["username"] != "octocat" || user["role"] != "user" {
		t.Fatalf("unexpected created account: %+v", user)
	}
	if user["password_set"] != false {
		t.Fatal("an account created by a provider must not have a password")
	}

	// The provider secret reaches the token endpoint (proving decryption works)
	// and the verifier proves the PKCE round trip.
	if len(fake.secrets) < 4 || fake.secrets[0] != "csecret" {
		t.Fatalf("provider did not receive the client secret: %v", fake.secrets)
	}
	if fake.secrets[1] == "" {
		t.Fatal("github flow did not send a code_verifier")
	}

	// A second sign-in with the same provider account reuses it.
	guest.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(startFlow()), nil, http.StatusSeeOther)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, "octocat").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("second sign-in created another account (count=%d)", count)
	}
}

func TestOAuthBindAndUnlink(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, _ := newTestServer(t, WithOAuthProviders(fake.table(t)))
	enableProvider(t, ts, oauth.GitHub, "cid", "csecret")

	alice := noRedirectClient(t)
	alice.base = ts.URL
	loginOut := alice.do(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "alice", "password": "alice-pass"}, http.StatusOK)
	alice.csrf, _ = loginOut["csrf_token"].(string)

	resp := alice.send(http.MethodGet, "/api/auth/oauth/github/start?mode=bind", nil, http.StatusFound)
	state := stateFromAuthorize(t, resp.Header.Get("Location"))
	bound := alice.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil, http.StatusSeeOther)
	if loc := bound.Header.Get("Location"); loc != "/settings?bind=ok&provider=github" {
		t.Fatalf("bind Location = %q", loc)
	}

	out := alice.do(http.MethodGet, "/api/me/identities", nil, http.StatusOK)
	if out["password_set"] != true {
		t.Fatalf("alice has a password: %+v", out)
	}
	identities := out["identities"].([]any)
	if len(identities) != 1 || identities[0].(map[string]any)["provider"] != "github" {
		t.Fatalf("unexpected identities: %+v", identities)
	}
	if identities[0].(map[string]any)["login"] != "octocat" {
		t.Fatalf("provider login not stored: %+v", identities)
	}

	// Now signing in through GitHub lands on alice's account.
	guest := noRedirectClient(t)
	guest.base = ts.URL
	state = stateFromAuthorize(t, guest.send(http.MethodGet, "/api/auth/oauth/github/start", nil, http.StatusFound).Header.Get("Location"))
	guest.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil, http.StatusSeeOther)
	me := guest.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	if me["user"].(map[string]any)["username"] != "alice" {
		t.Fatalf("provider sign-in did not reach the bound account: %+v", me)
	}

	alice.do(http.MethodDelete, "/api/me/identities/github", nil, http.StatusOK)
	after := alice.do(http.MethodGet, "/api/me/identities", nil, http.StatusOK)
	if len(after["identities"].([]any)) != 0 {
		t.Fatalf("unlink left a row: %+v", after)
	}
	alice.do(http.MethodDelete, "/api/me/identities/github", nil, http.StatusNotFound)
	alice.do(http.MethodDelete, "/api/me/identities/nope", nil, http.StatusNotFound)
}

func TestOAuthAccountCannotUnlinkItsOnlyWayIn(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, _ := newTestServer(t, WithOAuthProviders(fake.table(t)))
	enableProvider(t, ts, oauth.GitHub, "cid", "csecret")
	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPut, "/api/admin/auth", map[string]any{"registration_enabled": true}, http.StatusOK)

	guest := noRedirectClient(t)
	guest.base = ts.URL
	state := stateFromAuthorize(t, guest.send(http.MethodGet, "/api/auth/oauth/github/start", nil, http.StatusFound).Header.Get("Location"))
	guest.send(http.MethodGet,
		"/api/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil, http.StatusSeeOther)

	me := guest.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	// The session came from a redirect, so the CSRF token has to be picked up
	// from /me the way the SPA does on bootstrap.
	guest.csrf, _ = me["csrf_token"].(string)
	user := me["user"].(map[string]any)
	if user["password_set"] != false {
		t.Fatalf("provider account should start without a password: %+v", user)
	}
	out := guest.do(http.MethodGet, "/api/me/identities", nil, http.StatusOK)
	if out["password_set"] != false || len(out["identities"].([]any)) != 1 {
		t.Fatalf("unexpected identities: %+v", out)
	}
	guest.do(http.MethodDelete, "/api/me/identities/github", nil, http.StatusBadRequest)

	// Setting a password needs no old one (there is none), and then the
	// binding can go.
	guest.do(http.MethodPost, "/api/auth/password",
		map[string]string{"old_password": "", "new_password": "a-real-password"}, http.StatusOK)
	fresh := login(t, ts, "octocat", "a-real-password")
	fresh.do(http.MethodDelete, "/api/me/identities/github", nil, http.StatusOK)
}

func TestAdministratorsHaveNoThirdPartySignIn(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, _ := newTestServer(t, WithOAuthProviders(fake.table(t)))
	enableProvider(t, ts, oauth.GitHub, "cid", "csecret")

	admin := noRedirectClient(t)
	admin.base = ts.URL
	out := admin.do(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "admin", "password": "admin-pass"}, http.StatusOK)
	admin.csrf, _ = out["csrf_token"].(string)

	admin.do(http.MethodGet, "/api/me/identities", nil, http.StatusForbidden)
	resp := admin.send(http.MethodGet, "/api/auth/oauth/github/start?mode=bind", nil, http.StatusSeeOther)
	if loc := resp.Header.Get("Location"); loc != "/settings?bind_error=admin_not_allowed" {
		t.Fatalf("admin bind Location = %q", loc)
	}
}

func TestOAuthCallbackRejectsUnknownState(t *testing.T) {
	fake := newFakeProvider(t, oauth.GitHub, 42, "octocat")
	ts, _ := newTestServer(t, WithOAuthProviders(fake.table(t)))
	guest := noRedirectClient(t)
	guest.base = ts.URL
	resp := guest.send(http.MethodGet, "/api/auth/oauth/github/callback?code=abc&state=forged", nil, http.StatusSeeOther)
	if loc := resp.Header.Get("Location"); loc != "/login?oauth_error=state_invalid" {
		t.Fatalf("Location = %q", loc)
	}
	guest.send(http.MethodGet, "/api/auth/oauth/nope/start", nil, http.StatusNotFound)
}
