package template

import (
	"strings"
	"testing"
	"time"

	"feedwatch/internal/domain"
)

func sample(kind domain.NotificationKind, count int) domain.Notification {
	items := make([]domain.ReleaseItem, 0, count)
	for i := range count {
		items = append(items, domain.ReleaseItem{
			Feed:        "golang/go",
			Version:     "go1.27." + string(rune('0'+i)),
			URL:         "https://github.com/golang/go/releases/tag/go1.27.3",
			PublishedAt: time.Date(2026, 10, 20, 1, 26, 0, 0, time.UTC),
			IsNew:       kind == domain.KindNewReleases,
		})
	}
	return domain.Notification{Kind: kind, GeneratedAt: time.Now(), Items: items}
}

func TestRenderBaseline(t *testing.T) {
	r, err := Render(sample(domain.KindBaseline, 3), "[FeedWatch]")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Subject, "版本监控已启动") || !strings.HasPrefix(r.Subject, "[FeedWatch]") {
		t.Errorf("subject = %q", r.Subject)
	}
	for _, want := range []string{"FeedWatch 已启动", "首次启动时各项目的最新稳定版本", "此后仅在新版本发布时通知", "go1.27.0", "2026-10"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(r.HTML, "（新）") {
		t.Error("baseline rows must not be marked as new")
	}
	if !strings.Contains(r.Text, "go1.27.0") || !strings.Contains(r.Text, "https://github.com") {
		t.Errorf("plain-text fallback is incomplete: %q", r.Text)
	}
	if strings.Count(r.HTML, "<tr>") != 3 { // one row per item; the header <tr> has attributes
		t.Errorf("unexpected row count in HTML: %d", strings.Count(r.HTML, "<tr>"))
	}
}

func TestRenderNewReleases(t *testing.T) {
	r, err := Render(sample(domain.KindNewReleases, 2), "[FW]")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Subject, "发现 2 个新版本") {
		t.Errorf("subject = %q", r.Subject)
	}
	if !strings.Contains(r.HTML, "（新）") || !strings.Contains(r.HTML, "发现新版本") {
		t.Errorf("new-release markers missing: %q", r.HTML)
	}
	if !strings.Contains(r.Text, "(新)") {
		t.Errorf("plain-text new marker missing: %q", r.Text)
	}
}

// per_feed batches set Scope so the subject names the project; digest and
// baseline notifications must keep their original wording.
func TestSubjectUsesScopeForPerFeedBatches(t *testing.T) {
	n := sample(domain.KindNewReleases, 2)
	n.Scope = "golang/go"
	r, err := Render(n, "[FeedWatch]")
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "[FeedWatch] golang/go 发现 2 个新版本：go1.27.0、go1.27.1" {
		t.Errorf("subject = %q", r.Subject)
	}
	if !strings.Contains(r.HTML, "发现新版本 · golang/go") {
		t.Errorf("heading should name the project too: %q", r.HTML)
	}

	n.Scope = ""
	plain, err := Render(n, "[FeedWatch]")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Subject, "golang/go") {
		t.Errorf("digest subject must stay project-agnostic: %q", plain.Subject)
	}

	baseline := sample(domain.KindBaseline, 2)
	baseline.Scope = "golang/go"
	base, err := Render(baseline, "[FeedWatch]")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(base.Subject, "golang/go") {
		t.Errorf("baseline is always one summary, got %q", base.Subject)
	}
}

func TestSubjectTruncatesLongLists(t *testing.T) {
	r, err := Render(sample(domain.KindNewReleases, 5), "[FW]")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Subject, "等 5 个") {
		t.Errorf("subject = %q", r.Subject)
	}
	if strings.Contains(r.Subject, "go1.27.4") {
		t.Error("subject should list at most 3 versions")
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	n := domain.Notification{Kind: domain.KindNewReleases, Items: []domain.ReleaseItem{
		{Feed: "<script>alert(1)</script>", Version: "v1.2.3", URL: "https://example.com/r", IsNew: true},
	}}
	r, err := Render(n, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<script>") {
		t.Errorf("HTML was not escaped: %q", r.HTML)
	}
	if !strings.Contains(r.HTML, "&lt;script&gt;") {
		t.Errorf("expected escaped content: %q", r.HTML)
	}
}

func TestRenderEmptyAndUnknownKind(t *testing.T) {
	r, err := Render(domain.Notification{Kind: domain.KindBaseline}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HTML, "暂无稳定版本条目") {
		t.Errorf("empty state should be rendered gracefully: %q", r.HTML)
	}
	if _, err := Render(domain.Notification{Kind: "other"}, ""); err == nil {
		t.Error("unknown kind should fail")
	}
}
