//go:build webui

package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type baselineCall struct {
	userID  int64
	urlKey  string
	enabled string // source_type, just to prove fields were passed
}

type fakeBaseline struct {
	mu    sync.Mutex
	calls []baselineCall
	ch    chan baselineCall
}

func newFakeBaseline() *fakeBaseline {
	return &fakeBaseline{ch: make(chan baselineCall, 16)}
}

func (f *fakeBaseline) PushBaseline(_ context.Context, userID int64, _ string, urlKey, sourceType, _, _ string) error {
	c := baselineCall{userID: userID, urlKey: urlKey, enabled: sourceType}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	f.ch <- c
	return nil
}

func (f *fakeBaseline) wait(t *testing.T) baselineCall {
	t.Helper()
	select {
	case c := <-f.ch:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for baseline push")
		return baselineCall{}
	}
}

func (f *fakeBaseline) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestSubscribeTriggersBaseline(t *testing.T) {
	fake := newFakeBaseline()
	ts, db := newTestServer(t, WithBaseline(fake))
	seedEnabledChannel(t, db, userIDByName(t, db, "alice"))
	c := login(t, ts, "alice", "alice-pass")

	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom", "display_name": "Go"}, http.StatusCreated)

	call := fake.wait(t)
	if call.userID != userIDByName(t, db, "alice") {
		t.Fatalf("baseline user = %d", call.userID)
	}
	if call.urlKey == "" || call.enabled != "atom" {
		t.Fatalf("baseline call missing fields: %+v", call)
	}
}

func TestReEnableTriggersBaselineButOtherPatchesDoNot(t *testing.T) {
	fake := newFakeBaseline()
	ts, db := newTestServer(t, WithBaseline(fake))
	aliceID := userIDByName(t, db, "alice")
	seedEnabledChannel(t, db, aliceID)
	c := login(t, ts, "alice", "alice-pass")

	// Start disabled so enabling is a genuine 0->1 flip.
	mustExec(t, db, `INSERT INTO subscriptions (user_id, url, url_key, display_name, source_type, filter_json, notify_policy, enabled)
		VALUES (?,?,?,?,?,?,?,0)`, aliceID, "https://example.com/a.atom", "key-re", "Go", "atom", "{}", "on_update")
	var subID int64
	if err := db.QueryRow(`SELECT id FROM subscriptions WHERE url_key=?`, "key-re").Scan(&subID); err != nil {
		t.Fatal(err)
	}

	// A display-name-only patch must NOT trigger a baseline.
	c.do(http.MethodPatch, "/api/subscriptions/"+itoa(subID),
		map[string]string{"display_name": "Renamed"}, http.StatusOK)
	// Disabling (1->0 is not possible here since it starts at 0) — a redundant
	// set to false must also not trigger.
	c.do(http.MethodPatch, "/api/subscriptions/"+itoa(subID),
		map[string]any{"enabled": false}, http.StatusOK)
	if got := fake.count(); got != 0 {
		t.Fatalf("non-enable patches triggered %d baselines", got)
	}

	// 0->1 flip triggers exactly one baseline.
	c.do(http.MethodPatch, "/api/subscriptions/"+itoa(subID),
		map[string]any{"enabled": true}, http.StatusOK)
	call := fake.wait(t)
	if call.userID != aliceID || call.urlKey != "key-re" {
		t.Fatalf("bad re-enable baseline call: %+v", call)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("baseline calls = %d, want 1", got)
	}
}

func TestBaselineOptOutSuppressesSubscribeAndReEnable(t *testing.T) {
	fake := newFakeBaseline()
	ts, db := newTestServer(t, WithBaseline(fake))
	aliceID := userIDByName(t, db, "alice")
	seedEnabledChannel(t, db, aliceID)
	c := login(t, ts, "alice", "alice-pass")

	c.do(http.MethodPatch, "/api/me", map[string]any{"baseline_on_subscribe": false}, http.StatusOK)

	// A fresh subscription must not push the current-version snapshot.
	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/optout.atom", "display_name": "Go"}, http.StatusCreated)

	// Neither must a 0->1 re-enable, the other baseline trigger.
	mustExec(t, db, `INSERT INTO subscriptions (user_id, url, url_key, display_name, source_type, filter_json, notify_policy, enabled)
		VALUES (?,?,?,?,?,?,?,0)`, aliceID, "https://example.com/reopt.atom", "key-reopt", "Go", "atom", "{}", "on_update")
	var subID int64
	if err := db.QueryRow(`SELECT id FROM subscriptions WHERE url_key=?`, "key-reopt").Scan(&subID); err != nil {
		t.Fatal(err)
	}
	c.do(http.MethodPatch, "/api/subscriptions/"+itoa(subID),
		map[string]any{"enabled": true}, http.StatusOK)

	// The trigger is synchronous up to the goroutine; wait briefly so a stray
	// fire-and-forget push would still be observed.
	time.Sleep(200 * time.Millisecond)
	if got := fake.count(); got != 0 {
		t.Fatalf("opted-out account triggered %d baselines", got)
	}

	// Turning the preference back on restores the behaviour.
	c.do(http.MethodPatch, "/api/me", map[string]any{"baseline_on_subscribe": true}, http.StatusOK)
	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/backon.atom", "display_name": "Go"}, http.StatusCreated)
	call := fake.wait(t)
	if call.userID != aliceID || call.urlKey == "" {
		t.Fatalf("bad baseline after re-enabling preference: %+v", call)
	}
}

func TestNilBaselineDoesNotPanic(t *testing.T) {
	// Default newTestServer has no baseline; subscribe and re-enable must still
	// work (the trigger is a no-op).
	ts, db := newTestServer(t)
	seedEnabledChannel(t, db, userIDByName(t, db, "alice"))
	c := login(t, ts, "alice", "alice-pass")
	c.do(http.MethodPost, "/api/subscriptions",
		map[string]string{"url": "https://example.com/a.atom"}, http.StatusCreated)
}
