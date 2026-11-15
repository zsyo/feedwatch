//go:build !webui

// Package store owns the WebUI variant's relational state (SQLite, MySQL or
// PostgreSQL). Every implementation file carries the webui build tag; this file
// keeps the package loadable in the base build, where it contributes no code at
// all.
package store
