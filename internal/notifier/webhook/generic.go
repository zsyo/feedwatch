package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	texttemplate "text/template"

	notiftpl "feedwatch/internal/notifier/template"
)

// generic ("webhook" in YAML) is an escape hatch for anything not built in:
// Gotify, Pushover, Server酱, Mattermost, a home-grown endpoint, ... The body
// is a text/template rendered against template.Message; use {{json .Text}} etc.
// so dynamic strings are always JSON-escaped. Custom headers use the "header."
// option prefix.
const defaultGenericBody = `{"title":{{json .Subject}},"text":{{json .Text}},"markdown":{{json .Markdown}},"kind":{{json .Kind}},"generated_at":{{json .GeneratedAt}},"count":{{.Count}}}`

type generic struct{ webhookOnly }

func (generic) Type() string { return "webhook" }

func (generic) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	bodyTmpl := o.Or("body", defaultGenericBody)
	t, err := texttemplate.New("webhook.body").Funcs(texttemplate.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
	}).Parse(bodyTmpl)
	if err != nil {
		return Request{}, fmt.Errorf("webhook: invalid body template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, m); err != nil {
		return Request{}, fmt.Errorf("webhook: render body: %w", err)
	}
	contentType := o.Or("content_type", "application/json; charset=utf-8")
	if strings.Contains(contentType, "json") && !json.Valid(buf.Bytes()) {
		return Request{}, fmt.Errorf("webhook: rendered body is not valid JSON: %s", clip(buf.Bytes()))
	}
	headers := map[string]string{"Content-Type": contentType}
	for k, v := range o.Values {
		if name, ok := strings.CutPrefix(k, "header."); ok && strings.TrimSpace(name) != "" {
			headers[strings.TrimSpace(name)] = v
		}
	}
	return Request{
		Method:  strings.ToUpper(o.Or("method", http.MethodPost)),
		URL:     u,
		Headers: headers,
		Body:    buf.Bytes(),
	}, nil
}

func (generic) Check(status int, body []byte) error { return accept2xx(status, body) }
