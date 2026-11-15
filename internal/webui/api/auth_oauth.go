//go:build webui

package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"feedwatch/internal/webui/auth"
	"feedwatch/internal/webui/oauth"
	"feedwatch/internal/webui/store"
)

// Sign-in attempts live in the database for the same reason sessions do: a
// restart in the middle of a redirect must not strand the user, and a leaked
// database must not hand out usable callbacks (only the state hash is stored).
const (
	flowLogin = "login"
	flowBind  = "bind"
	flowTTL   = 10 * time.Minute
)

var (
	errFlowInvalid = errors.New("oauth: unknown, used or expired state")
	errNoIdentity  = errors.New("oauth: provider account is not linked yet")
)

// providerRow is one auth_providers row. SealedSecret is the fwsec envelope:
// it never leaves the server, not even to the administrator who typed it.
type providerRow struct {
	Key          string
	Enabled      bool
	ClientID     string
	SealedSecret string
}

// available means "configured and switched on". The login page only shows a
// button for providers in this state, so a half-filled form looks inert
// instead of leading to a dead end.
func (r providerRow) available() bool {
	return r.Enabled && r.ClientID != "" && r.SealedSecret != ""
}

func secretAAD(provider string) []byte { return []byte("oauth-provider:" + provider) }

// providerRows reads every configured provider in one query. A read failure is
// not fatal: the caller treats the empty map as "nothing is configured".
func (s *Server) providerRows() map[string]providerRow {
	out := make(map[string]providerRow, len(oauth.Keys()))
	rows, err := s.db.Query(`SELECT provider, enabled, client_id, client_secret FROM auth_providers`)
	if err != nil {
		s.log.Warn("webui: read oauth providers failed", "error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var row providerRow
		var enabled int
		if err := rows.Scan(&row.Key, &enabled, &row.ClientID, &row.SealedSecret); err != nil {
			s.log.Warn("webui: scan oauth provider failed", "error", err)
			return out
		}
		row.Enabled = enabled != 0
		out[row.Key] = row
	}
	return out
}

func (s *Server) providerCredentials(key string, row providerRow) (oauth.Credentials, error) {
	if row.SealedSecret == "" {
		return oauth.Credentials{}, errors.New("oauth: no client secret stored")
	}
	secret, err := s.sealer.Open(row.SealedSecret, secretAAD(key))
	if err != nil {
		return oauth.Credentials{}, err
	}
	return oauth.Credentials{ClientID: row.ClientID, ClientSecret: string(secret)}, nil
}

// oauthFlow is one sign-in attempt in flight: what the provider needs to know
// (mode, redirect, PKCE verifier) and, for a binding, which account to attach.
type oauthFlow struct {
	Provider string
	Mode     string
	UserID   int64
	Redirect string
	Verifier string
}

func (s *Server) createOAuthFlow(provider, mode, redirect, verifier string, userID int64) (string, error) {
	raw, hash, err := oauth.GenerateState()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	// Abandoned attempts are worthless; clearing them here keeps the table
	// small without a background job of its own.
	if _, err := s.db.Exec(`DELETE FROM oauth_flows WHERE expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		s.log.Warn("webui: purge oauth flows failed", "error", err)
	}
	var owner any
	if userID > 0 {
		owner = userID
	}
	if _, err := s.db.Exec(`INSERT INTO oauth_flows
		(state_hash, provider, mode, user_id, redirect, code_verifier, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		hash, provider, mode, owner, redirect, verifier,
		now.Format(time.RFC3339), now.Add(flowTTL).Format(time.RFC3339)); err != nil {
		return "", err
	}
	return raw, nil
}

// consumeOAuthFlow reads and deletes one attempt. Single use is enforced by the
// delete before the checks: a replayed callback finds nothing even if the first
// one failed halfway.
func (s *Server) consumeOAuthFlow(raw string) (oauthFlow, error) {
	if raw == "" {
		return oauthFlow{}, errFlowInvalid
	}
	hash := oauth.HashState(raw)
	var flow oauthFlow
	var owner sql.NullInt64
	var expires string
	err := s.db.QueryRow(`SELECT provider, mode, user_id, redirect, code_verifier, expires_at
		FROM oauth_flows WHERE state_hash=?`, hash).
		Scan(&flow.Provider, &flow.Mode, &owner, &flow.Redirect, &flow.Verifier, &expires)
	if _, delErr := s.db.Exec(`DELETE FROM oauth_flows WHERE state_hash=?`, hash); delErr != nil {
		s.log.Warn("webui: delete oauth flow failed", "error", delErr)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return oauthFlow{}, errFlowInvalid
	}
	if err != nil {
		return oauthFlow{}, err
	}
	if owner.Valid {
		flow.UserID = owner.Int64
	}
	deadline, perr := time.Parse(time.RFC3339, expires)
	if perr != nil || time.Now().UTC().After(deadline) {
		return oauthFlow{}, errFlowInvalid
	}
	return flow, nil
}

// baseURL reconstructs the URL the browser used. The redirect_uri registered at
// the provider must match byte for byte, so a reverse proxy that terminates TLS
// has to pass X-Forwarded-Proto/Host — the admin page shows the computed value
// to copy.
func (s *Server) baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := firstForwarded(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
		scheme = proto
	}
	host := r.Host
	if fwd := firstForwarded(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

func firstForwarded(value string) string {
	if i := strings.IndexByte(value, ','); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

func (s *Server) oauthRedirectURI(r *http.Request, provider string) string {
	return s.baseURL(r) + "/api/auth/oauth/" + provider + "/callback"
}

// safeRedirect keeps post-sign-in navigation on this site: the target arrives
// in a query string and is handed straight to http.Redirect.
func safeRedirect(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	if strings.HasPrefix(raw, "/api/") || strings.HasPrefix(raw, "/login") {
		return "/"
	}
	return raw
}

// oauthFail sends the browser back to a page that can explain what happened.
// A bind failure belongs on the account page the user started from; a login
// failure belongs on the login page.
func (s *Server) oauthFail(w http.ResponseWriter, r *http.Request, mode, code string) {
	target := "/login?oauth_error=" + url.QueryEscape(code)
	if mode == flowBind {
		target = "/settings?bind_error=" + url.QueryEscape(code)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// oauthStart builds the provider URL and remembers the attempt.
//
// mode=bind requires a session. The cookie is SameSite=Strict, so a cross-site
// request (an <img> on another site, say) cannot reach this with a session: it
// would be treated as a plain login attempt, never as a binding.
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request, c Ctx) {
	key := r.PathValue("provider")
	provider, ok := s.oauthProviders[key]
	if !ok {
		fail(w, http.StatusNotFound, "unknown_provider", "unknown sign-in provider")
		return
	}
	mode := flowLogin
	var userID int64
	if r.URL.Query().Get("mode") == flowBind {
		if c.User.ID == 0 {
			http.Redirect(w, r, "/login?redirect=%2Fsettings", http.StatusSeeOther)
			return
		}
		// Administrators have no third-party sign-in at all.
		if auth.IsAdmin(c.User) {
			s.oauthFail(w, r, flowBind, "admin_not_allowed")
			return
		}
		mode, userID = flowBind, c.User.ID
	}

	row, ok := s.providerRows()[key]
	if !ok || !row.available() {
		s.oauthFail(w, r, mode, "not_configured")
		return
	}
	creds, err := s.providerCredentials(key, row)
	if err != nil {
		// A secret that cannot be decrypted means the master key changed. Say
		// "not configured" in the UI and leave the real reason in the log.
		s.log.Error("webui: oauth client secret unreadable", "provider", key, "error", err)
		s.oauthFail(w, r, mode, "not_configured")
		return
	}

	var verifier, challenge string
	if provider.PKCE {
		if verifier, challenge, err = oauth.GeneratePKCE(); err != nil {
			s.log.Error("webui: generate pkce failed", "error", err)
			s.oauthFail(w, r, mode, "state_failed")
			return
		}
	}
	state, err := s.createOAuthFlow(key, mode, safeRedirect(r.URL.Query().Get("redirect")), verifier, userID)
	if err != nil {
		s.log.Error("webui: create oauth flow failed", "error", err)
		s.oauthFail(w, r, mode, "state_failed")
		return
	}
	http.Redirect(w, r, provider.AuthorizeURLFor(creds, s.oauthRedirectURI(r, key), state, challenge), http.StatusFound)
}

// oauthCallback completes the flow. It is public: the state row is what proves
// the callback belongs to a sign-in this server started, and for a binding it
// also carries which account to attach to.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request, c Ctx) {
	key := r.PathValue("provider")
	provider, ok := s.oauthProviders[key]
	if !ok {
		fail(w, http.StatusNotFound, "unknown_provider", "unknown sign-in provider")
		return
	}
	query := r.URL.Query()
	flow, err := s.consumeOAuthFlow(query.Get("state"))
	if err != nil || flow.Provider != key {
		if err != nil && !errors.Is(err, errFlowInvalid) {
			s.log.Warn("webui: consume oauth flow failed", "provider", key, "error", err)
		}
		s.oauthFail(w, r, flowLogin, "state_invalid")
		return
	}
	if query.Get("error") != "" {
		s.log.Info("webui: oauth authorization denied", "provider", key, "error", query.Get("error"))
		s.oauthFail(w, r, flow.Mode, "denied")
		return
	}
	code := query.Get("code")
	if code == "" {
		s.oauthFail(w, r, flow.Mode, "state_invalid")
		return
	}
	row, ok := s.providerRows()[key]
	if !ok || !row.available() {
		s.oauthFail(w, r, flow.Mode, "not_configured")
		return
	}
	creds, err := s.providerCredentials(key, row)
	if err != nil {
		s.log.Error("webui: oauth client secret unreadable", "provider", key, "error", err)
		s.oauthFail(w, r, flow.Mode, "not_configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	profile, err := provider.Exchange(ctx, s.httpClient, creds, code, s.oauthRedirectURI(r, key), flow.Verifier)
	if err != nil {
		s.log.Warn("webui: oauth exchange failed", "provider", key, "error", err)
		s.oauthFail(w, r, flow.Mode, "exchange_failed")
		return
	}

	if flow.Mode == flowBind {
		s.finishBinding(w, r, flow, profile)
		return
	}
	s.finishOAuthLogin(w, r, c, flow, profile)
}

// finishBinding attaches the provider account to the user who started the flow.
// The user id comes from the state row, never from the query string, so a
// tampered callback cannot bind an identity to somebody else.
func (s *Server) finishBinding(w http.ResponseWriter, r *http.Request, flow oauthFlow, profile oauth.Profile) {
	if flow.UserID == 0 {
		s.oauthFail(w, r, flowBind, "state_invalid")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.log.Error("webui: begin bind failed", "error", err)
		s.oauthFail(w, r, flowBind, "account_failed")
		return
	}
	defer tx.Rollback()

	now := store.Now()
	var owner int64
	err = tx.QueryRow(`SELECT user_id FROM user_identities WHERE provider=? AND subject=?`,
		flow.Provider, profile.Subject).Scan(&owner)
	switch {
	case err == nil && owner != flow.UserID:
		s.oauthFail(w, r, flowBind, "already_linked")
		return
	case err == nil:
		// Re-linking the same provider account: refresh the display fields.
		if _, err := tx.Exec(`UPDATE user_identities SET login=?, email=?, updated_at=?
			WHERE provider=? AND user_id=?`,
			profile.Login, profile.Email, now, flow.Provider, flow.UserID); err != nil {
			s.log.Error("webui: refresh identity failed", "error", err)
			s.oauthFail(w, r, flowBind, "account_failed")
			return
		}
	case !errors.Is(err, sql.ErrNoRows):
		s.log.Error("webui: look up identity failed", "error", err)
		s.oauthFail(w, r, flowBind, "account_failed")
		return
	default:
		if _, err := tx.Exec(`INSERT INTO user_identities
			(user_id, provider, subject, login, email, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?)`,
			flow.UserID, flow.Provider, profile.Subject, profile.Login, profile.Email, now, now); err != nil {
			// unique (provider, user_id): this account already has a different
			// account from the same provider linked.
			if tx.Dialect().IsUniqueViolation(err) {
				s.oauthFail(w, r, flowBind, "already_linked_other")
				return
			}
			s.log.Error("webui: insert identity failed", "error", err)
			s.oauthFail(w, r, flowBind, "account_failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.log.Error("webui: commit bind failed", "error", err)
		s.oauthFail(w, r, flowBind, "account_failed")
		return
	}
	http.Redirect(w, r, "/settings?bind=ok&provider="+url.QueryEscape(flow.Provider), http.StatusSeeOther)
}

// finishOAuthLogin signs in the account behind the provider identity, creating
// one when the site allows it. A brand-new account is active immediately: the
// provider already verified who this is.
func (s *Server) finishOAuthLogin(w http.ResponseWriter, r *http.Request, c Ctx, flow oauthFlow, profile oauth.Profile) {
	userID, err := s.identityOwner(flow.Provider, profile.Subject)
	switch {
	case errors.Is(err, errNoIdentity):
		if !s.registrationEnabled() {
			s.oauthFail(w, r, flowLogin, "registration_closed")
			return
		}
		if userID, err = s.createIdentityUser(flow.Provider, profile); err != nil {
			s.log.Error("webui: create oauth account failed", "provider", flow.Provider, "error", err)
			s.oauthFail(w, r, flowLogin, "account_failed")
			return
		}
	case err != nil:
		s.log.Error("webui: look up identity failed", "error", err)
		s.oauthFail(w, r, flowLogin, "account_failed")
		return
	}

	u, err := s.auth.LoadUser(userID)
	if err != nil {
		s.log.Error("webui: load oauth user failed", "user", userID, "error", err)
		s.oauthFail(w, r, flowLogin, "account_failed")
		return
	}
	if !u.IsActive {
		s.oauthFail(w, r, flowLogin, "user_disabled")
		return
	}
	// Third-party sign-in exists for regular users only. An administrator
	// account keeps its password and is not reachable through a provider.
	if auth.IsAdmin(u) {
		s.oauthFail(w, r, flowLogin, "admin_not_allowed")
		return
	}
	// Signing in again from the same browser replaces the session; the old row
	// must not stay usable.
	if raw := auth.TokenFromRequest(r); raw != "" && c.Session.TokenHash != "" {
		if err := s.auth.DeleteSession(raw); err != nil {
			s.log.Warn("webui: delete previous session failed", "error", err)
		}
	}
	raw, _, err := s.auth.CreateSession(userID)
	if err != nil {
		s.log.Error("webui: create session failed", "error", err)
		s.oauthFail(w, r, flowLogin, "session_failed")
		return
	}
	http.SetCookie(w, s.auth.SessionCookie(raw))
	http.Redirect(w, r, safeRedirect(flow.Redirect), http.StatusSeeOther)
}

func (s *Server) identityOwner(provider, subject string) (int64, error) {
	var userID int64
	err := s.db.QueryRow(`SELECT user_id FROM user_identities WHERE provider=? AND subject=?`,
		provider, subject).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errNoIdentity
	}
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// createIdentityUser creates the account and its binding in one transaction, so
// a failure never leaves a user who cannot sign in any way.
func (s *Server) createIdentityUser(provider string, profile oauth.Profile) (int64, error) {
	base := deriveUsername(provider, profile.Login)
	now := store.Now()
	for attempt := 0; attempt < 10; attempt++ {
		username := base
		if attempt > 0 {
			username = fmt.Sprintf("%s-%d", base, attempt+1)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return 0, err
		}
		// No password: the account can only be entered through the provider
		// until the user sets one in the account page.
		id, err := tx.InsertID(`INSERT INTO users
			(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
			VALUES (?, '', 'user', 1, 0, ?, ?)`, username, now, now)
		if err != nil {
			tx.Rollback()
			if tx.Dialect().IsUniqueViolation(err) {
				continue // username taken; try the next suffix
			}
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO user_identities
			(user_id, provider, subject, login, email, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?)`,
			id, provider, profile.Subject, profile.Login, profile.Email, now, now); err != nil {
			tx.Rollback()
			if tx.Dialect().IsUniqueViolation(err) {
				// Two simultaneous first sign-ins for the same provider
				// account: the loser adopts the winner's rows.
				if owner, lookupErr := s.identityOwner(provider, profile.Subject); lookupErr == nil {
					return owner, nil
				}
				continue
			}
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return id, nil
	}
	return 0, fmt.Errorf("oauth: no free username derived from %q", profile.Login)
}

// deriveUsername turns a provider login into a username. Only characters that
// are unambiguous to type and to compare survive; when nothing usable is left
// (a purely CJK login, say) the provider name plus a counter takes over.
func deriveUsername(provider, login string) string {
	var b strings.Builder
	for _, r := range login {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= 24 {
			break
		}
	}
	name := strings.Trim(b.String(), "-_.")
	if len(name) < minUsernameLen {
		return provider + "-user"
	}
	return name
}

// listIdentities backs the account page: which providers this account can use,
// whether a password exists, and what is already linked.
func (s *Server) listIdentities(w http.ResponseWriter, _ *http.Request, c Ctx) {
	type identity struct {
		Provider  string `json:"provider"`
		Login     string `json:"login"`
		Email     string `json:"email"`
		CreatedAt string `json:"created_at"`
	}
	identities := make([]identity, 0, 2)
	bound := make(map[string]bool, 2)
	rows, err := s.db.Query(`SELECT provider, login, email, created_at
		FROM user_identities WHERE user_id=? ORDER BY provider`, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	for rows.Next() {
		var item identity
		if err := rows.Scan(&item.Provider, &item.Login, &item.Email, &item.CreatedAt); err != nil {
			rows.Close()
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		identities = append(identities, item)
		bound[item.Provider] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}

	// A second query, so the rows above are closed first: SQLite serves one
	// connection and a nested query would wait on itself.
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id=?`, c.User.ID).Scan(&hash); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}

	type providerView struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Bound     bool   `json:"bound"`
	}
	state := s.providerRows()
	providers := make([]providerView, 0, len(oauth.Keys()))
	for _, key := range oauth.Keys() {
		providers = append(providers, providerView{
			Key: key, Name: s.oauthProviders[key].Name,
			Available: state[key].available(), Bound: bound[key],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"password_set": hash != "",
		"identities":   identities,
		"providers":    providers,
	})
}

// unlinkIdentity removes one binding. It refuses to remove the last way in: an
// account with no password whose only provider is unlinked would be lost.
func (s *Server) unlinkIdentity(w http.ResponseWriter, r *http.Request, c Ctx) {
	key := r.PathValue("provider")
	if _, ok := s.oauthProviders[key]; !ok {
		fail(w, http.StatusNotFound, "unknown_provider", "unknown sign-in provider")
		return
	}
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id=?`, c.User.ID).Scan(&hash); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_identities WHERE user_id=?`, c.User.ID).Scan(&total); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if hash == "" && total <= 1 {
		fail(w, http.StatusBadRequest, "last_login_method",
			"这是账号唯一的登录方式，请先在下方设置登录密码再解绑")
		return
	}
	res, err := s.db.Exec(`DELETE FROM user_identities WHERE user_id=? AND provider=?`, c.User.ID, key)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, http.StatusNotFound, "not_found", "该登录方式尚未绑定")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminProviderView is what the administrator sees. The secret is reduced to a
// boolean: it can be replaced, never read back.
type adminProviderView struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"client_id"`
	SecretSet   bool   `json:"secret_set"`
	Available   bool   `json:"available"`
	CallbackURL string `json:"callback_url"`
	Scope       string `json:"scope"`
	PKCE        bool   `json:"pkce"`
}

func (s *Server) adminAuthResponse(r *http.Request) map[string]any {
	state := s.providerRows()
	providers := make([]adminProviderView, 0, len(oauth.Keys()))
	for _, key := range oauth.Keys() {
		row := state[key]
		providers = append(providers, adminProviderView{
			Key: key, Name: s.oauthProviders[key].Name,
			Enabled: row.Enabled, ClientID: row.ClientID,
			SecretSet: row.SealedSecret != "", Available: row.available(),
			CallbackURL: s.oauthRedirectURI(r, key),
			Scope:       s.oauthProviders[key].Scope,
			PKCE:        s.oauthProviders[key].PKCE,
		})
	}
	return map[string]any{
		"registration_enabled": s.registrationEnabled(),
		"providers":            providers,
	}
}

func (s *Server) adminGetAuth(w http.ResponseWriter, r *http.Request, _ Ctx) {
	writeJSON(w, http.StatusOK, s.adminAuthResponse(r))
}

// adminPutAuth changes the registration switch and/or a provider's settings.
// Everything is validated before anything is written: a provider cannot be
// enabled while its credentials are incomplete.
func (s *Server) adminPutAuth(w http.ResponseWriter, r *http.Request, _ Ctx) {
	var req struct {
		RegistrationEnabled *bool `json:"registration_enabled"`
		Providers           map[string]struct {
			Enabled      *bool   `json:"enabled"`
			ClientID     *string `json:"client_id"`
			ClientSecret *string `json:"client_secret"`
		} `json:"providers"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.RegistrationEnabled == nil && len(req.Providers) == 0 {
		fail(w, http.StatusBadRequest, "empty_patch", "nothing to update")
		return
	}
	for key := range req.Providers {
		if _, ok := s.oauthProviders[key]; !ok {
			fail(w, http.StatusBadRequest, "unknown_provider", "unknown sign-in provider: "+key)
			return
		}
	}

	// Read the current rows before opening the transaction: SQLite runs on a
	// single connection, so a query issued while the transaction holds it would
	// wait for itself forever.
	state := s.providerRows()
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()

	now := store.Now()
	for key, change := range req.Providers {
		row := state[key]
		if change.ClientID != nil {
			row.ClientID = strings.TrimSpace(*change.ClientID)
		}
		if change.Enabled != nil {
			row.Enabled = *change.Enabled
		}
		// An empty secret field means "keep the stored one": the form never
		// receives the current value, so an unnoticed empty input must not
		// wipe a working configuration.
		if change.ClientSecret != nil {
			if secret := strings.TrimSpace(*change.ClientSecret); secret != "" {
				sealed, err := s.sealer.Seal([]byte(secret), secretAAD(key))
				if err != nil {
					s.log.Error("webui: seal oauth secret failed", "provider", key, "error", err)
					fail(w, http.StatusInternalServerError, "seal_failed", err.Error())
					return
				}
				row.SealedSecret = sealed
			}
		}
		if row.Enabled && (row.ClientID == "" || row.SealedSecret == "") {
			fail(w, http.StatusBadRequest, "provider_incomplete",
				"启用 "+s.oauthProviders[key].Name+" 前需要同时填写 Client ID 与 Client Secret")
			return
		}
		if _, err := tx.Upsert(`INSERT INTO auth_providers
			(provider, enabled, client_id, client_secret, updated_at) VALUES (?,?,?,?,?)`,
			"provider", []string{"enabled", "client_id", "client_secret", "updated_at"},
			key, boolToInt(row.Enabled), row.ClientID, row.SealedSecret, now); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}

	if req.RegistrationEnabled != nil {
		value := "false"
		if *req.RegistrationEnabled {
			value = "true"
		}
		if _, err := tx.Upsert(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
			"setting_key", []string{"value", "updated_at"}, settingAllowRegistration, value, now); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.adminAuthResponse(r))
}

// adminClearProvider forgets a provider's credentials. The switch goes off with
// them, so a stale secret cannot stay usable after an administrator asked for
// it to be removed.
func (s *Server) adminClearProvider(w http.ResponseWriter, r *http.Request, _ Ctx) {
	key := r.PathValue("provider")
	if _, ok := s.oauthProviders[key]; !ok {
		fail(w, http.StatusNotFound, "unknown_provider", "unknown sign-in provider")
		return
	}
	if _, err := s.db.Exec(`UPDATE auth_providers
		SET enabled=0, client_id='', client_secret='', updated_at=? WHERE provider=?`,
		store.Now(), key); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.adminAuthResponse(r))
}
