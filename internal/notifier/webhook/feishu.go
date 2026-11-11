package webhook

import (
	"fmt"
	"strconv"

	notiftpl "feedwatch/internal/notifier/template"
)

// feishu is the Feishu/Lark custom bot.
// POST https://open.feishu.cn/open-apis/bot/v2/hook/<id>
// Body <= 20 KB; 100/min and 5/s per bot. Optional HMAC signature lives in the
// JSON body as "timestamp" (seconds) and "sign".
type feishu struct{ webhookOnly }

func (feishu) Type() string { return "feishu" }

func (feishu) Build(m notiftpl.Message, o Options) (Request, error) {
	u, err := webhookURL(o)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	switch kind := o.Or("msg_type", "text"); kind {
	case "text":
		payload = map[string]any{
			"msg_type": "text",
			"content":  map[string]any{"text": truncateBytes(m.Text, 19000)},
		}
	case "card":
		payload = map[string]any{
			"msg_type": "interactive",
			"card": map[string]any{
				"schema": "2.0",
				"header": map[string]any{
					"title":    map[string]any{"tag": "plain_text", "content": m.Subject},
					"template": "blue",
				},
				"body": map[string]any{
					"elements": []any{
						map[string]any{"tag": "markdown", "content": truncateBytes(m.Markdown, 19000)},
					},
				},
			},
		}
	default:
		return Request{}, fmt.Errorf("feishu: unsupported msg_type %q (want text or card)", kind)
	}
	if secret := o.Get("secret"); secret != "" {
		ts := o.Time().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = feishuSign(secret, ts)
	}
	body, err := jsonBody(payload)
	return Request{URL: u, Body: body}, err
}

func (feishu) Check(_ int, body []byte) error { return checkCode(body) }
