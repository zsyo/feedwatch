//go:build webui

package api

import (
	"net/http"
	"strings"
	"testing"
)

// The zone is account state: it must survive a new session (a different device),
// be normalised before storage, and never be written when the name is unknown.
func TestPreferencesTimezoneRoundTrip(t *testing.T) {
	ts, db := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")

	me := c.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	user, _ := me["user"].(map[string]any)
	if user["timezone"] != "" {
		t.Fatalf("new account should follow the server, got %v", user["timezone"])
	}

	out := c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": "Asia/Tokyo"}, http.StatusOK)
	if out["timezone"] != "Asia/Tokyo" {
		t.Fatalf("PATCH /api/me = %+v", out)
	}
	var stored string
	if err := db.QueryRow("SELECT timezone FROM users WHERE username='alice'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "Asia/Tokyo" {
		t.Fatalf("stored timezone = %q", stored)
	}

	// A second login is the "another device" case: the value must come back.
	other := login(t, ts, "alice", "alice-pass")
	me = other.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK)
	user, _ = me["user"].(map[string]any)
	if user["timezone"] != "Asia/Tokyo" {
		t.Fatalf("new session did not see the stored zone: %+v", user)
	}

	// Surrounding whitespace is trimmed, so a padded value is not rejected.
	out = c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": "  Europe/Berlin  "}, http.StatusOK)
	if out["timezone"] != "Europe/Berlin" {
		t.Fatalf("normalisation failed: %+v", out)
	}

	// An unknown zone is rejected and leaves the previous value alone.
	bad := c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": "Mars/Olympus"}, http.StatusBadRequest)
	if code, _ := bad["error"].(map[string]any)["code"].(string); code != "invalid_timezone" {
		t.Fatalf("error code = %q", code)
	}
	if err := db.QueryRow("SELECT timezone FROM users WHERE username='alice'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "Europe/Berlin" {
		t.Fatalf("rejected write still changed the row: %q", stored)
	}

	// Empty restores "follow the server".
	out = c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": ""}, http.StatusOK)
	if out["timezone"] != "" {
		t.Fatalf("reset = %+v", out)
	}
}

func TestPreferencesRequireTimezone(t *testing.T) {
	ts, _ := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")

	c.do(http.MethodPatch, "/api/me", map[string]string{}, http.StatusBadRequest)
	c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": strings.Repeat("a", 65)}, http.StatusBadRequest)
	// "Local" is the server's own zone, i.e. the same thing as "follow server".
	out := c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": "Local"}, http.StatusOK)
	if out["timezone"] != "" {
		t.Fatalf("Local should fold to the server default: %+v", out)
	}
}

// Administrators configure no subscriptions, but their own display zone is still
// theirs to set.
func TestPreferencesAllowedForAdmin(t *testing.T) {
	ts, _ := newTestServer(t)
	c := login(t, ts, "admin", "admin-pass")
	out := c.do(http.MethodPatch, "/api/me", map[string]string{"timezone": "UTC"}, http.StatusOK)
	if out["timezone"] != "UTC" {
		t.Fatalf("admin PATCH /api/me = %+v", out)
	}
}

func TestMetaReportsServerZone(t *testing.T) {
	ts, _ := newTestServer(t)
	c := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	out := c.do(http.MethodGet, "/api/meta", nil, http.StatusOK)
	if name, _ := out["server_timezone"].(string); name == "" {
		t.Fatalf("server_timezone missing: %+v", out)
	}
	if offset, _ := out["server_utc_offset"].(string); len(offset) != 6 || offset[0] != '+' && offset[0] != '-' {
		t.Fatalf("server_utc_offset = %v", out["server_utc_offset"])
	}
}
