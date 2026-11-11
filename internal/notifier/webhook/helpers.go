package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func jsonBody(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return b, nil
}

func webhookURL(o Options) (string, error) {
	u := o.Get("webhook_url")
	if u == "" {
		return "", fmt.Errorf("option webhook_url is required (use ${ENV_VAR} so the URL/token stays out of YAML)")
	}
	return u, nil
}

// webhookOnly is embedded by every adapter whose only mandatory credential is
// webhook_url. Telegram overrides Validate (it can build the URL from a token).
type webhookOnly struct{}

func (webhookOnly) Validate(o Options) error {
	_, err := webhookURL(o)
	return err
}

// truncateRunes keeps at most max characters (used by Telegram / Discord).
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// truncateBytes keeps at most max UTF-8 bytes without splitting a rune
// (WeCom / DingTalk document their limits in bytes).
func truncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func splitCSV(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// appendQuery adds signed parameters to a webhook URL that may already have a
// query string (DingTalk webhook URLs always carry access_token).
func appendQuery(raw string, q url.Values) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	base := u.Query()
	for k, vs := range q {
		for _, v := range vs {
			base.Set(k, v)
		}
	}
	u.RawQuery = base.Encode()
	return u.String(), nil
}

// hmacSHA256Base64 computes HMAC-SHA256 and returns its base64 encoding.
// Feishu and DingTalk use the same primitive but different key/message inputs.
func hmacSHA256Base64(key, message string) string {
	h := hmac.New(sha256.New, []byte(key))
	_, _ = h.Write([]byte(message))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// feishuSign: key = timestamp + "\n" + secret, message = "" (per official doc).
func feishuSign(secret string, ts int64) string {
	return hmacSHA256Base64(strconv.FormatInt(ts, 10)+"\n"+secret, "")
}

// dingtalkSign: key = secret, message = timestamp(ms) + "\n" + secret.
// Returns the raw base64 value; appendQuery's url.Values.Encode() performs
// the required urlencode (quote_plus) exactly once.
func dingtalkSign(secret string, tsMillis int64) string {
	ts := strconv.FormatInt(tsMillis, 10)
	return hmacSHA256Base64(secret, ts+"\n"+secret)
}

var (
	mdLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	boldPattern   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// slackify converts the shared Markdown subset into Slack mrkdwn:
// [label](url) -> <url|label> and **bold** -> *bold*.
func slackify(s string) string {
	s = mdLinkPattern.ReplaceAllString(s, "<$2|$1>")
	return boldPattern.ReplaceAllString(s, "*$1*")
}

// ---- vendor response checks (all called for 2xx only) ----

type errCodeResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func checkErrCode(body []byte) error {
	var r errCodeResponse
	if json.Unmarshal(bytes.TrimSpace(body), &r) != nil {
		return nil // non-JSON success body (or empty)
	}
	if r.ErrCode != 0 {
		return fmt.Errorf("vendor error errcode=%d errmsg=%q", r.ErrCode, r.ErrMsg)
	}
	return nil
}

type codeResponse struct {
	Code          int    `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

func checkCode(body []byte) error {
	var r codeResponse
	if json.Unmarshal(bytes.TrimSpace(body), &r) != nil {
		return nil
	}
	if r.Code != 0 && r.Code != 200 {
		return fmt.Errorf("vendor error code=%d msg=%q", r.Code, r.Msg)
	}
	if r.StatusCode != 0 && r.StatusCode != 200 {
		return fmt.Errorf("vendor error StatusCode=%d StatusMessage=%q", r.StatusCode, r.StatusMessage)
	}
	return nil
}

func checkTelegram(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	if !r.OK {
		return fmt.Errorf("telegram error %d: %s", r.ErrorCode, r.Description)
	}
	return nil
}

func accept2xx(_ int, _ []byte) error { return nil }

// stableTime is a helper for tests that need a fixed signing timestamp.
func stableTime(t time.Time) func() time.Time { return func() time.Time { return t } }
