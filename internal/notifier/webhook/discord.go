package webhook

import notiftpl "feedwatch/internal/notifier/template"

// discord executes a webhook. content <= 2000 characters; the default response
// is 204 No Content. allowed_mentions.parse=[] prevents a version string from
// accidentally pinging @everyone.
type discord struct{ webhookOnly }

func (discord) Type() string { return "discord" }

func (discord) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	payload := map[string]any{
		"content":          truncateRunes(m.Markdown, 2000),
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	if v := o.Get("username"); v != "" {
		payload["username"] = truncateRunes(v, 80)
	}
	if v := o.Get("avatar_url"); v != "" {
		payload["avatar_url"] = v
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (discord) Check(status int, body []byte) error { return accept2xx(status, body) }
