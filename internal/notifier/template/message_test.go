package template

import (
	"strings"
	"testing"
	"time"

	"feedwatch/internal/domain"
)

func chatNote() domain.Notification {
	return domain.Notification{
		Kind:        domain.KindNewReleases,
		GeneratedAt: time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC),
		Items: []domain.ReleaseItem{
			{Feed: "golang/go", Version: "go1.27.3", URL: "https://example.com/a?x=1&y=2", IsNew: true},
			{Feed: "rust-lang/rust", Version: "1.99.1", URL: "https://example.com/b", IsNew: true},
		},
	}
}

func TestRenderMessageAllRepresentations(t *testing.T) {
	m, err := RenderMessage(chatNote(), "[FeedWatch]")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Subject, "2 个新版本") || !strings.Contains(m.Subject, "go1.27.3") {
		t.Errorf("subject = %q", m.Subject)
	}
	for name, body := range map[string]string{"text": m.Text, "markdown": m.Markdown, "html": m.HTML} {
		if !strings.Contains(body, "golang/go") || !strings.Contains(body, "1.99.1") {
			t.Errorf("%s lost items: %s", name, body)
		}
	}
	if !strings.Contains(m.Markdown, "[Release 详情](https://example.com/a?x=1&y=2)") {
		t.Errorf("markdown link missing: %s", m.Markdown)
	}
	// html/template must escape the ampersand inside href, not emit raw &.
	if !strings.Contains(m.HTML, "x=1&amp;y=2") {
		t.Errorf("HTML href not escaped: %s", m.HTML)
	}
	if m.Count != 2 || len(m.Items) != 2 {
		t.Errorf("Count/Items = %d/%d", m.Count, len(m.Items))
	}
	if m.Kind != domain.KindNewReleases || m.GeneratedAt == "" {
		t.Errorf("metadata lost: %+v", m)
	}
}

func TestRenderMessageBaselineAndUnknownKind(t *testing.T) {
	n := chatNote()
	n.Kind = domain.KindBaseline
	m, err := RenderMessage(n, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Markdown, "首次启动") || !strings.Contains(m.Markdown, "此后仅在新版本") {
		t.Errorf("baseline intro/footer missing: %s", m.Markdown)
	}
	n.Kind = domain.NotificationKind("other")
	if _, err := RenderMessage(n, ""); err == nil {
		t.Error("unknown kind must fail")
	}
}
