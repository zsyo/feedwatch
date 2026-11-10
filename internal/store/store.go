// Package store defines the persistence contract for "which releases have
// already been seen". Implementations live in subpackages; this parent package
// never imports them, which keeps the dependency graph acyclic.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"feedwatch/internal/domain"
)

// ErrNotFound means "no state exists yet" (a first run). A corrupted state
// file must return an ordinary error instead, so the watcher can refuse to
// silently re-send the baseline mail.
var ErrNotFound = errors.New("state not found")

var ErrUnknownType = errors.New("unknown store type")

type StateStore interface {
	Load(ctx context.Context) (domain.SeenState, error)
	Save(ctx context.Context, s domain.SeenState) error
}

type StoreSpec struct {
	Type    string
	Path    string
	Options map[string]string
}

type Factory func(spec StoreSpec, log *slog.Logger) (StateStore, error)

// Registry maps a config "type" string to a constructor. Registration is done
// explicitly in cmd/feedwatch/main.go (the single composition root).
type Registry struct{ m map[string]Factory }

func NewRegistry() *Registry { return &Registry{m: make(map[string]Factory)} }

func (r *Registry) Register(typ string, f Factory) { r.m[typ] = f }

func (r *Registry) Build(spec StoreSpec, log *slog.Logger) (StateStore, error) {
	f, ok := r.m[spec.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, spec.Type)
	}
	return f(spec, log)
}
