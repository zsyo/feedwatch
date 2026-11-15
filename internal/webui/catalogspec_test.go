//go:build webui

package webui

import (
	"strings"
	"testing"
)

func TestCatalogURLKeyNormalizesEquivalentSpellings(t *testing.T) {
	_, first, err := CatalogURLKey("https://github.com/owner/repo/releases.atom?a=1&b=2")
	if err != nil {
		t.Fatal(err)
	}
	normalized, second, err := CatalogURLKey("HTTPS://GitHub.com:443/owner/repo/releases.atom?b=2&a=1#frag")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent URLs got different keys: %q vs %q", first, second)
	}
	if strings.Contains(normalized, "#frag") || strings.Contains(normalized, ":443") {
		t.Fatalf("normalization left noise behind: %q", normalized)
	}
	if strings.Contains(normalized, "?b=2&a=1") {
		t.Fatalf("query keys were not sorted: %q", normalized)
	}
}

func TestCatalogURLKeyRejectsUnsafeURLs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"relative", "/owner/repo/releases.atom"},
		{"missing host", "https:///releases.atom"},
		{"ftp", "ftp://example.com/feed.atom"},
		{"javascript", "javascript:alert(1)"},
		{"file", "file:///etc/passwd"},
		{"userinfo", "https://user:pass@example.com/feed.atom"},
		{"token query", "https://example.com/feed.atom?token=abcdef"},
		{"api key query", "https://example.com/feed.atom?api_key=abcdef"},
		{"signature query", "https://example.com/feed.atom?sig=abcdef"},
		{"password query", "https://example.com/feed.atom?password=abcdef"},
		{"too long", "https://example.com/" + strings.Repeat("a", MaxCatalogURLLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := CatalogURLKey(tc.raw); err == nil {
				t.Fatalf("expected %q to be rejected", tc.raw)
			}
		})
	}
	for _, raw := range []string{
		"http://example.com/feed.atom",
		"https://example.com/feed.atom?tag=v1.2.3",
	} {
		if _, _, err := CatalogURLKey(raw); err != nil {
			t.Fatalf("expected %q to be accepted: %v", raw, err)
		}
	}
}

func TestCleanCatalogText(t *testing.T) {
	got, err := CleanCatalogText("  Hello  ", 10, "name")
	if err != nil || got != "Hello" {
		t.Fatalf("trim failed: %q %v", got, err)
	}
	if _, err := CleanCatalogText("line\nbreak", 80, "name"); err == nil {
		t.Fatal("line break should be rejected")
	}
	if _, err := CleanCatalogText("nul\x00byte", 80, "name"); err == nil {
		t.Fatal("NUL should be rejected")
	}
	// Rune-based limit: 3 Chinese characters fit in a limit of 3.
	if _, err := CleanCatalogText("中文名", 3, "name"); err != nil {
		t.Fatalf("rune limit should count characters: %v", err)
	}
	if _, err := CleanCatalogText("中文名", 2, "name"); err == nil {
		t.Fatal("rune limit should reject a longer name")
	}
}

func TestCleanCatalogHomepage(t *testing.T) {
	got, err := CleanCatalogHomepage("")
	if err != nil || got != "" {
		t.Fatalf("empty homepage should stay empty: %q %v", got, err)
	}
	if _, err := CleanCatalogHomepage("javascript:alert(1)"); err == nil {
		t.Fatal("javascript URL should be rejected")
	}
	if _, err := CleanCatalogHomepage("https://u:p@example.com"); err == nil {
		t.Fatal("credentialed homepage should be rejected")
	}
	if _, err := CleanCatalogHomepage("https://github.com/oven-sh/bun"); err != nil {
		t.Fatalf("ordinary homepage rejected: %v", err)
	}
}

func TestExampleCatalogIsUsable(t *testing.T) {
	if len(ExampleCatalog) < 6 {
		t.Fatalf("example catalog is too small: %d", len(ExampleCatalog))
	}
	seen := make(map[string]bool)
	for _, e := range ExampleCatalog {
		if e.Category == "" || e.Name == "" || e.Description == "" || e.Homepage == "" {
			t.Fatalf("incomplete example: %+v", e)
		}
		if _, err := CleanCatalogText(e.Name, MaxCatalogNameLen, "name"); err != nil {
			t.Fatalf("example name: %v", err)
		}
		if _, err := CleanCatalogText(e.Description, MaxCatalogDescriptionLen, "description"); err != nil {
			t.Fatalf("example description: %v", err)
		}
		if _, err := CleanCatalogHomepage(e.Homepage); err != nil {
			t.Fatalf("example homepage: %v", err)
		}
		_, key, err := CatalogURLKey(e.URL)
		if err != nil {
			t.Fatalf("example URL %q: %v", e.URL, err)
		}
		if seen[key] {
			t.Fatalf("duplicate example URL: %q", e.URL)
		}
		seen[key] = true
	}
	categories := ExampleCategoryNames()
	if len(categories) < 2 {
		t.Fatalf("expected several categories, got %v", categories)
	}
	unique := make(map[string]bool)
	for _, c := range categories {
		if unique[c] {
			t.Fatalf("ExampleCategoryNames returned %q twice", c)
		}
		unique[c] = true
	}
}

func TestValidImportStrategy(t *testing.T) {
	for _, ok := range []string{ImportStrategySkip, ImportStrategyOverwrite, ImportStrategyError} {
		if !ValidImportStrategy(ok) {
			t.Fatalf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "merge", "SKIP"} {
		if ValidImportStrategy(bad) {
			t.Fatalf("%q should be invalid", bad)
		}
	}
}
