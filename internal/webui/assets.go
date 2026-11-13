//go:build webui

// Package webui holds the optional multi-tenant WebUI variant.
package webui

import (
	"embed"
	"io/fs"
)

// The "all:" prefix also embeds dotfiles such as .gitkeep. The directory always
// exists with at least stub.html, so a clean checkout compiles with -tags webui
// even before the Vite assets have been built.
//
//go:embed all:embedded
var embeddedFiles embed.FS

// Assets returns the embedded SPA filesystem rooted at embedded/. A missing
// index.html means the frontend has not been built yet; the HTTP layer falls
// back to stub.html in that case.
func Assets() (fs.FS, error) {
	return fs.Sub(embeddedFiles, "embedded")
}
