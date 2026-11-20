//go:build webui

package api

import (
	"net/http"
	"testing"

	"feedwatch/internal/webui/store"
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

// seedSubscription inserts a subscription directly with explicit enabled and
// deleted_at state. key must be unique per subscription.
func seedSubscription(t *testing.T, db *store.DB, userID int64, key string, enabled, deleted bool) {
	t.Helper()
	var deletedAt any
	if deleted {
		deletedAt = store.Now()
	}
	mustExec(t, db, `INSERT INTO subscriptions (user_id, url, url_key, enabled, deleted_at)
		VALUES (?,?,?,?,?)`, userID, "https://example.com/"+key+".atom", key, boolInt(enabled), deletedAt)
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

func TestChannelDeletionWithoutSubscriptionsAllowed(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	first := seedEnabledChannel(t, db, aliceID)
	second := seedEnabledChannel(t, db, aliceID)
	c := login(t, ts, "alice", "alice-pass")

	c.do(http.MethodDelete, "/api/channels/"+itoa(first), nil, http.StatusOK)
	// No subscriptions: deleting the last enabled channel is now allowed.
	c.do(http.MethodDelete, "/api/channels/"+itoa(second), nil, http.StatusOK)
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM channels`); got != 0 {
		t.Fatalf("channels = %d, want 0", got)
	}
}

func TestDeleteLastChannelWithSubscriptionRejected(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	id := seedEnabledChannel(t, db, aliceID)
	seedSubscription(t, db, aliceID, "sub-live", true, false)
	c := login(t, ts, "alice", "alice-pass")

	out := c.do(http.MethodDelete, "/api/channels/"+itoa(id), nil, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastChannelSubscribedCode {
		t.Fatalf("error code = %q, want %s", got, lastChannelSubscribedCode)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM channels`); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
}

func TestDeletedSubscriptionDoesNotBlockDeletion(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	id := seedEnabledChannel(t, db, aliceID)
	// A soft-deleted subscription must not block removal of the last channel.
	seedSubscription(t, db, aliceID, "sub-deleted", true, true)
	c := login(t, ts, "alice", "alice-pass")

	c.do(http.MethodDelete, "/api/channels/"+itoa(id), nil, http.StatusOK)
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM channels`); got != 0 {
		t.Fatalf("channels = %d, want 0", got)
	}
}

func TestDisabledSubscriptionBlocksDeletion(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	id := seedEnabledChannel(t, db, aliceID)
	// "stopped subscriptions count too": enabled=0 still blocks.
	seedSubscription(t, db, aliceID, "sub-stopped", false, false)
	c := login(t, ts, "alice", "alice-pass")

	out := c.do(http.MethodDelete, "/api/channels/"+itoa(id), nil, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastChannelSubscribedCode {
		t.Fatalf("error code = %q, want %s", got, lastChannelSubscribedCode)
	}
}

func TestDeletingChannelWithAnotherEnabledStaysAllowed(t *testing.T) {
	ts, db := newTestServer(t)
	aliceID := userIDByName(t, db, "alice")
	first := seedEnabledChannel(t, db, aliceID)
	second := seedEnabledChannel(t, db, aliceID)
	seedSubscription(t, db, aliceID, "sub-kept", true, false)
	c := login(t, ts, "alice", "alice-pass")

	// Subscription present, but deleting one of two enabled channels is fine.
	c.do(http.MethodDelete, "/api/channels/"+itoa(first), nil, http.StatusOK)
	// Now the remaining one is the last enabled channel -> guarded.
	out := c.do(http.MethodDelete, "/api/channels/"+itoa(second), nil, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastChannelSubscribedCode {
		t.Fatalf("error code = %q, want %s", got, lastChannelSubscribedCode)
	}
}

func TestDeleteLastChannelGuardIsPerUser(t *testing.T) {
	ts, db := newTestServer(t)
	mustUser(t, db, "bob", "bob-pass", "user", 0)
	aliceID := userIDByName(t, db, "alice")
	bobID := userIDByName(t, db, "bob")
	aliceCh := seedEnabledChannel(t, db, aliceID)
	bobCh := seedEnabledChannel(t, db, bobID)
	// Only bob has a subscription; alice's last channel stays deletable.
	seedSubscription(t, db, bobID, "sub-bob", true, false)

	alice := login(t, ts, "alice", "alice-pass")
	alice.do(http.MethodDelete, "/api/channels/"+itoa(aliceCh), nil, http.StatusOK)

	bob := login(t, ts, "bob", "bob-pass")
	out := bob.do(http.MethodDelete, "/api/channels/"+itoa(bobCh), nil, http.StatusBadRequest)
	if got := errorCode(t, out); got != lastChannelSubscribedCode {
		t.Fatalf("error code = %q, want %s", got, lastChannelSubscribedCode)
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
