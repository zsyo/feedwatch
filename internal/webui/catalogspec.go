//go:build webui

package webui

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Bounds for user-supplied catalog content. They are also handed to the SPA in
// GET /api/catalog's "meta" so the form enforces the same limits client-side.
const (
	MaxCatalogNameLen        = 80
	MaxCatalogDescriptionLen = 500
	MaxCatalogHomepageLen    = 2048
	MaxCatalogURLLen         = 2048
	MaxCatalogCategoryLen    = 40
	MaxPendingPerUser        = 10
	MaxCatalogRejectReason   = 500
)

// sensitiveQueryFragments are rejected in catalog URLs. A catalog URL is shown
// to every regular user and copied verbatim from the "pending review" page, so
// it must not be able to carry a credential. NormalizeURL deliberately keeps
// query values (webhook credentials legitimately live there for subscriptions),
// which is exactly why the catalog needs its own, stricter entry check.
var sensitiveQueryFragments = []string{
	"token", "secret", "key", "password", "passwd", "pwd",
	"auth", "sig", "signature", "credential", "private",
}

// CatalogURLKey normalizes a catalog URL and returns its stable identity.
//
// It starts from URLKey so equivalent spellings (case, default port, fragment,
// reordered query) collide on the same key, then applies the public-catalog
// restrictions: http(s) only, no userinfo, no credential-like query keys.
// subscriptions keep using the more permissive URLKey unchanged.
func CatalogURLKey(raw string) (normalized, key string, err error) {
	if len(raw) > MaxCatalogURLLen {
		return "", "", fmt.Errorf("webui: catalog URL is longer than %d bytes", MaxCatalogURLLen)
	}
	normalized, key, err = URLKey(raw)
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", "", fmt.Errorf("webui: parse normalized catalog URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("webui: catalog URL must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("webui: catalog URL has no host")
	}
	if u.User != nil {
		return "", "", fmt.Errorf("webui: catalog URL must not contain credentials in the userinfo")
	}
	for qk := range u.Query() {
		lower := strings.ToLower(qk)
		for _, fragment := range sensitiveQueryFragments {
			if strings.Contains(lower, fragment) {
				return "", "", fmt.Errorf("webui: catalog URL must not contain a credential-like query parameter %q", qk)
			}
		}
	}
	return normalized, key, nil
}

// CleanCatalogText trims a user-supplied text field and rejects the control
// characters that would let it forge extra rows in the UI. maxLen counts
// runes, not bytes, so Chinese descriptions get the same allowance.
func CleanCatalogText(value string, maxLen int, label string) (string, error) {
	cleaned := strings.TrimSpace(value)
	if strings.ContainsAny(cleaned, "\r\n\x00") {
		return "", fmt.Errorf("webui: %s must not contain line breaks or NUL", label)
	}
	if utf8.RuneCountInString(cleaned) > maxLen {
		return "", fmt.Errorf("webui: %s is longer than %d characters", label, maxLen)
	}
	return cleaned, nil
}

