//go:build webui

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeWritableDirRemovesTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := probeWritableDir(dir, os.CreateTemp); err != nil {
		t.Fatalf("probeWritableDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left files behind: %v", entries)
	}
}

func TestProbeWritableDirReportsHint(t *testing.T) {
	want := errors.New("permission denied")
	dir := t.TempDir()
	err := probeWritableDir(dir, func(string, string) (*os.File, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("want wrapped %v, got %v", want, err)
	}
	for _, hint := range []string{filepath.Base(dir), "WAL", "--user", "FW_UID"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("error %q should mention %q", err, hint)
		}
	}
}

func TestOpenFailsBeforeTouchingDatabaseWhenDirIsUnwritable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nested", "feedwatch.db")
	_, err := open(dbPath, nil, func(string, string) (*os.File, error) {
		return nil, errors.New("permission denied")
	})
	if err == nil {
		t.Fatal("open should fail the writability precheck")
	}
	if !strings.Contains(err.Error(), "--user") {
		t.Fatalf("error should carry the uid/gid hint: %v", err)
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("no database file should be created before the precheck passes, stat err = %v", statErr)
	}
}
