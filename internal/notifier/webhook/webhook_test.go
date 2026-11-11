package webhook

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
	notiftpl "feedwatch/internal/notifier/template"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testNote() domain.Notification {
	return domain.Notification{
		Kind:        domain.KindNewReleases,
		GeneratedAt: time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC),
		Items: []domain.ReleaseItem{
			{
				Feed: "golang/go", Version: "go1.27.3",
				URL:         "https://github.com/golang/go/releases/tag/go1.27.3",
				PublishedAt: time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC), IsNew: true,
			},
		},
	}
}

type captured struct {
	method   string
	rawQuery string
	header   http.Header
	body     []byte
}

// sendWith spins up an in-memory HTTP server, runs one adapter against it and
// returns the request that reached the handler. Client() must be called first:
// it starts the fake network and assigns srv.URL.
func sendWith(t *testing.T, a Adapter, opts map[string]string, respond func(w http.ResponseWriter, r *http.Request)) captured {
	t.Helper()
	var got captured
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.rawQuery = r.URL.RawQuery
		got.header = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		respond(w, r)
	}))
	client := srv.Client()
	merged := map[string]string{
		"webhook_url": srv.URL, "retry_max": "0", "retry_base_delay": "1ms",
	}
	maps.Copy(merged, opts)
	n, err := New(a, notifier.NotifierSpec{Type: a.Type(), Options: merged, Client: client}, quietLogger())
	if err != nil {
		t.Fatalf("New(%s): %v", a.Type(), err)
	}
	if err := n.Send(t.Context(), testNote()); err != nil {
		t.Fatalf("Send(%s): %v", a.Type(), err)
	}
	return got
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, b)
	}
	return m
}

func TestAdaptersRegistryIsConsistent(t *testing.T) {
	all := Adapters()
	if len(all) != 10 {
		t.Errorf("expected 10 built-in channels, got %d", len(all))
	}
	for key, a := range all {
		if a.Type() != key {
			t.Errorf("adapter %q reports Type() %q", key, a.Type())
		}
	}
}

func TestEveryAdapterPayload(t *testing.T) {
	cases := []struct {
		typ     string
		extra   map[string]string
		respond func(w http.ResponseWriter, r *http.Request)
		assert  func(t *testing.T, got captured)
	}{
		{
			typ: "wecom",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{"errcode":0,"errmsg":"ok"}`)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				if m["msgtype"] != "markdown" {
					t.Errorf("msgtype = %v", m["msgtype"])
				}
				content := m["markdown"].(map[string]any)["content"].(string)
				if !strings.Contains(content, "go1.27.3") || !strings.Contains(content, "](https://") {
					t.Errorf("unexpected markdown: %s", content)
				}
			},
		},
		{
			typ: "feishu",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{"code":0,"msg":"success","StatusCode":0}`)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				if m["msg_type"] != "text" {
					t.Errorf("msg_type = %v", m["msg_type"])
				}
			},
		},
		{
			typ: "dingtalk",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{"errcode":0,"errmsg":"ok"}`)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				md := m["markdown"].(map[string]any)
				if md["title"] == "" || !strings.Contains(md["text"].(string), "go1.27.3") {
					t.Errorf("bad dingtalk markdown: %v", md)
				}
			},
		},
		{
			typ:   "telegram",
			extra: map[string]string{"chat_id": "@mychannel", "token": "123:ABC"},
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{"ok":true,"result":{"message_id":1}}`)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				if m["chat_id"] != "@mychannel" || m["parse_mode"] != "HTML" {
					t.Errorf("bad telegram payload: %v", m)
				}
				if !strings.Contains(m["text"].(string), "<a href=") {
					t.Errorf("telegram HTML link missing: %v", m["text"])
				}
			},
		},
		{
			typ: "slack",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				text := m["text"].(string)
				if !strings.Contains(text, "<https://github.com/") || strings.Contains(text, "**") {
					t.Errorf("slack text not converted to mrkdwn: %s", text)
				}
			},
		},
		{
			typ: "discord",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				if !strings.Contains(m["content"].(string), "go1.27.3") {
					t.Errorf("discord content missing version")
				}
			},
		},
		{
			typ: "teams",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				atts := m["attachments"].([]any)
				card := atts[0].(map[string]any)["content"].(map[string]any)
				if card["$schema"] == nil || card["type"] != "AdaptiveCard" {
					t.Errorf("teams adaptive card shape wrong: %v", card)
				}
			},
		},
		{
			typ: "bark",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{"code":200,"message":"success"}`)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				if m["title"] == "" || m["body"] == "" || m["group"] != "FeedWatch" {
					t.Errorf("bad bark payload: %v", m)
				}
			},
		},
		{
			typ: "ntfy",
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{"id":"abc"}`)
			},
			assert: func(t *testing.T, got captured) {
				if got.header.Get("Content-Type") != "text/plain; charset=utf-8" {
					t.Errorf("ntfy content-type = %q", got.header.Get("Content-Type"))
				}
				if got.header.Get("Title") == "" || got.header.Get("Tags") == "" {
					t.Errorf("ntfy headers missing: %v", got.header)
				}
				if !strings.Contains(string(got.body), "go1.27.3") {
					t.Errorf("ntfy body missing version")
				}
			},
		},
		{
			typ: "webhook",
			extra: map[string]string{
				"body":           `{"text":{{json .Text}},"count":{{.Count}},"kind":{{json .Kind}}}`,
				"header.X-Token": "abc123",
				"content_type":   "application/json",
			},
			respond: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, 200, `{}`)
			},
			assert: func(t *testing.T, got captured) {
				m := decode(t, got.body)
				if m["count"] != float64(1) || m["kind"] != "new_releases" {
					t.Errorf("bad generic payload: %v", m)
				}
				if got.header.Get("X-Token") != "abc123" {
					t.Errorf("custom header missing: %v", got.header)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			all := Adapters()
			got := sendWith(t, all[tc.typ], tc.extra, tc.respond)
			if got.method != http.MethodPost {
				t.Errorf("method = %s, want POST", got.method)
			}
			tc.assert(t, got)
		})
	}
}

func TestVendorErrorCodeFailsWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 200, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
	}))
	client := srv.Client()
	n, err := New(wecom{}, notifier.NotifierSpec{
		Options: map[string]string{"webhook_url": srv.URL, "retry_max": "3", "retry_base_delay": "1ms"},
		Client:  client,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(t.Context(), testNote()); err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("expected vendor error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("vendor errors must not be retried, got %d calls", calls.Load())
	}
}

func TestRetryOn429And5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			writeJSON(w, 429, `{"errcode":1}`)
		case 2:
			writeJSON(w, 503, `busy`)
		default:
			writeJSON(w, 200, `{"errcode":0}`)
		}
	}))
	client := srv.Client()
	n, err := New(wecom{}, notifier.NotifierSpec{
		Options: map[string]string{"webhook_url": srv.URL, "retry_max": "3", "retry_base_delay": "1ms"},
		Client:  client,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(t.Context(), testNote()); err != nil {
		t.Fatalf("expected eventual success: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

type failingTransport struct{ calls *atomic.Int32 }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, errors.New("dial failed")
}

func TestNetworkErrorRetriesThenFails(t *testing.T) {
	var calls atomic.Int32
	n, err := New(wecom{}, notifier.NotifierSpec{
		Options: map[string]string{"webhook_url": "https://example.invalid/hook", "retry_max": "2", "retry_base_delay": "1ms"},
		Client:  &http.Client{Transport: failingTransport{calls: &calls}},
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(t.Context(), testNote()); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 3 { // initial + 2 retries
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestSignaturesUseOfficialAlgorithms(t *testing.T) {
	fixed := time.Unix(1700000000, 0).UTC()
	// Vectors computed independently with openssl:
	//   feishu   = HMAC(key="1700000000\nSECtest", msg="")
	//   dingtalk = HMAC(key="SECtest", msg="1700000000000\nSECtest")
	if got := feishuSign("SECtest", 1700000000); got != "G7XpBpG8NgG02fJOAhX6FRAObIljmFoxVReo8I62pEk=" {
		t.Errorf("feishu sign = %q", got)
	}
	if got := dingtalkSign("SECtest", 1700000000000); got != "aZLLrriXgn05YbwaGR7knYsLeJADjr9NwLaNNKpxh4g=" {
		t.Errorf("dingtalk sign = %q", got)
	}

	var feishuBody []byte
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feishuBody, _ = io.ReadAll(r.Body)
		writeJSON(w, 200, `{"code":0,"msg":"success"}`)
	}))
	client := srv.Client()
	n, err := New(feishu{}, notifier.NotifierSpec{
		Options: map[string]string{"webhook_url": srv.URL, "secret": "SECtest", "retry_max": "0"},
		Client:  client,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	n.(*Notifier).now = stableTime(fixed)
	if err := n.Send(t.Context(), testNote()); err != nil {
		t.Fatal(err)
	}
	m := decode(t, feishuBody)
	if m["timestamp"] != "1700000000" || m["sign"] != "G7XpBpG8NgG02fJOAhX6FRAObIljmFoxVReo8I62pEk=" {
		t.Errorf("feishu body signature wrong: %v", m)
	}

	var dingQuery string
	srv2 := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dingQuery = r.URL.RawQuery
		writeJSON(w, 200, `{"errcode":0}`)
	}))
	client2 := srv2.Client()
	n2, err := New(dingtalk{}, notifier.NotifierSpec{
		Options: map[string]string{"webhook_url": srv2.URL + "?access_token=xxx", "secret": "SECtest", "retry_max": "0"},
		Client:  client2,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	n2.(*Notifier).now = stableTime(fixed)
	if err := n2.Send(t.Context(), testNote()); err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(dingQuery)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("access_token") != "xxx" || q.Get("timestamp") != "1700000000000" {
		t.Errorf("dingtalk query lost: %s", dingQuery)
	}
	if q.Get("sign") != "aZLLrriXgn05YbwaGR7knYsLeJADjr9NwLaNNKpxh4g=" {
		t.Errorf("dingtalk sign double-encoded or wrong: %q", q.Get("sign"))
	}
}

func TestStartupValidationFailsFast(t *testing.T) {
	cases := []struct {
		name string
		a    Adapter
		opts map[string]string
		want string
	}{
		{"missing url", wecom{}, nil, "webhook_url"},
		{"empty url", feishu{}, map[string]string{"webhook_url": "  "}, "webhook_url"},
		{"telegram no chat", telegram{}, map[string]string{"token": "123:ABC"}, "chat_id"},
		{"telegram no token", telegram{}, map[string]string{"chat_id": "@x"}, "token"},
		{"bad timeout", wecom{}, map[string]string{"webhook_url": "https://x.test", "timeout": "later"}, "invalid timeout"},
		{"negative retry", wecom{}, map[string]string{"webhook_url": "https://x.test", "retry_max": "-1"}, "invalid retry_max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.a, notifier.NotifierSpec{Options: tc.opts}, quietLogger())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := New(telegram{}, notifier.NotifierSpec{
		Options: map[string]string{"chat_id": "@x", "token": "123:ABC"},
	}, quietLogger()); err != nil {
		t.Errorf("valid telegram config rejected: %v", err)
	}
}

func TestTelegramRetryAfterHint(t *testing.T) {
	body := []byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
	if got := telegramRetryAfter(body); got != 3*time.Second {
		t.Errorf("telegramRetryAfter = %v", got)
	}
	if got := telegramRetryAfter([]byte(`{"ok":false}`)); got != 0 {
		t.Errorf("telegramRetryAfter(empty) = %v", got)
	}
}

func TestHelpersAndValidation(t *testing.T) {
	if got := truncateRunes("abcdef", 3); got != "ab…" {
		t.Errorf("truncateRunes = %q", got)
	}
	if got := truncateBytes("你好世界", 5); got != "你" { // 2 runes = 6 bytes > 5
		t.Errorf("truncateBytes split a rune: %q", got)
	}
	if got := slackify("**bold** [link](https://x.test/a)"); got != "*bold* <https://x.test/a|link>" {
		t.Errorf("slackify = %q", got)
	}
	if err := validateURL("ftp://x.test"); err == nil {
		t.Error("non-http scheme must be rejected")
	}
	if err := validateURL(""); err == nil {
		t.Error("empty URL must be rejected")
	}
	if err := validateURL("https://x.test/hook"); err != nil {
		t.Errorf("valid URL rejected: %v", err)
	}
	// A $schema field must survive option expansion untouched; this is why
	// config uses ${VAR} only. Guard the Teams card shape here too.
	m, err := notiftpl.RenderMessage(testNote(), "[FeedWatch]")
	if err != nil {
		t.Fatal(err)
	}
	req, err := Adapters()["teams"].Build(m, Options{Values: map[string]string{"webhook_url": "https://x.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req.Body), "$schema") {
		t.Error("Adaptive Card lost its $schema field")
	}
}
