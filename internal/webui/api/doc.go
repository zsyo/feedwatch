//go:build !webui

// Package api serves the WebUI variant's JSON endpoints and embedded SPA. All
// implementation files are excluded from the base build; this file only keeps
// the package loadable.
package api
