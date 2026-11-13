// Package notifier defines the delivery contract and its registry. Concrete
// channels (email, log, webhook-based IM bots) live in subpackages and render
// the neutral domain.Notification themselves.
package notifier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"feedwatch/internal/domain"
)

var ErrUnknownType = errors.New("unknown notifier type")

type Notifier interface {
	Type() string
	Send(ctx context.Context, n domain.Notification) error
}

// Named is optional: channels implementing it get a readable label in fan-out
// logs, which matters when the same type is configured more than once.
type Named interface {
	Name() string
}

type NotifierSpec struct {
	Type          string
	Name          string // optional label for logs
	From          string
	SubjectPrefix string
	To            []string
	Cc            []string
	// APIKey is injected by main from the environment; it never comes from YAML.
	APIKey  string
	Options map[string]string
	// Client is optional and mainly used by tests (httptest in-memory network).
	// A nil Client means "build a default client with a sane timeout".
	Client *http.Client
}

type Factory func(spec NotifierSpec, log *slog.Logger) (Notifier, error)

type Registry struct{ m map[string]Factory }

func NewRegistry() *Registry { return &Registry{m: make(map[string]Factory)} }

func (r *Registry) Register(typ string, f Factory) { r.m[typ] = f }

func (r *Registry) Build(spec NotifierSpec, log *slog.Logger) (Notifier, error) {
	f, ok := r.m[spec.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, spec.Type)
	}
	return f(spec, log)
}

// Types returns the registered channel types, sorted for stable log output.
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
