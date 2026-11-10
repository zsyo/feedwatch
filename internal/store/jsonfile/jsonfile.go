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
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("jsonfile: create state directory: %w", err)
	}
	return &Store{path: p, log: log}, nil
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
