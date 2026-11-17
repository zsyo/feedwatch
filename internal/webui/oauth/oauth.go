//go:build webui

// Package oauth implements the authorization-code flow for the third-party
// identity providers FeedWatch can sign users in with.
//
// It deliberately knows nothing about FeedWatch: no database, no sessions, no
// HTTP handlers. It builds an authorize URL, exchanges a code for a token and
// turns the provider's user payload into a Profile. The API layer owns the
// state row, the account binding and the session.
//
// Both providers speak plain OAuth 2.0 with a Bearer token on /api/user, so one
// implementation covers them; the differences (endpoint URLs, scope, PKCE
// support, field names) live in the Provider table below.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Provider keys are stored in user_identities.provider and auth_providers
// .provider, so they are part of the database format: never rename one.
const (
	GitHub  = "github"
	LinuxDo = "linuxdo"
)

var (
	// ErrExchange reports a rejected code or a failed token/user request.
	ErrExchange = errors.New("oauth: exchange failed")
	// ErrNoIdentity reports a provider payload that carries no usable user id.
	ErrNoIdentity = errors.New("oauth: provider returned no user id")
)

// Provider describes one OAuth 2.0 identity provider. The zero value is not
// usable; look one up through Defaults.
type Provider struct {
	// Key is the stable database name ("github", "linuxdo").
	Key string
	// Name is what the login page shows.
	Name string
	// AuthorizeURL is where the browser is sent first.
	AuthorizeURL string
	// TokenURL exchanges the code for an access token.
	TokenURL string
	// UserURL returns the signed-in account.
	UserURL string
	// EmailsURL, when set, is queried only if UserURL returned no address
	// (GitHub hides the address unless the user made it public).
	EmailsURL string
	// Scope is the minimum needed: an identity, no repositories, no email
	// beyond what the provider already exposes.
	Scope string
	// PKCE asks for a code challenge. GitHub documents S256 support; linux.do
	// Connect does not, and an unsupported parameter could fail the redirect.
	PKCE bool
}

// Endpoints are fixed: nothing a user submits ever reaches these URLs, so the
// flow cannot be pointed at an internal host.
var defaults = map[string]Provider{
	GitHub: {
		Key:          GitHub,
		Name:         "GitHub",
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		UserURL:      "https://api.github.com/user",
		EmailsURL:    "https://api.github.com/user/emails",
		Scope:        "read:user user:email",
		PKCE:         true,
	},
	LinuxDo: {
		Key:          LinuxDo,
		Name:         "linux.do",
		AuthorizeURL: "https://connect.linux.do/oauth2/authorize",
		TokenURL:     "https://connect.linux.do/oauth2/token",
		UserURL:      "https://connect.linux.do/api/user",
		Scope:        "user",
		PKCE:         false,
	},
}

// Defaults returns the built-in provider table. The caller may copy and mutate
// it (tests point it at a local server); the map itself is never written to at
// runtime.
func Defaults() map[string]Provider {
	out := make(map[string]Provider, len(defaults))
	maps.Copy(out, defaults)
	return out
}

// Keys returns the provider keys in a stable order (GitHub first, matching the
// login page).
func Keys() []string { return []string{GitHub, LinuxDo} }

// Credentials are one provider's registered OAuth app.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// Profile is the provider account, reduced to what FeedWatch stores.
type Profile struct {
	// Subject is the provider's immutable user id, as a string.
	Subject string
	// Login is the provider-side username; display only, it can be renamed.
	Login string
	// Email may be empty: linux.do does not return one, and GitHub only does
	// when the user published it.
	Email string
}

