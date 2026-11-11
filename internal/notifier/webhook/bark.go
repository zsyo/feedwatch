package webhook

import notiftpl "feedwatch/internal/notifier/template"

// bark pushes to the iOS app Bark (api.day.app or a self-hosted server).
// webhook_url is the full POST endpoint (usually .../device_key). When posting
// to a server root, set device_key and it will be included in the JSON body.
type bark struct{ webhookOnly }

func (bark) Type() string { return "bark" }

func (bark) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	payload := map[string]any{
		"title": m.Subject,
		"body":  truncateBytes(m.Text, 8000),
		"group": o.Or("group", "FeedWatch"),
	}
	if v := o.Get("device_key"); v != "" {
		payload["device_key"] = v
	}
	if v := o.Get("sound"); v != "" {
		payload["sound"] = v
	}
	if v := o.Get("level"); v != "" {
		payload["level"] = v
	}
	if v := o.Get("icon"); v != "" {
		payload["icon"] = v
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (bark) Check(_ int, body []byte) error { return checkCode(body) }
