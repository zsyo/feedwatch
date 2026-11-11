package webhook

import notiftpl "feedwatch/internal/notifier/template"

// slack posts to an Incoming Webhook URL. Slack returns HTTP 200 with the
// plain body "ok"; failures use non-2xx status codes, so Check is a no-op.
// Incoming webhooks cannot override username/channel, so only "text" is sent.
type slack struct{ webhookOnly }

func (slack) Type() string { return "slack" }

func (slack) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	body, err := jsonBody(map[string]any{"text": slackify(m.Markdown)})
	return Request{URL: u, Body: body}, err
}

func (slack) Check(_ int, _ []byte) error { return nil }
