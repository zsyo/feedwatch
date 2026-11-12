package notifier

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/pace"
)

type countingNotifier struct {
	typ      string
	calls    atomic.Int64
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (c *countingNotifier) Type() string { return c.typ }
func (c *countingNotifier) Send(context.Context, domain.Notification) error {
	c.calls.Add(1)
	cur := c.inFlight.Add(1)
	for {
		old := c.peak.Load()
		if cur <= old || c.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(time.Millisecond)
	c.inFlight.Add(-1)
	return nil
}

func TestPaceSharesOneGateAcrossChannelsAndBatches(t *testing.T) {
	gate := pace.New(1, 0) // serial: enough to prove they share the same limit
	children := []Notifier{
		Pace(&countingNotifier{typ: "a"}, gate),
		Pace(&countingNotifier{typ: "b"}, gate),
	}
	m, err := NewMulti(children, BestEffort, quiet())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _ = m.Send(context.Background(), note()) })
	}
	wg.Wait()
	if got := children[0].(*paced).inner.(*countingNotifier).calls.Load(); got != 20 {
		t.Errorf("channel a calls = %d, want 20", got)
	}
	if got := children[1].(*paced).inner.(*countingNotifier).calls.Load(); got != 20 {
		t.Errorf("channel b calls = %d, want 20", got)
	}
	// All 40 deliveries go through one gate with concurrency=1, so no two
	// channel Sends may overlap, even across concurrent fan-out calls.
	for _, c := range children {
		if peak := c.(*paced).inner.(*countingNotifier).peak.Load(); peak > 1 {
			t.Errorf("%s overlapped: peak=%d", c.Type(), peak)
		}
	}
}

func TestPaceSpacingThrottlesDeliveries(t *testing.T) {
	gate := pace.New(1, 25*time.Millisecond)
	n := Pace(&fakeNotifier{typ: "x"}, gate)
	start := time.Now()
	for range 4 {
		if err := n.Send(context.Background(), note()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 3*25*time.Millisecond {
		t.Errorf("4 sends finished in %v, spacing was not enforced", elapsed)
	}
}

func TestPaceForwardsNameAndRespectsCancellation(t *testing.T) {
	if got := Pace(&fakeNotifier{typ: "plain"}, pace.New(1, 0)).(Named).Name(); got != "" {
		t.Errorf("Name() for a non-Named inner = %q, want empty", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeNotifier{typ: "x"}
	if err := Pace(f, pace.New(1, 0)).Send(ctx, note()); err == nil {
		t.Error("canceled context must reach the caller")
	}
	if f.calls != 0 {
		t.Errorf("inner must not be called after cancellation, got %d", f.calls)
	}
	if got := Pace(f, nil); got != f {
		t.Error("a nil gate must not wrap the notifier")
	}
}
