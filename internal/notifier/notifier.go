// Package notifier defines the delivery contract and its registry. Concrete
// channels (email, log, future slack/webhook) live in subpackages and render
// the neutral domain.Notification themselves.
package notifier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"feedwatch/internal/domain"
)

var ErrUnknownType = errors.New("unknown notifier type")

type Notifier interface {
	Type() string
	Send(ctx context.Context, n domain.Notification) error
}

type NotifierSpec struct {
	Type          string
	From          string
	SubjectPrefix string
	To            []string
	Cc            []string
	// APIKey is injected by main from the environment; it never comes from YAML.
	APIKey  string
	Options map[string]string
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
