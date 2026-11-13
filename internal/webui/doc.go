//go:build !webui

// Package webui holds the optional multi-tenant WebUI variant. In the base
// build every implementation file is excluded by the webui build tag, so this
// file keeps the package (and therefore `go test ./...`) valid.
package webui
