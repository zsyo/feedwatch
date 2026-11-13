//go:build webui

package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
	"feedwatch/internal/webui/store"
)

type recordingNotifier struct {
	mu    sync.Mutex
	calls []domain.Notification
	err   error
}

func (r *recordingNotifier) Type() string { return "fake" }

func (r *recordingNotifier) Send(_ context.Context, n domain.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, n)
	return r.err
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingNotifier) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

type deliveryHarness struct {
	w   *DeliveryWorker
	db  *sql.DB
	rec *recordingNotifier
}

func newDeliveryHarness(t *testing.T, spacing time.Duration) *deliveryHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "feedwatch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := NewDeliveryWorker(st, quietLog(), spacing)
	w.now = func() time.Time { return fixedTime }
	rec := &recordingNotifier{}
	w.reg = notifier.NewRegistry()
	for _, typ := range []string{"log", "email", "telegram"} {
		w.reg.Register(typ, func(spec notifier.NotifierSpec, log *slog.Logger) (notifier.Notifier, error) {
			return rec, nil
		})
	}
	return &deliveryHarness{w: w, db: st.DB(), rec: rec}
}

func (h *deliveryHarness) addMaterialized(t *testing.T, cycleID string, userID int64, channelIDs []int64) {
	t.Helper()
	note := domain.Notification{
		Kind: domain.KindNewReleases, GeneratedAt: fixedTime,
		Items: []domain.ReleaseItem{{Feed: "project", Version: "1.2.3", IsNew: true}},
	}
	r, err := materialize(note, "[FW]", userID, channelIDs)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertNotification(tx, cycleID, r, fixedTime.Format(time.RFC3339Nano)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanDeliveriesCreatesIndependentRequestsPerUser(t *testing.T) {
	h := newDeliveryHarness(t, 0)
	u1, u2 := addUser(t, h.db, "alice"), addUser(t, h.db, "bob")
	c1, c2 := addChannel(t, h.db, u1), addChannel(t, h.db, u2)
	h.addMaterialized(t, "cycle-1", u1, []int64{c1})
	h.addMaterialized(t, "cycle-1", u2, []int64{c2})

	planned, err := PlanDeliveries(context.Background(), h.db)
	if err != nil {
		t.Fatal(err)
	}
	if planned != 2 {
		t.Fatalf("planned = %d, want 2 independent requests", planned)
	}
	if got := countRows(t, h.db, `SELECT COUNT(*) FROM delivery_groups`); got != 2 {
		t.Fatalf("delivery_groups = %d, want 2 (no merging)", got)
	}
	if got := countRows(t, h.db, `SELECT COUNT(*) FROM deliveries`); got != 2 {
		t.Fatalf("deliveries = %d, want 2", got)
	}
	var groupsWithTwo int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM (SELECT group_id FROM deliveries GROUP BY group_id HAVING COUNT(*)>1)`).Scan(&groupsWithTwo); err != nil {
		t.Fatal(err)
	}
	if groupsWithTwo != 0 {
		t.Fatalf("%d groups merged several users/channels", groupsWithTwo)
	}
	if got := countRows(t, h.db, `SELECT COUNT(*) FROM notifications WHERE merge_key='planned'`); got != 2 {
		t.Fatalf("notifications not claimed: %d", got)
	}

	sent, failed, err := h.w.DeliverPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 || failed != 0 {
		t.Fatalf("sent=%d failed=%d, want 2/0", sent, failed)
	}
	if h.rec.count() != 2 {
		t.Fatalf("network sends = %d, want one per user", h.rec.count())
	}
	if got := countRows(t, h.db, `SELECT COUNT(*) FROM delivery_groups WHERE status='sent'`); got != 2 {
		t.Fatalf("sent groups = %d, want 2", got)
	}

	// Re-planning must be a no-op (idempotent claim marker).
	again, err := PlanDeliveries(context.Background(), h.db)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("re-planning created %d duplicate groups", again)
	}
}

func TestRateLimitGateIsPerChannelType(t *testing.T) {
	h := newDeliveryHarness(t, 250*time.Millisecond)
	email1 := h.w.gateFor("email")
	email2 := h.w.gateFor("email")
	telegram := h.w.gateFor("telegram")
	if email1 != email2 {
		t.Fatal("same channel type must share one gate")
	}
	if email1 == telegram {
		t.Fatal("different providers must not share a gate")
	}
	if email1.Spacing() != 250*time.Millisecond || telegram.Spacing() != 250*time.Millisecond {
		t.Fatal("send_spacing was not applied per type")
	}
	if len(h.w.gates) != 2 {
		t.Fatalf("gates = %d, want 2", len(h.w.gates))
	}
}

func TestFailedDeliveryRetriesThenAbandonsWithoutReSending(t *testing.T) {
	h := newDeliveryHarness(t, 0)
	u := addUser(t, h.db, "alice")
	c := addChannel(t, h.db, u)
	h.addMaterialized(t, "cycle-1", u, []int64{c})
	if _, err := PlanDeliveries(context.Background(), h.db); err != nil {
		t.Fatal(err)
	}
	h.rec.setError(errors.New("provider 500"))

	for want := 1; want <= 2; want++ {
		if _, _, err := h.w.DeliverPending(context.Background()); err != nil {
			t.Fatal(err)
		}
		var attempts int
		var status string
		if err := h.db.QueryRow(`SELECT attempts, status FROM delivery_groups`).Scan(&attempts, &status); err != nil {
			t.Fatal(err)
		}
		if attempts != want || status != "retry" {
			t.Fatalf("attempt %d: attempts=%d status=%s", want, attempts, status)
		}
	}
	if _, err := h.db.Exec(`UPDATE delivery_groups SET attempts=4`); err != nil {
		t.Fatal(err)
	}
	// The 5th failure exhausts the retry budget and abandons the group.
	if _, _, err := h.w.DeliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err := h.db.QueryRow(`SELECT status, attempts FROM delivery_groups`).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "abandoned" || attempts != 5 {
		t.Fatalf("want abandoned/5, got %s/%d", status, attempts)
	}
	h.rec.setError(nil)
	before := h.rec.count()
	if _, _, err := h.w.DeliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != before {
		t.Fatal("abandoned group was retried")
	}
}

func TestExpiredGroupIsAbandonedWithoutSending(t *testing.T) {
	h := newDeliveryHarness(t, 0)
	u := addUser(t, h.db, "alice")
	c := addChannel(t, h.db, u)
	h.addMaterialized(t, "cycle-1", u, []int64{c})
	if _, err := PlanDeliveries(context.Background(), h.db); err != nil {
		t.Fatal(err)
	}
	// w.now is fixedTime (2026-01-02); anything older than 24h before it is stale.
	if _, err := h.db.Exec(`UPDATE delivery_groups SET created_at=?`,
		fixedTime.Add(-25*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.w.DeliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.rec.count() != 0 {
		t.Fatal("expired group must not be sent")
	}
	if got := countRows(t, h.db, `SELECT COUNT(*) FROM delivery_groups WHERE status='abandoned'`); got != 1 {
		t.Fatalf("abandoned groups = %d, want 1", got)
	}
}

func TestBareKindFallbackForFirstStartBaseline(t *testing.T) {
	h := newDeliveryHarness(t, 0)
	u := addUser(t, h.db, "alice")
	c := addChannel(t, h.db, u)
	now := fixedTime.Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`INSERT INTO notifications
		(cycle_id, merge_key, kind, subject, "text", html, markdown, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, "seed-cycle", "seed-baseline", "baseline", "subject", "text", "", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO delivery_groups
		(group_id, cycle_id, merge_key, channel_type, idempotency_key, status, attempts, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, "grp-seed", "seed-cycle", "log", "log", "seed", "pending", 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO deliveries
		(cycle_id, group_id, user_id, channel_id, kind, status, attempts)
		VALUES (?,?,?,?,?,?,?)`, "seed-cycle", "grp-seed", u, c, "baseline", "pending", 0); err != nil {
		t.Fatal(err)
	}
	sent, failed, err := h.w.DeliverPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || failed != 0 || h.rec.count() != 1 {
		t.Fatalf("sent=%d failed=%d calls=%d", sent, failed, h.rec.count())
	}
}
