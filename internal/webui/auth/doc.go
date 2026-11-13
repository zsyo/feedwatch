//go:build !webui

// Package auth provides password hashing, sessions and CSRF for the WebUI
// variant. All implementation files are excluded from the base build; this
// file only keeps the package loadable.
package auth
