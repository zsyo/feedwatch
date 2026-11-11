package template

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	texttemplate "text/template"

	"feedwatch/internal/domain"
)

// Chat channels do not want a full HTML email. RenderMessage produces three
// channel-neutral representations of the same Notification:
//
//	Subject  - short title (shared with the email)
//	Text     - plain text (shared with the email's text fallback)
//	Markdown - common subset understood by WeCom / DingTalk / Discord / Feishu
//	HTML     - minimal safe HTML for Telegram's parse_mode=HTML

//go:embed chat.md.tmpl chat.html.tmpl
var chatFiles embed.FS

var (
	mdTmpl = texttemplate.Must(texttemplate.New("chat.md.tmpl").ParseFS(chatFiles, "chat.md.tmpl"))
	htTmpl = htmltemplate.Must(htmltemplate.New("chat.html.tmpl").ParseFS(chatFiles, "chat.html.tmpl"))
)

type MessageItem struct {
	Feed    string
	Version string
	URL     string
	Date    string
	IsNew   bool
}

// Message is consumed by every webhook channel adapter.
type Message struct {
	Subject     string
	Text        string
	Markdown    string
	HTML        string
	Kind        domain.NotificationKind
	GeneratedAt string
	Count       int
	Items       []MessageItem
}

type chatView struct {
	Subject     string
	Intro       string
	Footer      string
	GeneratedAt string
	Items       []MessageItem
}

func RenderMessage(n domain.Notification, subjectPrefix string) (Message, error) {
	r, err := Render(n, subjectPrefix)
	if err != nil {
		return Message{}, err
	}
	v := chatView{
		Subject:     r.Subject,
		GeneratedAt: formatDate(n.GeneratedAt),
		Items:       make([]MessageItem, 0, len(n.Items)),
	}
	switch n.Kind {
	case domain.KindBaseline:
		v.Intro = "首次启动，以下是各项目当前最新稳定版本："
		v.Footer = "此后仅在新版本发布时通知。"
	case domain.KindNewReleases:
		v.Intro = "检测到以下新稳定版本："
		v.Footer = "本消息由 FeedWatch 自动发送。"
	default:
		return Message{}, fmt.Errorf("template: unknown notification kind %q", n.Kind)
	}
	for _, it := range n.Items {
		v.Items = append(v.Items, MessageItem{
			Feed: it.Feed, Version: it.Version, URL: it.URL,
			Date: formatDate(it.PublishedAt), IsNew: it.IsNew,
		})
	}

	var mdBuf, htmlBuf bytes.Buffer
	if err := mdTmpl.Execute(&mdBuf, v); err != nil {
		return Message{}, fmt.Errorf("template: render markdown: %w", err)
	}
	if err := htTmpl.Execute(&htmlBuf, v); err != nil {
		return Message{}, fmt.Errorf("template: render chat html: %w", err)
	}
	return Message{
		Subject:     r.Subject,
		Text:        r.Text,
		Markdown:    mdBuf.String(),
		HTML:        htmlBuf.String(),
		Kind:        n.Kind,
		GeneratedAt: v.GeneratedAt,
		Count:       len(n.Items),
		Items:       v.Items,
	}, nil
}
