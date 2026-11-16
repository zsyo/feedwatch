//go:build webui

package api

import (
	"net/http"
	"strings"
	"testing"

	"feedwatch/internal/webui/secrets"
)

func TestGithubTokenIsSealedAndNeverEchoed(t *testing.T) {
	ts, db := newTestServer(t)
	admin := login(t, ts, "admin", "admin-pass")

	out := admin.do(http.MethodGet, "/api/admin/github-token", nil, http.StatusOK)
	if out["set"] != false {
		t.Fatalf("fresh install should have no token: %+v", out)
	}
	out = admin.do(http.MethodPut, "/api/admin/github-token", map[string]any{
		"token": "ghp_supersecret", "graphql": true,
	}, http.StatusOK)
	if out["set"] != true {
		t.Fatalf("PUT should report a stored token: %+v", out)
	}

	// The raw token and its sealed envelope must be absent from every read.
	settings := admin.do(http.MethodGet, "/api/admin/settings", nil, http.StatusOK)
	raw, _ := settings["settings"].([]any)
	for _, row := range raw {
		m := row.(map[string]any)
		if strings.Contains(m["key"].(string), "github") {
			t.Errorf("github key leaked into generic settings: %+v", m)
		}
		if strings.Contains(m["value"].(string), "ghp_supersecret") {
			t.Errorf("token leaked into generic settings")
		}
	}
	token := admin.do(http.MethodGet, "/api/admin/github-token", nil, http.StatusOK)
	if token["set"] != true {
		t.Errorf("token should still be set: %+v", token)
	}
	for _, v := range token {
		if s, ok := v.(string); ok && strings.Contains(s, "ghp_") {
			t.Errorf("token detail leaked: %+v", token)
		}
	}

	// The stored value is a sealed envelope, not plaintext.
	var stored string
	if err := db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`,
		secrets.GithubTokenSettingKey).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "fwsec:v1:") || strings.Contains(stored, "ghp_") {
		t.Errorf("stored token is not sealed: %q", stored)
	}
	var graphql string
	if err := db.QueryRow(`SELECT value FROM settings WHERE setting_key='github.graphql'`).Scan(&graphql); err != nil {
		t.Fatal(err)
	}
	if graphql != "true" {
		t.Errorf("graphql toggle = %q, want true", graphql)
	}

	// Clearing with an empty token removes the credential.
	admin.do(http.MethodPut, "/api/admin/github-token", map[string]any{"token": ""}, http.StatusOK)
	out = admin.do(http.MethodGet, "/api/admin/github-token", nil, http.StatusOK)
	if out["set"] != false {
		t.Errorf("token should be cleared: %+v", out)
	}
}

func TestGithubTokenRequiresAdmin(t *testing.T) {
	ts, _ := newTestServer(t)
	member := login(t, ts, "alice", "alice-pass")
	member.do(http.MethodGet, "/api/admin/github-token", nil, http.StatusForbidden)
	member.do(http.MethodPut, "/api/admin/github-token", map[string]any{"token": "x"}, http.StatusForbidden)
}
