// Package template renders a domain.Notification into an email subject, an
// HTML body and a plain-text fallback. Templates are embedded in the binary
// via go:embed, so the Docker image needs no extra files.
package template

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"feedwatch/internal/domain"
)

//go:embed email.html.tmpl email.txt.tmpl
var files embed.FS

var (
	htmlTmpl = htmltemplate.Must(htmltemplate.New("email.html.tmpl").ParseFS(files, "email.html.tmpl"))
	textTmpl = texttemplate.Must(texttemplate.New("email.txt.tmpl").ParseFS(files, "email.txt.tmpl"))
)

type Rendered struct {
	Subject string
	HTML    string
	Text    string
}

type itemView struct {
	Feed    string
	Version string
	URL     string
	Date    string
	IsNew   bool
}

type viewData struct {
	Heading     string
	Intro       string
	Footer      string
	GeneratedAt string
	Count       int
	Items       []itemView
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(time.Local).Format("2006-01-02 15:04")
}

func subjectFor(n domain.Notification, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if n.Kind == domain.KindBaseline {
		return strings.TrimSpace(prefix + " 版本监控已启动 · 当前版本基线")
	}
	limit := min(len(n.Items), 3)
	versions := make([]string, 0, limit)
	for _, it := range n.Items[:limit] {
		versions = append(versions, it.Version)
	}
	detail := strings.Join(versions, "、")
	if len(n.Items) > 3 {
		detail += fmt.Sprintf(" 等 %d 个", len(n.Items))
	}
	return strings.TrimSpace(fmt.Sprintf("%s 发现 %d 个新版本：%s", prefix, len(n.Items), detail))
}

// Render produces the subject and both bodies. Both notification kinds share
// one template; only the heading/intro/footer/subject differ.
func Render(n domain.Notification, subjectPrefix string) (Rendered, error) {
	d := viewData{
		Count:       len(n.Items),
		GeneratedAt: formatDate(n.GeneratedAt),
		Items:       make([]itemView, 0, len(n.Items)),
	}
	switch n.Kind {
	case domain.KindBaseline:
		d.Heading = "FeedWatch 已启动"
		d.Intro = "以下是首次启动时各项目的最新稳定版本："
		d.Footer = "此后仅在新版本发布时通知。"
	case domain.KindNewReleases:
		d.Heading = "发现新版本"
		d.Intro = "自上次检查以来，检测到以下新稳定版本："
		d.Footer = "本邮件由 FeedWatch 自动发送。"
	default:
		return Rendered{}, fmt.Errorf("template: unknown notification kind %q", n.Kind)
	}
	for _, it := range n.Items {
		d.Items = append(d.Items, itemView{
			Feed:    it.Feed,
			Version: it.Version,
			URL:     it.URL,
			Date:    formatDate(it.PublishedAt),
			IsNew:   it.IsNew,
		})
	}

	var htmlBuf, textBuf bytes.Buffer
	if err := htmlTmpl.Execute(&htmlBuf, d); err != nil {
		return Rendered{}, fmt.Errorf("template: render html: %w", err)
	}
	if err := textTmpl.Execute(&textBuf, d); err != nil {
		return Rendered{}, fmt.Errorf("template: render text: %w", err)
	}
	return Rendered{
		Subject: subjectFor(n, subjectPrefix),
		HTML:    htmlBuf.String(),
		Text:    textBuf.String(),
	}, nil
}