// AuthorizeURL builds the redirect target. challenge is ignored by providers
// that do not support PKCE, so passing one is always harmless.
func (p Provider) AuthorizeURLFor(creds Credentials, redirectURI, state, challenge string) string {
	u, err := url.Parse(p.AuthorizeURL)
	if err != nil {
		return p.AuthorizeURL
	}
	q := u.Query()
	q.Set("client_id", creds.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	if p.PKCE && challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// tokenResponse is what both token endpoints return. GitHub answers
// form-encoded unless asked for JSON, which is why the request sends
// Accept: application/json.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange trades an authorization code for an access token and then for the
// account behind it. Any non-2xx answer, an error field or an unparsable body
// is reported as ErrExchange with a short detail safe to log.
func (p Provider) Exchange(ctx context.Context, client *http.Client, creds Credentials, code, redirectURI, verifier string) (Profile, error) {
	if client == nil {
		client = http.DefaultClient
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
	}
	if p.PKCE && verifier != "" {
		form.Set("code_verifier", verifier)
	}
	token, err := p.postForm(ctx, client, p.TokenURL, form)
	if err != nil {
		return Profile{}, err
	}
	if token.Error != "" {
		return Profile{}, fmt.Errorf("%w: token endpoint said %s", ErrExchange, token.Error)
	}
	if token.AccessToken == "" {
		return Profile{}, fmt.Errorf("%w: token endpoint returned no access_token", ErrExchange)
	}
	profile, err := p.profile(ctx, client, token.AccessToken)
	if err != nil {
		return Profile{}, err
	}
	if profile.Email == "" && p.EmailsURL != "" {
		// Best effort: an account without a usable address is still a valid
		// identity, so a failed secondary call must not fail the sign-in.
		profile.Email = p.primaryEmail(ctx, client, token.AccessToken)
	}
	return profile, nil
}

func (p Provider) postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: build token request: %v", ErrExchange, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "FeedWatch")
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: token request: %v", ErrExchange, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: read token response: %v", ErrExchange, err)
	}
	if resp.StatusCode/100 != 2 {
		return tokenResponse{}, fmt.Errorf("%w: token endpoint returned HTTP %d", ErrExchange, resp.StatusCode)
	}
	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		// A form-encoded body here means the Accept header was ignored; that
		// is a provider change, not a user error, and must be visible in logs.
		return tokenResponse{}, fmt.Errorf("%w: token response is not JSON", ErrExchange)
	}
	return out, nil
}

func (p Provider) getJSON(ctx context.Context, client *http.Client, endpoint, token string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrExchange, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "FeedWatch")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrExchange, endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrExchange, endpoint, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%w: %s returned HTTP %d", ErrExchange, endpoint, resp.StatusCode)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%w: %s returned unexpected JSON", ErrExchange, endpoint)
	}
	return nil
}

// githubUser and the linux.do payload differ only in the login field name.
type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type linuxDoUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

func (p Provider) profile(ctx context.Context, client *http.Client, token string) (Profile, error) {
	switch p.Key {
	case LinuxDo:
		var u linuxDoUser
		if err := p.getJSON(ctx, client, p.UserURL, token, &u); err != nil {
			return Profile{}, err
		}
		if u.ID <= 0 {
			return Profile{}, ErrNoIdentity
		}
		login := u.Username
		if login == "" {
			login = u.Name
		}
		return Profile{Subject: strconv.FormatInt(u.ID, 10), Login: login, Email: u.Email}, nil
	default:
		var u githubUser
		if err := p.getJSON(ctx, client, p.UserURL, token, &u); err != nil {
			return Profile{}, err
		}
		if u.ID <= 0 {
			return Profile{}, ErrNoIdentity
		}
		return Profile{Subject: strconv.FormatInt(u.ID, 10), Login: u.Login, Email: u.Email}, nil
	}
}

// primaryEmail picks the address GitHub itself would use: the primary one if
// it is verified, otherwise any verified address.
func (p Provider) primaryEmail(ctx context.Context, client *http.Client, token string) string {
	var list []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := p.getJSON(ctx, client, p.EmailsURL, token, &list); err != nil {
		return ""
	}
	fallback := ""
	for _, e := range list {
		if !e.Verified || e.Email == "" {
			continue
		}
		if e.Primary {
			return e.Email
		}
		if fallback == "" {
			fallback = e.Email
		}
	}
	return fallback
}

// GenerateState returns a random state value and the hash to store. The raw
// value travels to the provider and back; only the hash is persisted, so a
// leaked database does not yield usable callbacks.
func GenerateState() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("oauth: generate state: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashState(raw), nil
}

// HashState is the at-rest form of a state value.
func HashState(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// GeneratePKCE returns a code verifier and its S256 challenge. The verifier is
// stored with the flow; only the challenge ever leaves the server.
func GeneratePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("oauth: generate pkce: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
