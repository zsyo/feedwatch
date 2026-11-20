//go:build webui

package webui

import (
	"encoding/json"
	"maps"
	"strings"
	"time"

	"feedwatch/internal/domain"
	notiftpl "feedwatch/internal/notifier/template"
	webhooknotify "feedwatch/internal/notifier/webhook"
)

// ChannelPreview is the live sample rendered for the channel form. Body is the
// channel's native representation; Payload is the actual outgoing request with
// URLs and credentials removed. Rendering goes through the very same notifier
// template and adapter code used by real deliveries, so the sample cannot drift
// away from what the user will actually receive.
type ChannelPreview struct {
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	Format  string `json:"format"` // html | markdown | text
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Payload string `json:"payload"`
}

// previewWebhookURL is a harmless stand-in. Adapters need a syntactically valid
// URL to build a request, but the preview never sends it and never echoes it.
const previewWebhookURL = "https://preview.invalid/feedwatch"

const previewClipBytes = 32 << 10

// PreviewChannel renders one sample notification for a channel definition.
// Only declared, non-secret values are read: a preview must never depend on a
// stored credential, and must never become a way to echo one back.
func PreviewChannel(typ, kind string, values map[string]string) (ChannelPreview, error) {
	fields, ok := ChannelFields[typ]
	if !ok {
		return ChannelPreview{}, channelErrorf("invalid_channel_type", "unsupported channel type %q", typ)
	}
	noteKind, err := previewKind(kind)
	if err != nil {
		return ChannelPreview{}, err
	}

	// Iterating the declared fields drops unknown keys and every secret before
	// the value ever reaches a template or the response.
	opts := make(map[string]string, len(fields))
	for _, f := range fields {
		if f.Scope == ScopeSecret {
			continue
		}
		if v := strings.TrimSpace(values[f.Key]); v != "" {
			opts[f.Key] = v
		}
	}

	prefix := opts["subject_prefix"]
	note := previewNote(noteKind)
	msg, err := notiftpl.RenderMessage(note, prefix)
	if err != nil {
		return ChannelPreview{}, channelErrorf("preview_failed", "render preview: %v", err)
	}
	out := ChannelPreview{
		Type:    typ,
		Kind:    string(noteKind),
		Subject: msg.Subject,
	}

	if typ == "email" {
		rendered, err := notiftpl.Render(note, prefix)
		if err != nil {
			return ChannelPreview{}, channelErrorf("preview_failed", "render email preview: %v", err)
		}
		out.Format = "html"
		out.Body = clipPreview(rendered.HTML)
		out.Subject = rendered.Subject
		out.Payload = prettyPreview(map[string]any{
			"from":    opts["from"],
			"to":      splitList(opts["to"]),
			"cc":      splitList(opts["cc"]),
			"subject": rendered.Subject,
		})
		return out, nil
	}

	adapter, ok := webhooknotify.Adapters()[typ]
	if !ok {
		return ChannelPreview{}, channelErrorf("invalid_channel_type", "unsupported channel type %q", typ)
	}
	previewOpts := make(map[string]string, len(opts)+2)
	maps.Copy(previewOpts, opts)
	previewOpts["webhook_url"] = previewWebhookURL
	if typ == "telegram" {
		// chat_id is a plain field and may be empty while the form is being
		// filled; the stand-in keeps the preview request well-formed.
		if previewOpts["chat_id"] == "" {
			previewOpts["chat_id"] = "PREVIEW_CHAT_ID"
		}
		previewOpts["token"] = "PREVIEW_TOKEN"
	}
	req, err := adapter.Build(msg, webhooknotify.Options{Values: previewOpts})
	if err != nil {
		return ChannelPreview{}, channelErrorf("preview_failed", "build preview: %v", err)
	}

	format := previewFormat(typ, opts)
	out.Format = format
	out.Body = clipPreview(previewBody(format, msg))
	method := req.Method
	if method == "" {
		method = "POST"
	}
	delete(req.Headers, "Authorization")
	out.Payload = prettyPreview(map[string]any{
		"method":  method,
		"headers": req.Headers,
		"body":    previewRequestBody(req.Body),
	})
	return out, nil
}

func previewKind(kind string) (domain.NotificationKind, error) {
	switch strings.TrimSpace(kind) {
	case "", "new_releases":
		return domain.KindNewReleases, nil
	case "baseline":
		return domain.KindBaseline, nil
	default:
		return "", channelErrorf("invalid_preview_kind", "unsupported preview kind %q", kind)
	}
}

// previewNote uses a fixed date so the sample stays identical while typing.
func previewNote(kind domain.NotificationKind) domain.Notification {
	return domain.Notification{
		Kind:        kind,
		GeneratedAt: time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC),
		Items: []domain.ReleaseItem{{
			Feed:        "golang/go",
			Version:     "go1.27.4",
			URL:         "https://github.com/golang/go/releases/tag/go1.27.4",
			PublishedAt: time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC),
			IsNew:       kind == domain.KindNewReleases,
		}},
	}
}

// previewFormat mirrors each adapter's default and msg_type / parse_mode
// switches so the visual sample uses the same representation as the send.
func previewFormat(typ string, opts map[string]string) string {
	switch typ {
	case "telegram":
		switch opts["parse_mode"] {
		case "", "HTML":
			return "html"
		case "Markdown", "MarkdownV2":
			return "markdown"
		default:
			return "text"
		}
	case "wecom", "dingtalk":
		if opts["msg_type"] == "text" {
			return "text"
		}
		return "markdown"
	case "feishu":
		if opts["msg_type"] == "card" {
			return "markdown"
		}
		return "text"
	case "slack", "discord", "teams":
		return "markdown"
	default: // bark, ntfy, webhook
		return "text"
	}
}

func previewBody(format string, msg notiftpl.Message) string {
	switch format {
	case "html":
		return msg.HTML
	case "markdown":
		return msg.Markdown
	default:
		return msg.Text
	}
}

func previewRequestBody(raw []byte) any {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	return string(raw)
}

func prettyPreview(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return clipPreview(string(b))
}

func clipPreview(s string) string {
	if len(s) <= previewClipBytes {
		return s
	}
	return s[:previewClipBytes] + "\n…（预览已截断）"
}
