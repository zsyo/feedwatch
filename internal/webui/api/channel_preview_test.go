//go:build webui

package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestChannelPreviewEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)

	body := map[string]any{
		"type":   "telegram",
		"kind":   "new_releases",
		"fields": map[string]string{"parse_mode": "Markdown"},
	}
	anon := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	anon.do(http.MethodPost, "/api/channels/preview", body, http.StatusUnauthorized)

	c := login(t, ts, "alice", "alice-pass")
	out := c.do(http.MethodPost, "/api/channels/preview", body, http.StatusOK)
	subject, _ := out["subject"].(string)
	if !strings.Contains(subject, "go1.27.4") {
		t.Fatalf("subject = %q", subject)
	}
	if out["format"] != "markdown" || out["kind"] != "new_releases" {
		t.Fatalf("unexpected preview metadata: %+v", out)
	}
	if payload, _ := out["payload"].(string); !strings.Contains(payload, `"parse_mode": "Markdown"`) {
		t.Fatalf("payload = %s", payload)
	}

	// subject_prefix is an email field; verify it reaches the sample subject.
	mail := c.do(http.MethodPost, "/api/channels/preview", map[string]any{
		"type": "email", "fields": map[string]string{"subject_prefix": "[FW]"},
	}, http.StatusOK)
	if subject, _ := mail["subject"].(string); !strings.HasPrefix(subject, "[FW] ") {
		t.Fatalf("email subject = %q", subject)
	}

	bad := c.do(http.MethodPost, "/api/channels/preview",
		map[string]any{"type": "nope"}, http.StatusBadRequest)
	if got := errorCode(t, bad); got != "invalid_channel_type" {
		t.Fatalf("error code = %q", got)
	}

	// Administrators do not own channels, so they do not get the form preview.
	admin := login(t, ts, "admin", "admin-pass")
	admin.do(http.MethodPost, "/api/channels/preview", body, http.StatusForbidden)
}
