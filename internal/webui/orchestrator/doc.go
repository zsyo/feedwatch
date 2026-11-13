//go:build !webui

// Package orchestrator runs the WebUI variant's multi-tenant poll cycle. All
// implementation files are excluded from the base build; this file only keeps
// the package loadable.
package orchestrator
