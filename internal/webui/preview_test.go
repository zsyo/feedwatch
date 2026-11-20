//go:build webui

package webui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreviewChannelCoversEveryType(t *testing.T) {
	for _, typ := range ChannelTypes() {
		t.Run(typ, func(t *testing.T) {
			p, err := PreviewChannel(typ, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if p.Type != typ || p.Kind != "new_releases" {
				t.Fatalf("type/kind = %q/%q", p.Type, p.Kind)
			}
			if p.Subject == "" || p.Body == "" || p.Payload == "" {
				t.Fatalf("empty preview: %+v", p)
			}
			if !strings.Contains(p.Body, "golang/go") || !strings.Contains(p.Body, "go1.27.4") {
				t.Fatalf("sample feed missing: %s", p.Body)
			}
			if !json.Valid([]byte(p.Payload)) {
				t.Fatalf("payload is not JSON: %s", p.Payload)
			}
			// Credential stand-ins used only to make Build() succeed must never
			// surface. The chat_id stand-in stays visible: it is a plain field
			// and tells the user the form value is still empty.
			for _, leak := range []string{previewWebhookURL, "PREVIEW_TOKEN"} {
				if strings.Contains(p.Body+p.Payload, leak) {
					t.Fatalf("preview leaked %q: %+v", leak, p)
				}
			}
		})
	}
}

func TestPreviewChannelFormatsFollowAdapterSwitches(t *testing.T) {
	cases := []struct {
		typ, kind, want string
		values          map[string]string
	}{
		{"email", "", "html", nil},
		{"telegram", "", "html", nil},
		{"telegram", "", "markdown", map[string]string{"parse_mode": "MarkdownV2"}},
		{"telegram", "", "text", map[string]string{"parse_mode": "text"}},
		{"wecom", "", "markdown", nil},
		{"wecom", "", "text", map[string]string{"msg_type": "text"}},
		{"dingtalk", "", "markdown", nil},
		{"feishu", "", "text", nil},
		{"feishu", "", "markdown", map[string]string{"msg_type": "card"}},
		{"slack", "", "markdown", nil},
		{"discord", "", "markdown", nil},
		{"teams", "", "markdown", nil},
		{"bark", "", "text", nil},
		{"ntfy", "", "text", nil},
		{"webhook", "", "text", nil},
	}
	for _, tc := range cases {
		p, err := PreviewChannel(tc.typ, tc.kind, tc.values)
		if err != nil {
			t.Fatalf("%s %v: %v", tc.typ, tc.values, err)
		}
		if p.Format != tc.want {
			t.Errorf("%s %v format = %q, want %q", tc.typ, tc.values, p.Format, tc.want)
		}
	}
}

func TestPreviewChannelUsesSubjectPrefixAndKind(t *testing.T) {
	// subject_prefix is an email-only channel field: the form never offers it
	// for chat channels, so the preview must not invent one either.
	plain, err := PreviewChannel("email", "new_releases", nil)
	if err != nil {
		t.Fatal(err)
	}
	prefixed, err := PreviewChannel("email", "new_releases", map[string]string{"subject_prefix": "[FW]"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prefixed.Subject, "[FW] ") || strings.HasPrefix(plain.Subject, "[FW]") {
		t.Fatalf("prefix not applied: plain=%q prefixed=%q", plain.Subject, prefixed.Subject)
	}
	if !strings.Contains(prefixed.Payload, "[FW]") {
		t.Fatalf("email envelope should carry the prefixed subject: %s", prefixed.Payload)
	}

	base, err := PreviewChannel("email", "baseline", nil)
	if err != nil {
		t.Fatal(err)
	}
	if base.Kind != "baseline" || !strings.Contains(base.Subject, "版本监控已启动") {
		t.Fatalf("baseline subject = %q", base.Subject)
	}

	chat, err := PreviewChannel("telegram", "new_releases", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chat.Body, chat.Subject) {
		t.Fatalf("chat body should carry the subject as its title: %s", chat.Body)
	}
}

func TestPreviewChannelReflectsChannelOptions(t *testing.T) {
	md, err := PreviewChannel("wecom", "", map[string]string{"msg_type": "markdown"})
	if err != nil {
		t.Fatal(err)
	}
	text, err := PreviewChannel("wecom", "", map[string]string{
		"msg_type": "text", "mentioned_list": "alice,bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.Body, "**") || strings.Contains(text.Body, "**") {
		t.Fatalf("msg_type did not change the body: md=%q text=%q", md.Body, text.Body)
	}
	if !strings.Contains(text.Payload, `"mentioned_list"`) || !strings.Contains(text.Payload, "bob") {
		t.Fatalf("mentioned_list missing from payload: %s", text.Payload)
	}

	bark, err := PreviewChannel("bark", "", map[string]string{"group": "MyGroup", "sound": "bell"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bark.Payload, "MyGroup") || !strings.Contains(bark.Payload, "bell") {
		t.Fatalf("bark options missing from payload: %s", bark.Payload)
	}

	ntfy, err := PreviewChannel("ntfy", "", map[string]string{"tags": "arrow_up", "priority": "high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ntfy.Payload, `"Tags": "arrow_up"`) || !strings.Contains(ntfy.Payload, `"Priority": "high"`) {
		t.Fatalf("ntfy headers missing from payload: %s", ntfy.Payload)
	}
}

func TestPreviewChannelNeverReadsSecrets(t *testing.T) {
	secrets := map[string]string{
		"webhook_url":  "https://very-secret.example.com/hook/token",
		"token":        "SECRET_BOT_TOKEN",
		"secret":       "SECRET_SIGNING_KEY",
		"api_key":      "SECRET_API_KEY",
		"headers_json": `{"X-Secret":"SECRET_HEADER"}`,
	}
	for _, typ := range ChannelTypes() {
		p, err := PreviewChannel(typ, "", secrets)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		for _, leak := range []string{"very-secret.example.com", "SECRET_"} {
			if strings.Contains(p.Body+p.Payload+p.Subject, leak) {
				t.Fatalf("%s preview leaked %q: %+v", typ, leak, p)
			}
		}
	}
}

func TestPreviewChannelRejectsBadInput(t *testing.T) {
	if _, err := PreviewChannel("nope", "", nil); err == nil {
		t.Fatal("unknown type must fail")
	} else if ce, ok := err.(*ChannelError); !ok || ce.Code != "invalid_channel_type" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := PreviewChannel("email", "nope", nil); err == nil {
		t.Fatal("unknown kind must fail")
	} else if ce, ok := err.(*ChannelError); !ok || ce.Code != "invalid_preview_kind" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := PreviewChannel("webhook", "", map[string]string{"body": "{{.Nope"}); err == nil {
		t.Fatal("invalid body template must fail")
	} else if ce, ok := err.(*ChannelError); !ok || ce.Code != "preview_failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}
