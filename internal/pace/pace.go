// Package pace provides a small, dependency-free rate limiter for outbound
// work. It combines two independent limits:
//
//   - Concurrency: at most N operations may be in flight at once.
//   - Spacing: two operations may not START less than D apart, even when
//     several goroutines are waiting. This caps the request rate against a
//     single upstream host instead of letting a burst of N goroutines fire
//     simultaneously.
//
// Both limits are process-wide for a given Gate: one Gate shared by all feed
// fetches gives a global fetch cap, while notifier deliveries use one Gate per
// channel type so unrelated providers never block each other.
package pace

import (
	"context"
	"sync"
	"time"
)

// Gate is safe for concurrent use. Zero values are not usable; call New.
type Gate struct {
	slots   chan struct{}
	limit   int
	spacing time.Duration

	mu   sync.Mutex
	next time.Time // earliest time the next caller is allowed to start

	// Injectable clock/sleeper so spacing can be tested without real waits.
	now  func() time.Time
	wait func(context.Context, time.Duration) error
}

// New builds a Gate. concurrency < 1 is clamped to 1 (fully serial) and a
// negative spacing is clamped to 0 (disabled).
func New(concurrency int, spacing time.Duration) *Gate {
	concurrency = max(concurrency, 1)
	spacing = max(spacing, 0)
	return &Gate{
		slots:   make(chan struct{}, concurrency),
		limit:   concurrency,
		spacing: spacing,
		now:     time.Now,
		wait:    sleep,
	}
}

// Concurrency is the maximum number of in-flight operations.
func (g *Gate) Concurrency() int { return g.limit }

// Spacing is the minimum gap between two operation starts.
func (g *Gate) Spacing() time.Duration { return g.spacing }

// Acquire reserves the next start slot, waits until it is due, then takes one
// concurrency slot. The returned release must be called exactly once when the
// operation finishes; calling it twice is harmless.
//
// Holding mu across the wait is deliberate: it serialises the reservation
// itself, so the k-th concurrent caller starts k*spacing after the first,
// without any background goroutine or timer leak. A cancelled wait never
// advances next, so an aborted cycle cannot leave a stale schedule behind.
func (g *Gate) Acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if g.spacing > 0 {
		g.mu.Lock()
		now := g.now()
		if waitFor := g.next.Sub(now); waitFor > 0 {
			if err := g.wait(ctx, waitFor); err != nil {
				g.mu.Unlock()
				return nil, err
			}
			now = g.now()
		}
		g.next = now.Add(g.spacing)
		g.mu.Unlock()
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case g.slots <- struct{}{}:
	}
	var once sync.Once
	return func() {
		once.Do(func() { <-g.slots })
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
