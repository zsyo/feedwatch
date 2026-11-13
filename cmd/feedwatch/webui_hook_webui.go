//go:build webui

package main

// webuiLaunch takes over the process in webui builds. The base build has a
// constant-false twin in webui_hook.go, so neither build sees both paths.
func webuiLaunch(cfgPath, logLevel, logFormat string, once, dryRun bool) (bool, error) {
	return true, runWebUI(cfgPath, logLevel, logFormat, once, dryRun)
}

// runWebUI is filled in by later feature points (SQLite, HTTP server,
// orchestrator). For now it is a deliberate no-op so that the tagged build is a
// green, compilable node rather than a half-implemented server.
func runWebUI(_ string, _ string, _ string, _ bool, _ bool) error {
	return nil
}
