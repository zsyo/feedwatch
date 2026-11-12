package jsonfile

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/store"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	s, err := New(store.StoreSpec{Type: "jsonfile", Path: path}, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s.(*Store), path
}

func TestNewFailsFastWhenStateDirIsNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore before t.TempDir's own RemoveAll cleanup (cleanups run LIFO).
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// root ignores mode bits; detect that instead of failing in a root CI job.
	if f, err := os.CreateTemp(dir, ".check-*"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		t.Skip("running as root: directory mode bits do not restrict writes")
	}

	_, err := New(store.StoreSpec{Type: "jsonfile", Path: filepath.Join(dir, "state.json")}, slog.Default())
	if err == nil {
		t.Fatal("New must fail at startup when the state directory is not writable")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error should explain the unwritable directory: %v", err)
	}
}

func TestNewProbeLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(store.StoreSpec{Type: "jsonfile", Path: filepath.Join(dir, "state.json")}, slog.Default()); err != nil {
		t.Fatalf("New: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left files behind: %v", entries)
	}
}

func TestProbeErrorExplainsUnwritableDir(t *testing.T) {
	want := errors.New("permission denied")
	err := probeWritableDir("/app/data", func(string, string) (*os.File, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("probe must wrap the underlying error, got %v", err)
	}
	for _, hint := range []string{"not writable", "/app/data", "--user", "id -u"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("error %q should mention %q", err, hint)
		}
	}
}

func TestLoadMissingReturnsErrNotFound(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Load(context.Background())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s, path := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st := domain.NewSeenState()
	fs := st.EnsureFeed("golang/go")
	fs.SeenIDs["tag:go1.27.3"] = now
	fs.ETag = `"abc"`
	fs.LastVersion = "go1.27.3"

	if err := s.Save(ctx, st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != domain.SchemaVersion {
		t.Errorf("schema version = %d", got.SchemaVersion)
	}
	gfs := got.Feeds["golang/go"]
	if gfs == nil || gfs.ETag != `"abc"` || gfs.LastVersion != "go1.27.3" {
		t.Fatalf("unexpected feed state: %+v", gfs)
	}
	if !gfs.SeenIDs["tag:go1.27.3"].Equal(now) {
		t.Errorf("seen timestamp lost: %v", gfs.SeenIDs)
	}

	// Atomic write must not leave temp files behind, and must be readable JSON.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || e.Name()[0] == '.' {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}

	// omitzero (Go 1.24+) actually drops a zero time.Time; the old omitempty
	// tag silently wrote "0001-01-01T00:00:00Z" for every untouched feed.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"last_checked"`)) {
		t.Errorf("zero LastChecked must be omitted from the state file:\n%s", raw)
	}
}

func TestCorruptedStateIsNotTreatedAsFirstRun(t *testing.T) {
	s, path := newStore(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load(context.Background())
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Fatal("corrupted state must NOT be reported as a first run")
	}
}

func TestConcurrentSaveIsSafe(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context() // cancelled automatically when the test ends
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 { // Go 1.22: per-iteration i is safe to capture
		wg.Go(func() { // Go 1.25: Add(1)/Done() are handled by Go itself
			st := domain.NewSeenState()
			st.EnsureFeed("f").SeenIDs[string(rune('a'+i))] = time.Now().UTC()
			errs <- s.Save(ctx, st)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Save: %v", err)
		}
	}
	if _, err := s.Load(ctx); err != nil {
		t.Fatalf("Load after concurrent saves: %v", err)
	}
}

func TestCancelledContext(t *testing.T) {
	s, _ := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Load: want context.Canceled, got %v", err)
	}
	if err := s.Save(ctx, domain.NewSeenState()); !errors.Is(err, context.Canceled) {
		t.Errorf("Save: want context.Canceled, got %v", err)
	}
}
