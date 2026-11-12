// Package jsonfile implements store.StateStore with a single JSON document
// written atomically (temp file + rename), so a killed container can never
// leave a half-written state file behind.
package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"feedwatch/internal/domain"
	"feedwatch/internal/store"
)

type Store struct {
	path string
	log  *slog.Logger
	mu   sync.Mutex
}

func New(spec store.StoreSpec, log *slog.Logger) (store.StateStore, error) {
	p := filepath.FromSlash(spec.Path)
	if p == "" || p == "." {
		return nil, errors.New("jsonfile: empty state path")
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("jsonfile: create state directory: %w", err)
	}
	// Fail at startup, before the watcher sends anything. Save() writes via
	// CreateTemp + Rename, which needs write permission on the *directory*
	// even when state.json itself is writable. A bind-mounted host directory
	// owned by another uid is the usual cause on Linux (the image runs as an
	// unprivileged "app" user). Without this check every poll would send the
	// baseline again and only then discover the state cannot be saved.
	if err := probeWritableDir(dir, os.CreateTemp); err != nil {
		return nil, err
	}
	return &Store{path: p, log: log}, nil
}

// probeWritableDir performs the exact filesystem operation Save() relies on
// and removes the probe file immediately, leaving the directory untouched.
// createTemp is passed in so the error path can be tested on every OS.
func probeWritableDir(dir string, createTemp func(string, string) (*os.File, error)) error {
	f, err := createTemp(dir, ".state-probe-*.tmp")
	if err != nil {
		return fmt.Errorf("jsonfile: state directory %s is not writable: %w "+
			"(bind mount? run the container as the directory owner, e.g. "+
			"`--user \"$(id -u):$(id -g)\"`, or fix the directory permissions)", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name) // best effort; the original error is more useful
		return fmt.Errorf("jsonfile: close probe file in %s: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("jsonfile: remove probe file in %s: %w", dir, err)
	}
	return nil
}

func (s *Store) Load(ctx context.Context) (domain.SeenState, error) {
	if err := ctx.Err(); err != nil {
		return domain.SeenState{}, err
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.SeenState{}, store.ErrNotFound
	}
	if err != nil {
		return domain.SeenState{}, fmt.Errorf("jsonfile: read %s: %w", s.path, err)
	}
	var st domain.SeenState
	if err := json.Unmarshal(b, &st); err != nil {
		return domain.SeenState{}, fmt.Errorf("jsonfile: decode %s: %w", s.path, err)
	}
	if st.Feeds == nil {
		st.Feeds = make(map[string]*domain.FeedState)
	}
	return st, nil
}

func (s *Store) Save(ctx context.Context, st domain.SeenState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st.SchemaVersion = domain.SchemaVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("jsonfile: encode state: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("jsonfile: create temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("jsonfile: chmod temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("jsonfile: write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("jsonfile: sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("jsonfile: close temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("jsonfile: rename state file: %w", err)
	}
	return nil
}
