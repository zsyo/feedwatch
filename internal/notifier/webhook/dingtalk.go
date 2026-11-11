package webhook

import (
	"fmt"
	"net/url"
	"strconv"

	notiftpl "feedwatch/internal/notifier/template"
)

// dingtalk is the DingTalk (钉钉) custom robot, 20 msgs/min.
// The optional 加签 secret is sent as timestamp (milliseconds) + sign query
// parameters; sign is url-encoded base64 HMAC-SHA256.
type dingtalk struct{ webhookOnly }

func (dingtalk) Type() string { return "dingtalk" }

func (dingtalk) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	if secret := o.Get("secret"); secret != "" {
		ts := o.Time().UnixMilli()
		u, err = appendQuery(u, url.Values{
			"timestamp": {strconv.FormatInt(ts, 10)},
			"sign":      {dingtalkSign(secret, ts)},
		})
		if err != nil {
			return Request{}, fmt.Errorf("dingtalk: sign url: %w", err)
		}
	}
	var payload any
	switch kind := o.Or("msg_type", "markdown"); kind {
	case "markdown":
		payload = map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{
				"title": m.Subject,
				"text":  truncateBytes(m.Markdown, 19000),
			},
		}
	case "text":
		payload = map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": truncateBytes(m.Text, 19000)},
		}
	default:
		return Request{}, fmt.Errorf("dingtalk: unsupported msg_type %q (want markdown or text)", kind)
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (dingtalk) Check(_ int, body []byte) error { return checkErrCode(body) }
