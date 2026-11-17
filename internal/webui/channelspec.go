//go:build webui

package webui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"feedwatch/internal/config"
)

// ChannelSpec is the JSON shape stored in channels.config_json. It deliberately
// holds no credentials: secrets live in channel_secrets, sealed with the master
// key, and are merged back in memory only while a delivery is being built.
type ChannelSpec struct {
	Type          string            `json:"type"`
	Name          string            `json:"name,omitempty"`
	From          string            `json:"from,omitempty"`
	To            []string          `json:"to,omitempty"`
	Cc            []string          `json:"cc,omitempty"`
	SubjectPrefix string            `json:"subject_prefix,omitempty"`
	Options       map[string]string `json:"options,omitempty"`
}

// FieldScope says where a form value is persisted.
type FieldScope string

const (
	// ScopeSpec writes into the ChannelSpec columns (email envelope fields).
	ScopeSpec FieldScope = "spec"
	// ScopeOption writes into ChannelSpec.Options (non-secret parameters).
	ScopeOption FieldScope = "option"
	// ScopeSecret is sealed into channel_secrets and never echoed back.
	ScopeSecret FieldScope = "secret"
)

// FieldKind maps to the form control the SPA renders.
type FieldKind string

const (
	FieldText     FieldKind = "text"
	FieldPassword FieldKind = "password"
	FieldSelect   FieldKind = "select"
	FieldBool     FieldKind = "bool"
	FieldList     FieldKind = "list"
	FieldTextarea FieldKind = "textarea"
)

// Field describes one form value of a channel type. The SPA mirrors this table
// so both ends agree on labels, required markers and secret handling.
type Field struct {
	Key         string     `json:"key"`
	Label       string     `json:"label"`
	Kind        FieldKind  `json:"kind"`
	Scope       FieldScope `json:"scope"`
	Required    bool       `json:"required,omitempty"`
	Choices     []string   `json:"choices,omitempty"`
	Placeholder string     `json:"placeholder,omitempty"`
	Help        string     `json:"help,omitempty"`
}

