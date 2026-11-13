package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"feedwatch/internal/config"
	"feedwatch/internal/domain"
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
	// A single channel is just a one-element list.
	c.Notify.Channels = []config.ChannelConfig{{Type: "log", Enabled: true}}
	return c
}

func TestChannelSpecsSingleChannel(t *testing.T) {
	t.Setenv("FW_TOKEN", "https://hooks.example.com/abc")
	cfg := baseConfig()
	cfg.Notify.Channels = []config.ChannelConfig{{
		Type: "webhook", Enabled: true,
		Options: map[string]string{"webhook_url": "${FW_TOKEN}"},
	}}
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

func TestChannelSpecsRejectsNoEnabledChannel(t *testing.T) {
	cfg := baseConfig()
	cfg.Notify.Channels = []config.ChannelConfig{{Type: "log", Enabled: false}}
	if _, err := channelSpecs(cfg); err == nil {
		t.Fatal("expected an error when no channel is enabled")
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

func TestChannelSpecsEmailSettingsComeFromTheirChannel(t *testing.T) {
	t.Setenv("FW_KEY_A", "re_first")
	t.Setenv("FW_KEY_B", "re_second")
	cfg := baseConfig()
	cfg.Notify.SubjectPrefix = "[FW]"
	cfg.Notify.Channels = []config.ChannelConfig{
		{
			Type: "email", Name: "团队", Enabled: true,
			Email: config.EmailConfig{
				From: "A <a@example.com>", To: []string{"a@example.com"},
				Cc: []string{"cc@example.com"}, APIKeyEnv: "FW_KEY_A",
			},
		},
		{
			Type: "email", Name: "个人", Enabled: true,
			Email: config.EmailConfig{
				From: "B <b@example.com>", To: []string{"b@example.com"},
				APIKeyEnv: "FW_KEY_B",
			},
			Options: map[string]string{"subject_prefix": "[Personal]"},
		},
	}
	specs, err := channelSpecs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].From != "A <a@example.com>" || specs[0].To[0] != "a@example.com" ||
		specs[0].Cc[0] != "cc@example.com" || specs[0].APIKey != "re_first" ||
		specs[0].SubjectPrefix != "[FW]" {
		t.Errorf("first email channel = %+v", specs[0])
	}
	if specs[1].From != "B <b@example.com>" || specs[1].APIKey != "re_second" ||
		specs[1].SubjectPrefix != "[Personal]" {
		t.Errorf("second email channel = %+v", specs[1])
	}
}

func TestNotifyLabel(t *testing.T) {
	cfg := baseConfig()
	if got := notifyLabel(cfg, false); got != "log" {
		t.Errorf("single-channel label = %q", got)
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

// stampNotifier records the moment it was sent to, so tests can observe the
// pacing that buildNotifier wraps around real channels.
type stampNotifier struct {
	typ string
	hit func()
}

func (s stampNotifier) Type() string { return s.typ }

func (s stampNotifier) Send(context.Context, domain.Notification) error {
	s.hit()
	return nil
}

// stampRegistry registers each type as a stampNotifier sharing one recorder.
func stampRegistry(types ...string) *notifier.Registry {
	var (
		mu    sync.Mutex
		stamp []time.Time
	)
	reg := notifier.NewRegistry()
	for _, typ := range types {
		reg.Register(typ, func(_ notifier.NotifierSpec, _ *slog.Logger) (notifier.Notifier, error) {
			return stampNotifier{typ: typ, hit: func() {
				mu.Lock()
				defer mu.Unlock()
				stamp = append(stamp, time.Now())
			}}, nil
		})
	}
	return reg
}

// Different channel types are different services with independent quotas, so a
// fan-out across types must not be delayed by the spacing of the previous
// type. This is the base equivalent of the per-type gates used by the webui
// orchestrator.
func TestBuildNotifierDoesNotPaceAcrossChannelTypes(t *testing.T) {
	const spacing = 150 * time.Millisecond
	cfg := baseConfig()
	cfg.Notify.SendSpacing = config.Duration(spacing)
	cfg.Notify.Channels = []config.ChannelConfig{
		{Type: "fake_a", Enabled: true},
		{Type: "fake_b", Enabled: true},
	}
	n, err := buildNotifier(cfg, stampRegistry("fake_a", "fake_b"), false, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := n.Send(context.Background(), domain.Notification{Kind: domain.KindNewReleases}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= spacing {
		t.Fatalf("fan-out across two channel types took %v with spacing %v: "+
			"the types are still sharing one gate", elapsed, spacing)
	}
}

// Channels of the SAME type share one service quota, so the spacing must still
// apply between them even though their types differ from every other gate.
func TestBuildNotifierStillPacesWithinOneChannelType(t *testing.T) {
	const spacing = 150 * time.Millisecond
	cfg := baseConfig()
	cfg.Notify.SendSpacing = config.Duration(spacing)
	cfg.Notify.Channels = []config.ChannelConfig{
		{Type: "fake_a", Name: "群一", Enabled: true},
		{Type: "fake_a", Name: "群二", Enabled: true},
	}
	n, err := buildNotifier(cfg, stampRegistry("fake_a"), false, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := n.Send(context.Background(), domain.Notification{Kind: domain.KindNewReleases}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < spacing {
		t.Fatalf("two channels of the same type were sent %v apart, want >= %v", elapsed, spacing)
	}
}
