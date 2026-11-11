package webhook

import (
	"fmt"
	"unicode/utf8"

	notiftpl "feedwatch/internal/notifier/template"
)

// telegram uses the Bot API sendMessage method.
// POST https://api.telegram.org/bot<token>/sendMessage
// text <= 4096 chars; 429 responses carry parameters.retry_after (handled by
// the shared post loop). HTML parse_mode is the default: unlike MarkdownV2 it
// only needs escaping of & < >, which html/template already does for us.
type telegram struct{}

func (telegram) Type() string { return "telegram" }

func (telegram) Validate(o Options) error {
	if o.Get("chat_id") == "" {
		return fmt.Errorf("option chat_id is required")
	}
	if o.Get("webhook_url") == "" && o.Get("token") == "" {
		return fmt.Errorf("option token is required (or set webhook_url for a self-hosted Bot API)")
	}
	return nil
}

func (telegram) Build(m notiftpl.Message, o Options) (Request, error) {
	chatID := o.Get("chat_id")
	if chatID == "" {
		return Request{}, fmt.Errorf("telegram: option chat_id is required")
	}
	u := o.Get("webhook_url")
	if u == "" {
		token := o.Get("token")
		if token == "" {
			return Request{}, fmt.Errorf("telegram: option token is required (or set webhook_url for a self-hosted Bot API)")
		}
		u = "https://api.telegram.org/bot" + token + "/sendMessage"
	}

	payload := map[string]any{"chat_id": chatID}
	switch mode := o.Or("parse_mode", "HTML"); mode {
	case "HTML":
		// A truncated HTML string would be malformed, so fall back to plain text.
		if utf8.RuneCountInString(m.HTML) <= 4096 {
			payload["text"] = m.HTML
			payload["parse_mode"] = "HTML"
		} else {
			payload["text"] = truncateRunes(m.Text, 4096)
		}
	case "MarkdownV2", "Markdown":
		payload["text"] = truncateRunes(m.Markdown, 4096)
		payload["parse_mode"] = mode
	case "text", "none":
		payload["text"] = truncateRunes(m.Text, 4096)
	default:
		return Request{}, fmt.Errorf("telegram: unsupported parse_mode %q", mode)
	}
	if v := o.Get("disable_web_page_preview"); v == "true" {
		payload["disable_web_page_preview"] = true
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (telegram) Check(_ int, body []byte) error { return checkTelegram(body) }
