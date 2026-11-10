// Package source defines the data-source contract and its registry.
// Implementations live in subpackages (see ./atom). The watcher only knows
// this interface, so a new source type never touches the core logic.
package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"feedwatch/internal/domain"
)

var ErrUnknownType = errors.New("unknown source type")

// Source returns every entry currently visible in the feed window (before
// filtering). The watcher performs the diff against the persisted seen IDs.
type Source interface {
	Type() string
	Fetch(ctx context.Context, since Cursor) (Result, error)
}

// Cursor carries conditional-request validators from the previous poll.
type Cursor struct {
	ETag         string
	LastModified string
}

type Result struct {
	Releases     []domain.Release
	ETag         string
	LastModified string
	// NotModified means HTTP 304: the watcher must not diff or clear seen IDs.
	NotModified bool
}

type SourceSpec struct {
	Name    string
	Type    string
	URL     string
	Options map[string]string
	// Client is optional; mainly used by tests to inject an httptest client.
	Client *http.Client
}

type Factory func(spec SourceSpec, log *slog.Logger) (Source, error)

type Registry struct{ m map[string]Factory }

func NewRegistry() *Registry { return &Registry{m: make(map[string]Factory)} }

func (r *Registry) Register(typ string, f Factory) { r.m[typ] = f }

func (r *Registry) Build(spec SourceSpec, log *slog.Logger) (Source, error) {
	f, ok := r.m[spec.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, spec.Type)
	}
	return f(spec, log)
}
