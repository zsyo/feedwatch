package webhook

import notiftpl "feedwatch/internal/notifier/template"

// ntfy publishes to an ntfy.sh topic (or a self-hosted ntfy server).
// The simplest protocol is POST plain text to the topic URL with Title/Tags
// headers; no account or JSON wrapper is needed.
type ntfy struct{ webhookOnly }

func (ntfy) Type() string { return "ntfy" }

func (ntfy) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	headers := map[string]string{
		"Content-Type": "text/plain; charset=utf-8",
		"Title":        m.Subject,
		"Tags":         o.Or("tags", "arrow_up,package"),
		"Priority":     o.Or("priority", "default"),
	}
	if v := o.Get("token"); v != "" {
		headers["Authorization"] = "Bearer " + v
	}
	return Request{URL: u, Headers: headers, Body: []byte(m.Text)}, nil
}

func (ntfy) Check(status int, body []byte) error { return accept2xx(status, body) }
