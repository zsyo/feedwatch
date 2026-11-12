package notifier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"feedwatch/internal/domain"
)

// FailurePolicy decides what a partial fan-out failure means to the watcher.
// The watcher treats an error from ONE notification as "do not commit the
// feeds in that batch, retry those feeds next cycle"; other batches are still
// committed. Retrying a batch re-sends it to *every* channel, including the
// ones that already delivered successfully.
type FailurePolicy string

const (
	// BestEffort: succeed when at least one channel delivered. Failed channels
	// are logged loudly but are never retried, so no duplicates are produced.
	// With a single channel this behaves exactly like before: failure -> error.
	BestEffort FailurePolicy = "best_effort"
	// Strict: every channel must succeed. Guarantees no channel misses a
	// notification, at the cost of duplicates when one channel is flaky.
	Strict FailurePolicy = "strict"
)

func ParseFailurePolicy(s string) (FailurePolicy, error) {
	switch FailurePolicy(s) {
	case "", BestEffort:
		return BestEffort, nil
	case Strict:
		return Strict, nil
	default:
		return "", fmt.Errorf("unknown notify.on_partial_failure %q (want best_effort or strict)", s)
	}
}

// Multi fans one notification out to several channels, serially. Channels are
// independent: one failure never prevents the others from being attempted.
type Multi struct {
	children []Notifier
	policy   FailurePolicy
	log      *slog.Logger
}

func NewMulti(children []Notifier, policy FailurePolicy, log *slog.Logger) (*Multi, error) {
	if len(children) == 0 {
		return nil, errors.New("notifier: multi needs at least one channel")
	}
	if policy == "" {
		policy = BestEffort
	}
	if policy != BestEffort && policy != Strict {
		return nil, fmt.Errorf("notifier: unknown failure policy %q", policy)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Multi{children: children, policy: policy, log: log}, nil
}

func (m *Multi) Type() string { return "multi" }

func (m *Multi) Send(ctx context.Context, n domain.Notification) error {
	var errs []error
	delivered := 0
	for i, c := range m.children {
		label := c.Type() + "#" + strconv.Itoa(i)
		if named, ok := c.(Named); ok && named.Name() != "" {
			label = named.Name()
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			break
		}
		if err := c.Send(ctx, n); err != nil {
			m.log.Error("notification channel failed", "channel", label, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			continue
		}
		delivered++
		m.log.Info("notification delivered", "channel", label, "kind", n.Kind, "items", len(n.Items))
	}

	if len(errs) == 0 {
		return nil
	}
	// Strict, or every channel failed: propagate so the watcher does not commit
	// state and the whole batch is retried on the next cycle.
	if m.policy == Strict || delivered == 0 {
		return errors.Join(errs...)
	}
	m.log.Warn("some channels failed; committing state anyway to avoid duplicate pushes",
		"policy", m.policy, "delivered", delivered, "failed", len(errs))
	return nil
}

// Channels exposes a copy so callers can inspect the fan-out without mutating it.
func (m *Multi) Channels() []Notifier { return append([]Notifier(nil), m.children...) }