// CleanCatalogHomepage validates the optional project homepage. It is shown as
// a link, so it must be an ordinary http(s) URL without credentials; unlike a
// feed URL it does not need to be hashed or deduplicated.
func CleanCatalogHomepage(raw string) (string, error) {
	cleaned, err := CleanCatalogText(raw, MaxCatalogHomepageLen, "homepage")
	if err != nil || cleaned == "" {
		return cleaned, err
	}
	u, err := url.Parse(cleaned)
	if err != nil {
		return "", fmt.Errorf("webui: invalid homepage: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("webui: homepage must be an http(s) URL without credentials")
	}
	return cleaned, nil
}

// CatalogEntryTemplate is one row of the built-in example catalog. It is a
// template only: seeding it must never create a subscription or a pool row.
type CatalogEntryTemplate struct {
	Name        string
	URL         string
	Category    string
	Description string
	Homepage    string
}

// ExampleCatalog is shown on a brand-new database so the subscription center
// is not empty on first login. Every URL is a GitHub repository's releases.atom
// feed, which GitHub publishes automatically and which needs no credentials.
// Administrators may edit, disable or delete any of these rows.
var ExampleCatalog = []CatalogEntryTemplate{
	{
		Name:        "Node.js",
		URL:         "https://github.com/nodejs/node/releases.atom",
		Category:    "语言与运行时",
		Description: "Node.js 官方仓库的发布动态，包含 LTS 与 Current 版本。",
		Homepage:    "https://github.com/nodejs/node",
	},
	{
		Name:        "Deno",
		URL:         "https://github.com/denoland/deno/releases.atom",
		Category:    "语言与运行时",
		Description: "Deno 运行时与工具链的版本发布。",
		Homepage:    "https://github.com/denoland/deno",
	},
	{
		Name:        "Bun",
		URL:         "https://github.com/oven-sh/bun/releases.atom",
		Category:    "语言与运行时",
		Description: "Bun JavaScript 运行时的版本发布。",
		Homepage:    "https://github.com/oven-sh/bun",
	},
	{
		Name:        "ripgrep",
		URL:         "https://github.com/BurntSushi/ripgrep/releases.atom",
		Category:    "命令行工具",
		Description: "ripgrep 命令行搜索工具的版本发布。",
		Homepage:    "https://github.com/BurntSushi/ripgrep",
	},
	{
		Name:        "fd",
		URL:         "https://github.com/sharkdp/fd/releases.atom",
		Category:    "命令行工具",
		Description: "fd 文件查找工具的版本发布。",
		Homepage:    "https://github.com/sharkdp/fd",
	},
	{
		Name:        "fzf",
		URL:         "https://github.com/junegunn/fzf/releases.atom",
		Category:    "命令行工具",
		Description: "fzf 模糊查找工具的版本发布。",
		Homepage:    "https://github.com/junegunn/fzf",
	},
	{
		Name:        "GitHub CLI",
		URL:         "https://github.com/cli/cli/releases.atom",
		Category:    "命令行工具",
		Description: "gh 命令行工具的版本发布。",
		Homepage:    "https://github.com/cli/cli",
	},
	{
		Name:        "Neovim",
		URL:         "https://github.com/neovim/neovim/releases.atom",
		Category:    "编辑器",
		Description: "Neovim 编辑器的稳定版与预发布版本。",
		Homepage:    "https://github.com/neovim/neovim",
	},
}

// ExampleCategoryNames returns the distinct categories used by the example
// catalog, in first-appearance order.
func ExampleCategoryNames() []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 4)
	for _, e := range ExampleCatalog {
		if !seen[e.Category] {
			seen[e.Category] = true
			out = append(out, e.Category)
		}
	}
	return out
}

// CatalogDocument is the export/import format. Categories are matched by name
// rather than id because ids are local to a database. It never contains user
// subscriptions, channel credentials or delivery state.
type CatalogDocument struct {
	Format     string                    `json:"format"`
	Version    int                       `json:"version"`
	ExportedAt string                    `json:"exported_at,omitempty"`
	Categories []CatalogDocumentCategory `json:"categories"`
	Entries    []CatalogDocumentEntry    `json:"entries"`
	Strategy   string                    `json:"strategy,omitempty"`
}

type CatalogDocumentCategory struct {
	Name string `json:"name"`
}

type CatalogDocumentEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Enabled     bool   `json:"enabled"`
}

const (
	CatalogDocumentFormat = "feedwatch-catalog"
	CatalogDocumentV1     = 1

	ImportStrategySkip      = "skip"
	ImportStrategyOverwrite = "overwrite"
	ImportStrategyError     = "error"
)

// ValidImportStrategy reports whether strategy is one of the three supported
// conflict policies.
func ValidImportStrategy(strategy string) bool {
	switch strategy {
	case ImportStrategySkip, ImportStrategyOverwrite, ImportStrategyError:
		return true
	default:
		return false
	}
}
