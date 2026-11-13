//go:build !webui

package main

// webuiLaunch is a compile-time no-op in the base build. Returning false lets
// run() continue into the original config.yaml / watcher path unchanged; because
// the result is a constant, the linker removes the branch entirely. This file
// intentionally imports nothing from the webui tree so the base binary can
// never link SQLite, the HTTP server or the embedded SPA.
func webuiLaunch(_ string, _ string, _ string, _ bool, _ bool) (bool, error) {
	return false, nil
}