// ChannelFields is the single source of truth for what each channel type
// accepts. Keys are exactly the option names read by internal/notifier.
var ChannelFields = map[string][]Field{
	"email": {
		{Key: "from", Label: "发件人 From", Kind: FieldText, Scope: ScopeSpec, Required: true, Placeholder: "notify@example.com"},
		{Key: "to", Label: "收件人 To", Kind: FieldList, Scope: ScopeSpec, Required: true, Help: "多个地址用逗号分隔"},
		{Key: "cc", Label: "抄送 Cc", Kind: FieldList, Scope: ScopeSpec, Help: "选填，逗号分隔"},
		{Key: "subject_prefix", Label: "标题前缀", Kind: FieldText, Scope: ScopeSpec, Placeholder: "选填"},
		{Key: "api_key", Label: "Resend API Key", Kind: FieldPassword, Scope: ScopeSecret, Required: true, Help: "在 Resend 控制台创建，只保存在本机"},
	},
	"telegram": {
		{Key: "token", Label: "Bot Token", Kind: FieldPassword, Scope: ScopeSecret, Help: "@BotFather 生成的 token"},
		{Key: "chat_id", Label: "Chat ID", Kind: FieldText, Scope: ScopeOption, Required: true, Placeholder: "-1001234567890"},
		{Key: "webhook_url", Label: "自建 Bot API 地址（可选）", Kind: FieldPassword, Scope: ScopeSecret, Help: "替换官方 api.telegram.org 时才需要"},
		{Key: "parse_mode", Label: "消息格式", Kind: FieldSelect, Scope: ScopeOption, Choices: []string{"HTML", "MarkdownV2", "Markdown", "text"}},
		{Key: "disable_web_page_preview", Label: "关闭链接预览", Kind: FieldBool, Scope: ScopeOption},
	},
	"wecom": {
		{Key: "webhook_url", Label: "群机器人 Webhook", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
		{Key: "msg_type", Label: "消息类型", Kind: FieldSelect, Scope: ScopeOption, Choices: []string{"markdown", "text"}},
		{Key: "mentioned_list", Label: "提醒成员 userid", Kind: FieldText, Scope: ScopeOption, Help: "选填，逗号分隔"},
		{Key: "mentioned_mobile_list", Label: "提醒手机号", Kind: FieldText, Scope: ScopeOption, Help: "选填，逗号分隔"},
	},
	"feishu": {
		{Key: "webhook_url", Label: "群机器人 Webhook", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
		{Key: "secret", Label: "加签密钥（可选）", Kind: FieldPassword, Scope: ScopeSecret},
		{Key: "msg_type", Label: "消息类型", Kind: FieldSelect, Scope: ScopeOption, Choices: []string{"text", "card"}},
	},
	"dingtalk": {
		{Key: "webhook_url", Label: "群机器人 Webhook", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
		{Key: "secret", Label: "加签密钥（可选）", Kind: FieldPassword, Scope: ScopeSecret},
		{Key: "msg_type", Label: "消息类型", Kind: FieldSelect, Scope: ScopeOption, Choices: []string{"markdown", "text"}},
	},
	"slack": {
		{Key: "webhook_url", Label: "Incoming Webhook 地址", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
	},
	"discord": {
		{Key: "webhook_url", Label: "Webhook 地址", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
		{Key: "username", Label: "发送者名称", Kind: FieldText, Scope: ScopeOption, Placeholder: "选填"},
		{Key: "avatar_url", Label: "头像地址", Kind: FieldText, Scope: ScopeOption, Placeholder: "选填，http(s) 图片地址"},
	},
	"teams": {
		{Key: "webhook_url", Label: "Incoming Webhook 地址", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
	},
	"bark": {
		{Key: "webhook_url", Label: "推送地址", Kind: FieldPassword, Scope: ScopeSecret, Required: true, Help: "通常形如 https://api.day.app/<device_key>"},
		{Key: "device_key", Label: "Device Key（可选）", Kind: FieldPassword, Scope: ScopeSecret, Help: "地址只填服务器根路径时才需要"},
		{Key: "group", Label: "分组", Kind: FieldText, Scope: ScopeOption, Placeholder: "FeedWatch"},
		{Key: "sound", Label: "铃声", Kind: FieldText, Scope: ScopeOption, Placeholder: "选填"},
		{Key: "level", Label: "级别", Kind: FieldText, Scope: ScopeOption, Placeholder: "选填"},
		{Key: "icon", Label: "图标", Kind: FieldText, Scope: ScopeOption, Placeholder: "选填"},
	},
	"ntfy": {
		{Key: "webhook_url", Label: "Topic 地址", Kind: FieldPassword, Scope: ScopeSecret, Required: true, Help: "例如 https://ntfy.sh/my-topic"},
		{Key: "token", Label: "访问令牌（可选）", Kind: FieldPassword, Scope: ScopeSecret},
		{Key: "tags", Label: "标签", Kind: FieldText, Scope: ScopeOption, Placeholder: "arrow_up,package"},
		{Key: "priority", Label: "优先级", Kind: FieldSelect, Scope: ScopeOption, Choices: []string{"min", "low", "default", "high", "max", "urgent"}},
	},
	"webhook": {
		{Key: "webhook_url", Label: "目标地址", Kind: FieldPassword, Scope: ScopeSecret, Required: true},
		{Key: "headers_json", Label: "自定义请求头（JSON 对象）", Kind: FieldTextarea, Scope: ScopeSecret, Placeholder: `{"Authorization": "Bearer xxx"}`, Help: "选填；值会一并加密保存"},
		{Key: "method", Label: "请求方法", Kind: FieldSelect, Scope: ScopeOption, Choices: []string{"POST", "PUT", "PATCH"}},
		{Key: "content_type", Label: "Content-Type", Kind: FieldText, Scope: ScopeOption, Placeholder: "application/json; charset=utf-8"},
		{Key: "body", Label: "请求体模板", Kind: FieldTextarea, Scope: ScopeOption, Help: "留空使用默认 JSON 模板；可用 {{json .Text}} 等占位符"},
	},
}

// channelTypeOrder keeps the UI dropdown stable regardless of map iteration.
// "log" is deliberately absent: a WebUI log channel writes notifications to
// the server log that the account owner cannot see, so choosing it looks like
// a silent delivery failure. Remove it from ChannelFields too so the API
// rejects any attempt to create one. The base (config.yaml) build is a separate
// code path and still supports type: log.
var channelTypeOrder = []string{
	"email", "telegram", "wecom", "feishu", "dingtalk",
	"slack", "discord", "teams", "bark", "ntfy", "webhook",
}

// ChannelError carries a stable machine code so the API can answer 400 with a
// precise reason instead of a generic failure.
type ChannelError struct {
	Code    string
	Message string
}

func (e *ChannelError) Error() string { return e.Message }

func channelErrorf(code, format string, args ...any) error {
	return &ChannelError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ChannelTypes lists the supported types in presentation order.
func ChannelTypes() []string { return append([]string(nil), channelTypeOrder...) }

// FieldsOf returns the field table of a channel type.
func FieldsOf(typ string) ([]Field, bool) {
	fields, ok := ChannelFields[typ]
	return fields, ok
}

// SecretKeys lists the sealed field names of a channel type.
func SecretKeys(typ string) []string {
	fields, ok := ChannelFields[typ]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Scope == ScopeSecret {
			out = append(out, f.Key)
		}
	}
	return out
}

// BuildSpec turns the flat form values of a channel into its persisted shape.
// Only declared, non-secret keys are accepted; anything else is rejected so a
// typo cannot silently create a bucket of dead options.
func BuildSpec(typ, name string, values map[string]string) (ChannelSpec, error) {
	fields, ok := ChannelFields[typ]
	if !ok {
		return ChannelSpec{}, channelErrorf("invalid_channel_type", "unsupported channel type %q", typ)
	}
	spec := ChannelSpec{Type: typ, Name: strings.TrimSpace(name), Options: map[string]string{}}
	declared := make(map[string]Field, len(fields))
	for _, f := range fields {
		declared[f.Key] = f
	}
	for key, raw := range values {
		f, ok := declared[key]
		if !ok {
			return ChannelSpec{}, channelErrorf("unknown_option", "option %q is not supported by %s channels", key, typ)
		}
		if f.Scope == ScopeSecret {
			return ChannelSpec{}, channelErrorf("secret_in_options", "%q is a secret; send it in secrets, not fields", key)
		}
		value := strings.TrimSpace(raw)
		if f.Scope == ScopeSpec {
			switch key {
			case "from":
				spec.From = value
			case "to":
				spec.To = splitList(value)
			case "cc":
				spec.Cc = splitList(value)
			case "subject_prefix":
				spec.SubjectPrefix = value
			}
			continue
		}
		if f.Kind == FieldBool {
			// Adapters treat "true" as on and anything else as off, so only the
			// affirmative value is worth persisting.
			if strings.EqualFold(value, "true") || value == "1" {
				spec.Options[key] = "true"
			}
			continue
		}
		if value != "" {
			spec.Options[key] = value
		}
	}
	if err := ValidateFields(typ, values); err != nil {
		return ChannelSpec{}, err
	}
	return spec, nil
}

// ValidateFields checks the non-secret half of a channel definition.
func ValidateFields(typ string, values map[string]string) error {
	fields, ok := ChannelFields[typ]
	if !ok {
		return channelErrorf("invalid_channel_type", "unsupported channel type %q", typ)
	}
	declared := make(map[string]Field, len(fields))
	for _, f := range fields {
		declared[f.Key] = f
	}
	for key := range values {
		f, ok := declared[key]
		if !ok {
			if looksSecretKey(key) {
				return channelErrorf("secret_not_declared", "%q looks like a credential but %s channels do not use it", key, typ)
			}
			return channelErrorf("unknown_option", "option %q is not supported by %s channels", key, typ)
		}
		if f.Scope == ScopeSecret {
			return channelErrorf("secret_in_options", "%q is a secret; send it in secrets, not fields", key)
		}
	}
	for _, f := range fields {
		if f.Scope != ScopeSecret && f.Required && strings.TrimSpace(values[f.Key]) == "" {
			return channelErrorf("required_field", "%s is required", f.Key)
		}
	}
	switch typ {
	case "email":
		to := splitList(values["to"])
		if len(to) == 0 {
			return channelErrorf("invalid_recipients", "at least one recipient is required")
		}
		if len(to) > config.MaxEmailRecipients {
			return channelErrorf("invalid_recipients", "at most %d recipients are allowed", config.MaxEmailRecipients)
		}
	}
	if avatar := strings.TrimSpace(values["avatar_url"]); avatar != "" {
		if err := checkHTTPURL(avatar); err != nil {
			return channelErrorf("invalid_url", "avatar_url: %v", err)
		}
	}
	return nil
}

// ValidateSecrets checks the secret half of a request: only declared secret
// keys, well-formed URLs, and every required credential available either in
// this request or already stored.
func ValidateSecrets(typ string, supplied map[string]string, stored []string) error {
	fields, ok := ChannelFields[typ]
	if !ok {
		return channelErrorf("invalid_channel_type", "unsupported channel type %q", typ)
	}
	declared := make(map[string]Field, len(fields))
	for _, f := range fields {
		declared[f.Key] = f
	}
	for key, value := range supplied {
		f, ok := declared[key]
		if !ok || f.Scope != ScopeSecret {
			return channelErrorf("unknown_option", "secret field %q is not supported by %s channels", key, typ)
		}
		if value == "" {
			continue
		}
		switch key {
		case "webhook_url":
			if err := checkHTTPURL(value); err != nil {
				return channelErrorf("invalid_url", "webhook_url: %v", err)
			}
		case "headers_json":
			if err := validateHeaderJSON(value); err != nil {
				return err
			}
		}
	}

	available := make(map[string]bool, len(stored)+len(supplied))
	for _, key := range stored {
		available[key] = true
	}
	for key, value := range supplied {
		if value != "" {
			available[key] = true
		}
	}
	for _, f := range fields {
		if f.Scope != ScopeSecret || !f.Required {
			continue
		}
		if available[f.Key] {
			continue
		}
		// Telegram can authenticate either with a bot token or a self-hosted
		// Bot API base URL, so neither field is individually mandatory.
		if typ == "telegram" {
			continue
		}
		return channelErrorf("secret_required", "%s is required", f.Key)
	}
	if typ == "telegram" && !available["token"] && !available["webhook_url"] {
		return channelErrorf("secret_required", "either token or webhook_url is required")
	}
	return nil
}

// SecretHint renders a harmless preview of a sealed value: only scheme://host,
// because the path and query of a webhook URL are usually the credential
// itself (Slack, Discord, Bark, WeCom all embed secrets there).
func SecretHint(_ string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func splitList(v string) []string {
	out := make([]string, 0, 4)
	for part := range strings.SplitSeq(v, ",") {
		if item := strings.TrimSpace(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}

func validateHeaderJSON(raw string) error {
	headers := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return channelErrorf("invalid_headers", "headers_json must be a JSON object of strings")
	}
	for name, value := range headers {
		if strings.TrimSpace(name) == "" {
			return channelErrorf("invalid_headers", "header names must not be empty")
		}
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return channelErrorf("invalid_headers", "header names and values must not contain line breaks")
		}
	}
	return nil
}

// looksSecretKey is only a defensive net: the authoritative classification is
// the explicit Scope in ChannelFields. It catches "unknown key that obviously
// holds a credential" before it can be persisted as a plain option.
func looksSecretKey(key string) bool {
	k := strings.ToLower(key)
	if strings.HasPrefix(k, "header.") {
		return true
	}
	for _, part := range []string{"key", "token", "secret", "password", "passwd", "sign", "auth"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}
