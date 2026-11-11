package webhook

import notiftpl "feedwatch/internal/notifier/template"

// teams posts an Adaptive Card to a Teams "Workflows" webhook URL.
// Legacy Office 365 connectors (MessageCard) were retired in May 2026, so the
// Adaptive Card envelope is the supported shape. Card limit is 28 KB.
type teams struct{ webhookOnly }

func (teams) Type() string { return "teams" }

func (teams) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body": []any{
			map[string]any{
				"type": "TextBlock", "text": m.Subject,
				"weight": "Bolder", "size": "Medium", "wrap": true,
			},
			map[string]any{
				"type": "TextBlock", "text": truncateBytes(m.Markdown, 24000), "wrap": true,
			},
		},
	}
	payload := map[string]any{
		"type": "message",
		"attachments": []any{
			map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"contentUrl":  nil,
				"content":     card,
			},
		},
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (teams) Check(status int, body []byte) error { return accept2xx(status, body) }
