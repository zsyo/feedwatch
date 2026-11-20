//go:build webui

package api

import (
	"net/http"
	"testing"
)

func errorCode(t *testing.T, out map[string]any) string {
	t.Helper()
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in response: %+v", out)
	}
	code, _ := errObj["code"].(string)
	return code
}

func TestSubscribeRequiresEnabledChannel(t *testing.T) {
	ts, db := newTestServer(t)
	c := login(t, ts, "alice", "alice-pass")

	// No channel at all: subscribe is rejected with the dedicated code.
	out := c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusConflict)
	if got := errorCode(t, out); got != "no_enabled_channel" {
		t.Fatalf("error code = %q, want no_enabled_channel", got)
	}

	seedEnabledChannel(t, db, userIDByName(t, db, "alice"))
	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusCreated)
}

func TestGateChecksEnabledFlagOnly(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	// A disabled channel does not satisfy the gate: only enabled=1 counts.
	mustExec(t, db, `INSERT INTO channels (user_id, type, name, config_json, enabled)
		VALUES (?, 'log', 'test', '{}', 0)`, aliceID)
	c := login(t, ts, "alice", "alice-pass")
	out := c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusConflict)
	if got := errorCode(t, out); got != "no_enabled_channel" {
		t.Fatalf("error code = %q, want no_enabled_channel", got)
	}

	mustExec(t, db, `UPDATE channels SET enabled=1 WHERE user_id=?`, aliceID)
	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusCreated)
}

func TestGateIsPerUser(t *testing.T) {
	ts, db := newTestServer(t)
	mustUser(t, db, "bob", "bob-pass", "user", 0)
	seedEnabledChannel(t, db, userIDByName(t, db, "alice"))

	alice := login(t, ts, "alice", "alice-pass")
	alice.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusCreated)

	// Bob has no channel of his own; alice's channel must not help him.
	bob := login(t, ts, "bob", "bob-pass")
	out := bob.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/b.atom"}, http.StatusConflict)
	if got := errorCode(t, out); got != "no_enabled_channel" {
		t.Fatalf("error code = %q, want no_enabled_channel", got)
	}
}

func TestQuickSubscribeRequiresEnabledChannel(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	entryID, _ := insertCatalogEntry(t, db, "示例项目", "https://example.com/feed.atom", cat)
	c := login(t, ts, "alice", "alice-pass")
	out := c.do(http.MethodPost, "/api/catalog/"+itoa(entryID)+"/subscribe",
		map[string]any{}, http.StatusConflict)
	if got := errorCode(t, out); got != "no_enabled_channel" {
		t.Fatalf("error code = %q, want no_enabled_channel", got)
	}
}

func TestChannelDeletionKeepsOneEnabled(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	first := seedEnabledChannel(t, db, aliceID)
	second := seedEnabledChannel(t, db, aliceID)
	c := login(t, ts, "alice", "alice-pass")

	c.do(http.MethodDelete, "/api/channels/"+itoa(first), nil, http.StatusOK)
	out := c.do(http.MethodDelete, "/api/channels/"+itoa(second), nil, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastEnabledChannelCode {
		t.Fatalf("error code = %q, want %s", got, lastEnabledChannelCode)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM channels`); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
}

func TestMemberToggleCannotDisableLastChannel(t *testing.T) {
	ts, db := newTestServer(t)
	id := seedEnabledChannel(t, db, userIDByName(t, db, "alice"))
	c := login(t, ts, "alice", "alice-pass")
	out := c.do(http.MethodPatch, "/api/channels/"+itoa(id),
		map[string]any{"enabled": false}, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastEnabledChannelCode {
		t.Fatalf("error code = %q, want %s", got, lastEnabledChannelCode)
	}
}

func TestFullEditCannotBypassChannelGuard(t *testing.T) {
	ts, db := newTestServer(t)
	id := seedEnabledChannel(t, db, userIDByName(t, db, "alice"))
	c := login(t, ts, "alice", "alice-pass")
	// A full form PATCH carrying enabled:false must hit the same guard as the
	// standalone toggle, before the transaction begins. Use a UI-supported type
	// (log is internal-only and rejected by FieldsOf).
	out := c.do(http.MethodPatch, "/api/channels/"+itoa(id), map[string]any{
		"type": "telegram", "name": "renamed",
		"fields":  map[string]string{"chat_id": "123"},
		"secrets": map[string]string{"token": "123:abc"},
		"enabled": false,
	}, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastEnabledChannelCode {
		t.Fatalf("error code = %q, want %s", got, lastEnabledChannelCode)
	}
}

func TestAdminCannotDisableLastEnabledChannel(t *testing.T) {
	ts, db := newAdminTestServer(t, stubAssets())
	aliceID := userIDByName(t, db, "alice")
	id := seedEnabledChannel(t, db, aliceID)
	admin := login(t, ts, "admin", "admin-pass")
	out := admin.do(http.MethodPatch,
		"/api/admin/users/"+itoa(aliceID)+"/channels/"+itoa(id),
		map[string]any{"enabled": false}, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastEnabledChannelCode {
		t.Fatalf("error code = %q, want %s", got, lastEnabledChannelCode)
	}

	// With a second channel present, the admin can disable one.
	seedEnabledChannel(t, db, aliceID)
	admin.do(http.MethodPatch,
		"/api/admin/users/"+itoa(aliceID)+"/channels/"+itoa(id),
		map[string]any{"enabled": false}, http.StatusOK)
}
