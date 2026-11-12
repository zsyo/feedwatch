package pace

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock replaces both the clock and the sleeper so spacing can be checked
// deterministically, without any real waiting.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func newFakeGate(t *testing.T, concurrency int, spacing time.Duration) (*Gate, *fakeClock) {
	t.Helper()
	clk := &fakeClock{now: time.Unix(0, 0).UTC()}
	g := New(concurrency, spacing)
	g.now = clk.Now
	g.wait = clk.Wait
	return g, clk
}

func TestNewClampsInvalidLimits(t *testing.T) {
	g := New(0, -time.Second)
	if g.Concurrency() != 1 || g.Spacing() != 0 {
		t.Fatalf("clamp failed: concurrency=%d spacing=%v", g.Concurrency(), g.Spacing())
	}
}

func TestSpacingSerialisesStarts(t *testing.T) {
	const spacing = 100 * time.Millisecond
	g, _ := newFakeGate(t, 1, spacing)
	starts := make([]time.Time, 0, 4)
	for range 4 {
		release, err := g.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		starts = append(starts, g.now())
		release()
	}
	for i := 1; i < len(starts); i++ {
		if got := starts[i].Sub(starts[i-1]); got < spacing {
			t.Errorf("start %d only %v after the previous one, want >= %v", i, got, spacing)
		}
	}
}

func TestSpacingDisabledStartsImmediately(t *testing.T) {
	g, clk := newFakeGate(t, 1, 0)
	for range 3 {
		release, err := g.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if !clk.Now().Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("fake clock advanced without spacing: %v", clk.Now())
	}
}

func TestConcurrencyCapsInFlightWork(t *testing.T) {
	const limit = 3
	g := New(limit, 0)
	var inFlight, peak atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			release, err := g.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			cur := inFlight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inFlight.Add(-1)
			release()
		})
	}
	wg.Wait()
	if peak.Load() > limit {
		t.Fatalf("peak in-flight = %d, want <= %d", peak.Load(), limit)
	}
	if peak.Load() == 0 {
		t.Fatal("no work ran")
	}
}

func TestCanceledWaitDoesNotStaleTheSchedule(t *testing.T) {
	const spacing = time.Hour
	g, _ := newFakeGate(t, 1, spacing)
	t0 := time.Unix(0, 0).UTC()

	// First acquisition reserves the schedule at t0 + 1h.
	release, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !g.next.Equal(t0.Add(spacing)) {
		t.Fatalf("initial next = %v, want %v", g.next, t0.Add(spacing))
	}

	// The second caller has to wait; cancel it while it is waiting. A failed
	// wait must NOT advance next, otherwise every aborted feed in a cycle
	// would push the schedule further out and stall the next cycle.
	ctx, cancel := context.WithCancel(context.Background())
	// Simulate cancellation *during* the wait (the interesting path: the
	// caller already holds mu and must not leave a stale reservation).
	g.wait = func(c context.Context, _ time.Duration) error {
		cancel()
		return c.Err()
	}
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("canceled wait must return an error")
	}
	if !g.next.Equal(t0.Add(spacing)) {
		t.Fatalf("next was advanced by a failed acquire: %v", g.next)
	}

	// A fresh cycle (its real clock is far in the future) is never delayed by
	// the stale reservation.
	g.now = func() time.Time { return t0.Add(24 * time.Hour) }
	rel, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire in a later cycle: %v", err)
	}
	rel()
}

func TestReleaseIsIdempotent(t *testing.T) {
	g := New(1, 0)
	release, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if _, err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("slot was leaked: %v", err)
	}
}
