//go:build webui

package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testProvider(t *testing.T, key string, handler http.Handler) Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := Defaults()[key]
	p.TokenURL = srv.URL + "/token"
	p.UserURL = srv.URL + "/user"
	if p.EmailsURL != "" {
		p.EmailsURL = srv.URL + "/emails"
	}
	return p
}

func TestAuthorizeURLForCarriesTheFlow(t *testing.T) {
	creds := Credentials{ClientID: "cid"}
	p := Defaults()[GitHub]
	got := p.AuthorizeURLFor(creds, "https://fw.example/api/auth/oauth/github/callback", "state-1", "challenge-1")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://github.com/login/oauth/authorize?") {
		t.Fatalf("wrong base: %s", got)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"client_id":             "cid",
		"response_type":         "code",
		"state":                 "state-1",
		"redirect_uri":          "https://fw.example/api/auth/oauth/github/callback",
		"scope":                 "read:user user:email",
		"code_challenge":        "challenge-1",
		"code_challenge_method": "S256",
	} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}

	// linux.do Connect does not document PKCE support, so the parameter must
	// not be sent there even when a challenge is available.
	linux := Defaults()[LinuxDo]
	u2, err := url.Parse(linux.AuthorizeURLFor(creds, "https://fw.example/cb", "s", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if u2.Query().Has("code_challenge") {
		t.Error("linux.do must not receive a PKCE challenge")
	}
	if u2.Query().Get("scope") != "user" {
		t.Errorf("linux.do scope = %q", u2.Query().Get("scope"))
	}
}

func TestExchangeGitHub(t *testing.T) {
	var gotVerifier string
	p := testProvider(t, GitHub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "the-code" {
				t.Errorf("unexpected token form: %v", r.Form)
			}
			if r.Form.Get("client_secret") != "secret" {
				t.Errorf("client_secret = %q", r.Form.Get("client_secret"))
			}
			gotVerifier = r.Form.Get("code_verifier")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer","scope":"read:user"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("missing bearer token: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"The Octocat","email":""}`))
		case "/emails":
			_, _ = w.Write([]byte(`[
				{"email":"second@example.com","primary":false,"verified":true},
				{"email":"primary@example.com","primary":true,"verified":true}]`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))

	profile, err := p.Exchange(context.Background(), http.DefaultClient,
		Credentials{ClientID: "cid", ClientSecret: "secret"}, "the-code", "https://fw.example/cb", "verifier-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotVerifier != "verifier-1" {
		t.Errorf("code_verifier = %q, want verifier-1", gotVerifier)
	}
	want := Profile{Subject: "42", Login: "octocat", Email: "primary@example.com"}
	if profile != want {
		t.Errorf("profile = %+v, want %+v", profile, want)
	}
}

func TestExchangeLinuxDo(t *testing.T) {
	p := testProvider(t, LinuxDo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.Form.Has("code_verifier") {
				t.Error("linux.do must not receive a code_verifier")
			}
			_, _ = w.Write([]byte(`{"access_token":"ldo","token_type":"Bearer"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":777,"username":"linuxuser","name":"Linux User","trust_level":3}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	profile, err := p.Exchange(context.Background(), http.DefaultClient,
		Credentials{ClientID: "cid", ClientSecret: "secret"}, "code", "https://fw.example/cb", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Subject: "777", Login: "linuxuser"}
	if profile != want {
		t.Errorf("profile = %+v, want %+v", profile, want)
	}
}

func TestExchangeReportsProviderErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name: "token endpoint error field",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"expired"}`))
			},
			want: ErrExchange,
		},
		{
			name: "token endpoint 500",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			want: ErrExchange,
		},
		{
			name: "token endpoint form encoded",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`access_token=tok&token_type=bearer`))
			},
			want: ErrExchange,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testProvider(t, GitHub, tc.handler)
			_, err := p.Exchange(context.Background(), http.DefaultClient,
				Credentials{ClientID: "cid", ClientSecret: "secret"}, "code", "https://fw.example/cb", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExchangeRequiresAUserID(t *testing.T) {
	p := testProvider(t, GitHub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":0,"login":"ghost"}`))
	}))
	if _, err := p.Exchange(context.Background(), http.DefaultClient,
		Credentials{ClientID: "cid", ClientSecret: "secret"}, "code", "https://fw.example/cb", ""); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v, want ErrNoIdentity", err)
	}
}

func TestStateAndPKCE(t *testing.T) {
	raw, hash, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 40 || len(hash) != 64 {
		t.Fatalf("state %d chars, hash %d chars", len(raw), len(hash))
	}
	if HashState(raw) != hash {
		t.Fatal("HashState does not match GenerateState")
	}
	other, _, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	if other == raw {
		t.Fatal("two states must not collide")
	}

	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) != 43 || len(challenge) != 43 {
		t.Fatalf("verifier %d, challenge %d, want 43 each", len(verifier), len(challenge))
	}
	if verifier == challenge {
		t.Fatal("challenge must be the hash of the verifier")
	}
}
