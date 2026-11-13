//go:build webui

package store

import (
	"errors"
	"fmt"
	"os"
)

// createTempFunc mirrors os.CreateTemp so tests can simulate an unwritable
// directory without relying on filesystem permissions (which differ between
// Windows and Linux and are unreliable when running as root).
type createTempFunc func(dir, pattern string) (*os.File, error)

const writeHint = "WebUI uses SQLite in WAL mode, so the data DIRECTORY must be writable (not just the .db file). " +
	"For a bind mount, run the container with --user \"$(id -u):$(id -g)\" (or set FW_UID/FW_GID in .env) " +
	"and make sure the host directory is owned by that uid/gid."

// probeWritableDir fails fast at startup, before any notification can be sent.
// jsonfile.New uses the same pattern for the base build's state directory.
func probeWritableDir(dir string, createTemp createTempFunc) error {
	f, err := createTemp(dir, ".feedwatch-webui-*")
	if err != nil {
		return fmt.Errorf("webui: data directory %q is not writable: %w\n%s", dir, err, writeHint)
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return fmt.Errorf("webui: close write-probe file in %q: %w", dir, closeErr)
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("webui: remove write-probe file in %q: %w", dir, removeErr)
	}
	return nil
}
