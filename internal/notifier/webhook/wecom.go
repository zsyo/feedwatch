package webhook

import (
	"fmt"

	notiftpl "feedwatch/internal/notifier/template"
)

// wecom is the WeCom (企业微信) group robot.
// POST https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...
// markdown content <= 4096 bytes, text content <= 2048 bytes, 20 msgs/min.
type wecom struct{ webhookOnly }

func (wecom) Type() string { return "wecom" }

func (wecom) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	var payload any
	switch kind := o.Or("msg_type", "markdown"); kind {
	case "markdown":
		payload = map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{
				"content": truncateBytes(m.Markdown, 4096),
			},
		}
	case "text":
		text := map[string]any{"content": truncateBytes(m.Text, 2048)}
		if v := o.Get("mentioned_list"); v != "" {
			text["mentioned_list"] = splitCSV(v)
		}
		if v := o.Get("mentioned_mobile_list"); v != "" {
			text["mentioned_mobile_list"] = splitCSV(v)
		}
		payload = map[string]any{"msgtype": "text", "text": text}
	default:
		return Request{}, fmt.Errorf("wecom: unsupported msg_type %q (want markdown or text)", kind)
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (wecom) Check(_ int, body []byte) error { return checkErrCode(body) }
