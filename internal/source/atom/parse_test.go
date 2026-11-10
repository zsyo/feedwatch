package atom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseAtomFixture(t *testing.T) {
	rs, err := parseFeed(fixture(t, "go-releases.atom"), "golang/go", policyText)
	if err != nil {
		t.Fatalf("parseFeed: %v", err)
	}
	if len(rs) != 4 {
		t.Fatalf("got %d entries, want 4", len(rs))
	}
	first := rs[0]
	if first.ID != "tag:github.com,2008:Repository/23096959/go1.27.3" {
		t.Errorf("id = %q", first.ID)
	}
	if first.URL != "https://github.com/golang/go/releases/tag/go1.27.3" {
		t.Errorf("alternate link not selected: %q", first.URL)
	}
	if first.Feed != "golang/go" || first.Title != "go1.27.3" || first.CoreVersion != "1.27.3" {
		t.Errorf("unexpected normalisation: %+v", first)
	}
	if !first.PublishedAt.Equal(first.UpdatedAt) {
		t.Errorf("published should fall back to updated: %v vs %v", first.PublishedAt, first.UpdatedAt)
	}
	if first.Summary != "[release-branch.go1.27] go1.27.3 Security fixes." {
		t.Errorf("summary = %q", first.Summary)
	}

	if !rs[2].PublishedAt.Equal(time.Date(2026, 10, 8, 15, 36, 10, 0, time.UTC)) {
		t.Errorf("published was not parsed: %v", rs[2].PublishedAt)
	}
	if rs[2].Summary != "Patch release" {
		t.Errorf("summary should fall back to <summary>: %q", rs[2].Summary)
	}
	if rs[3].ID != "https://github.com/golang/go/releases/tag/no-id" {
		t.Errorf("missing <id> should fall back to URL: %q", rs[3].ID)
	}
}

func TestContentPolicies(t *testing.T) {
	data := fixture(t, "go-releases.atom")
	none, err := parseFeed(data, "x", policyNone)
	if err != nil {
		t.Fatal(err)
	}
	if none[0].Summary != "" {
		t.Errorf("policy none should not fill summary: %q", none[0].Summary)
	}
	html, err := parseFeed(data, "x", policyHTML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html[0].ContentHTML, "<p>") {
		t.Errorf("policy html should keep raw HTML: %q", html[0].ContentHTML)
	}
	if html[0].Summary == "" {
		t.Error("policy html should still fill the text summary")
	}
}

func TestParseRSSFixture(t *testing.T) {
	rs, err := parseFeed(fixture(t, "rss20.xml"), "example", policyText)
	if err != nil {
		t.Fatalf("parseFeed RSS: %v", err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d entries, want 2", len(rs))
	}
	if rs[0].ID != "https://example.com/releases/2.4.1" {
		t.Errorf("guid should be used as id: %q", rs[0].ID)
	}
	if rs[0].PublishedAt.IsZero() {
		t.Error("RFC1123Z pubDate was not parsed")
	}
	if rs[1].ID != "https://example.com/releases/2.4.0" {
		t.Errorf("missing guid should fall back to link: %q", rs[1].ID)
	}
	if rs[1].PublishedAt.IsZero() {
		t.Error("short date pubDate was not parsed")
	}
	if rs[0].Summary != "Bug fixes" {
		t.Errorf("summary = %q", rs[0].Summary)
	}
}

func TestBOMAndUnsupportedRoot(t *testing.T) {
	bom := append([]byte{0xef, 0xbb, 0xbf}, fixture(t, "rss20.xml")...)
	if _, err := parseFeed(bom, "x", policyText); err != nil {
		t.Errorf("UTF-8 BOM should be accepted: %v", err)
	}
	if _, err := parseFeed([]byte(`<html><body>hi</body></html>`), "x", policyText); err == nil {
		t.Error("unsupported root should fail")
	}
	if _, err := parseFeed([]byte(`<feed><entry>`), "x", policyText); err == nil {
		t.Error("malformed XML should fail")
	}
	if _, err := parseFeed(nil, "x", policyText); err == nil {
		t.Error("empty body should fail")
	}
}

func TestParseTimeIsForgiving(t *testing.T) {
	for _, s := range []string{
		"2026-10-20T01:26:06Z",
		"2026-10-20T01:26:06.123456789Z",
		"Tue, 20 Oct 2026 01:26:06 +0000",
		"2026-10-20",
	} {
		if parseTime(s).IsZero() {
			t.Errorf("failed to parse %q", s)
		}
	}
	if !parseTime("not a date").IsZero() {
		t.Error("invalid date should return the zero time, not an error")
	}
}

func TestStripHTMLTruncatesAndEscapes(t *testing.T) {
	long := "<script>alert(1)</script><p>" + strings.Repeat("字", 300) + "</p>"
	got := stripHTML(long)
	if strings.Contains(got, "alert") || strings.Contains(got, "<p>") {
		t.Errorf("HTML not stripped: %q", got)
	}
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != maxSummaryRunes+1 {
		t.Errorf("expected truncation to %d runes + ellipsis, got %d", maxSummaryRunes, len([]rune(got)))
	}
	if stripHTML("a &amp; b &lt;tag&gt;") != "a & b <tag>" {
		t.Errorf("entities not unescaped: %q", stripHTML("a &amp; b &lt;tag&gt;"))
	}
}
