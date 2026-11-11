package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"feedwatch/internal/config"
	"feedwatch/internal/notifier"
	emailnotify "feedwatch/internal/notifier/email"
	lognotify "feedwatch/internal/notifier/log"
	webhooknotify "feedwatch/internal/notifier/webhook"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testRegistry() *notifier.Registry {
	r := notifier.NewRegistry()
	r.Register("email", emailnotify.New)
	r.Register("log", lognotify.New)
	for typ, a := range webhooknotify.Adapters() {
		r.Register(typ, webhooknotify.Factory(a))
	}
	return r
}

func baseConfig() config.Config {
	c := config.Default()
	c.Notify.Type = "log"
	return c
}

func TestChannelSpecsLegacyFormIsBackwardCompatible(t *testing.T) {
	t.Setenv("FW_TOKEN", "https://hooks.example.com/abc")
	cfg := baseConfig()
	cfg.Notify.Type = "webhook"
	cfg.Notify.Options = map[string]string{"webhook_url": "${FW_TOKEN}"}
	specs, err := channelSpecs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Type != "webhook" {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].Options["webhook_url"] != "https://hooks.example.com/abc" {
		t.Errorf("env not expanded: %v", specs[0].Options)
	}
}

func TestChannelSpecsMergesGlobalOptionsAndHonorsEnabled(t *testing.T) {
	t.Setenv("FW_WECOM", "https://qyapi.weixin.qq.com/hook")
	cfg := baseConfig()
	cfg.Notify.Options = map[string]string{"retry_max": "4", "timeout": "7s"}
	cfg.Notify.Channels = []config.ChannelConfig{
		{Type: "wecom", Name: "团队群", Enabled: true, Options: map[string]string{
			"webhook_url": "${FW_WECOM}", "retry_max": "1",
		}},
		{Type: "slack", Enabled: false, Options: map[string]string{"webhook_url": "${FW_NEVER_SET}"}},
	}
	specs, err := channelSpecs(cfg)
	if err != nil {
		t.Fatalf("disabled channel must not require env: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	got := specs[0]
	if got.Name != "团队群" || got.Type != "wecom" {
		t.Errorf("spec = %+v", got)
	}
	if got.Options["retry_max"] != "1" || got.Options["timeout"] != "7s" {
		t.Errorf("option merge wrong: %v", got.Options)
	}
	if got.Options["webhook_url"] != "https://qyapi.weixin.qq.com/hook" {
		t.Errorf("env not expanded: %v", got.Options)
	}
}

func TestChannelSpecsFailsFastOnMissingEnv(t *testing.T) {
	cfg := baseConfig()
	cfg.Notify.Channels = []config.ChannelConfig{{
		Type: "wecom", Enabled: true, Name: "团队群",
		Options: map[string]string{"webhook_url": "${FW_NOT_SET_123}"},
	}}
	_, err := channelSpecs(cfg)
	if err == nil || !strings.Contains(err.Error(), "FW_NOT_SET_123") || !strings.Contains(err.Error(), "团队群") {
		t.Fatalf("want actionable missing-env error, got %v", err)
	}
}

func TestBuildNotifierDryRunReplacesWholeFanOut(t *testing.T) {
	cfg := baseConfig()
	cfg.Notify.Channels = []config.ChannelConfig{
		{Type: "wecom", Enabled: true, Options: map[string]string{"webhook_url": "${FW_NOT_SET_123}"}},
		{Type: "telegram", Enabled: true, Options: map[string]string{"chat_id": "@x"}},
	}
	n, err := buildNotifier(cfg, testRegistry(), true, quietLog())
	if err != nil {
		t.Fatalf("dry-run must ignore missing secrets: %v", err)
	}
	if n.Type() != "log" {
		t.Errorf("dry-run notifier = %q, want log", n.Type())
	}
}

func TestBuildNotifierFanOutAndUnknownType(t *testing.T) {
	cfg := baseConfig()
	cfg.Notify.Channels = []config.ChannelConfig{
		{Type: "wecom", Enabled: true, Options: map[string]string{"webhook_url": "https://x.test/a"}},
		{Type: "slack", Enabled: true, Options: map[string]string{"webhook_url": "https://x.test/b"}},
	}
	n, err := buildNotifier(cfg, testRegistry(), false, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := n.(*notifier.Multi)
	if !ok || len(m.Channels()) != 2 {
		t.Fatalf("expected a 2-channel Multi, got %T", n)
	}

	cfg.Notify.Channels[1].Type = "does_not_exist"
	if _, err := buildNotifier(cfg, testRegistry(), false, quietLog()); err == nil {
		t.Error("unknown channel type must fail at startup")
	}
}

func TestNotifyLabel(t *testing.T) {
	cfg := baseConfig()
	if got := notifyLabel(cfg, false); got != "log" {
		t.Errorf("legacy label = %q", got)
	}
	cfg.Notify.Channels = []config.ChannelConfig{
		{Type: "wecom", Enabled: true}, {Type: "slack", Enabled: false},
	}
	if got := notifyLabel(cfg, false); got != "wecom" {
		t.Errorf("fan-out label = %q", got)
	}
	if got := notifyLabel(cfg, true); got != "log (dry-run)" {
		t.Errorf("dry-run label = %q", got)
	}
}
