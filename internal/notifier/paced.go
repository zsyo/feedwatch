package notifier

import (
	"context"

	"feedwatch/internal/domain"
	"feedwatch/internal/pace"
)

// paced wraps ONE channel so every delivery shares a process-wide rate limit
// with all other channels and all notification batches. The watcher only sees
// a normal Notifier; pacing therefore also applies when several channels are
// fanned out by Multi, or when mode=per_feed produces many batches in a cycle.
type paced struct {
	inner Notifier
	gate  *pace.Gate
}

// Pace decorates n. A nil gate (or one with spacing=0 and concurrency=1) is a
// no-op wrapper, so callers can pass the result of pace.New unconditionally.
func Pace(n Notifier, gate *pace.Gate) Notifier {
	if gate == nil {
		return n
	}
	return &paced{inner: n, gate: gate}
}

func (p *paced) Type() string { return p.inner.Type() }

// Name keeps Multi's readable channel labels when the wrapped notifier is
// Named. Returning "" for the others makes Multi fall back to "type#index".
func (p *paced) Name() string {
	if named, ok := p.inner.(Named); ok {
		return named.Name()
	}
	return ""
}

func (p *paced) Send(ctx context.Context, n domain.Notification) error {
	release, err := p.gate.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return p.inner.Send(ctx, n)
}
