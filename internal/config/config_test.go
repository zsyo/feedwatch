package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalYAML = `
notify:
  channels:
    - type: log
feeds:
  - name: golang/go
    type: atom
    url: https://github.com/golang/go/releases.atom
`

const validFeeds = "feeds:\n  - name: x\n    type: atom\n    url: https://example.com/a.atom\n"

func TestLoadAppliesDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, minimalYAML), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Poll.Interval.D() != 30*time.Minute {
		t.Errorf("interval = %v, want 30m", c.Poll.Interval.D())
	}
	if !c.Poll.RunOnStart || c.Poll.Timeout.D() != 2*time.Minute {
		t.Errorf("unexpected poll defaults: %+v", c.Poll)
	}
	if c.HTTP.Timeout.D() != 15*time.Second || c.HTTP.RetryMax != 3 || !c.HTTP.Conditional {
		t.Errorf("unexpected http defaults: %+v", c.HTTP)
	}
	if c.Store.Type != "jsonfile" || c.Store.Path != "data/state.json" {
		t.Errorf("unexpected store defaults: %+v", c.Store)
	}
	if c.Poll.Concurrency != 4 || c.Poll.RequestSpacing.D() != 100*time.Millisecond {
		t.Errorf("unexpected fetch rate-limit defaults: %+v", c.Poll)
	}
	if c.Notify.Mode != "digest" || c.Notify.SubjectPrefix != "[FeedWatch]" ||
		c.Notify.SendSpacing.D() != 250*time.Millisecond {
		t.Errorf("unexpected notify defaults: %+v", c.Notify)
	}
	if len(c.Feeds) != 1 || !c.Feeds[0].Enabled {
		t.Errorf("feed enabled should default to true: %+v", c.Feeds)
	}
	if !c.Feeds[0].Filter.StableOnly {
		t.Errorf("filter.stable_only should default to true")
	}
}

func TestFeedLevelDefaultsSurviveNestedFilter(t *testing.T) {
	body := `
notify:
  channels:
    - type: log
feeds:
  - name: python/cpython
    type: atom
    url: https://github.com/python/cpython/releases.atom
    filter:
      exclude: ['(?i)dev']
`
	c, err := Load(writeConfig(t, body), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f := c.Feeds[0]
	if !f.Enabled || !f.Filter.StableOnly {
		t.Errorf("defaults lost: %+v", f)
	}
	if len(f.Filter.Exclude) != 1 {
		t.Errorf("exclude lost: %+v", f.Filter)
	}
}

func TestOverridesWinOverYAML(t *testing.T) {
	body := `
poll:
  interval: 5m
store:
  type: jsonfile
  path: original.json
log:
  level: debug
  format: text
notify:
  channels:
    - type: log
feeds:
  - name: rust-lang/rust
    type: atom
    url: https://github.com/rust-lang/rust/releases.atom
`
	c, err := Load(writeConfig(t, body), Overrides{
		PollInterval: "1h",
		StatePath:    "/app/data/state.json",
		LogLevel:     "warn",
		LogFormat:    "json",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Poll.Interval.D() != time.Hour {
		t.Errorf("interval = %v, want 1h", c.Poll.Interval.D())
	}
	if c.Store.Path != "/app/data/state.json" || c.Log.Level != "warn" || c.Log.Format != "json" {
		t.Errorf("overrides not applied: %+v / %+v", c.Store, c.Log)
	}
}

func TestInvalidDurationFailsFast(t *testing.T) {
	_, err := Load(writeConfig(t, "poll:\n  interval: 30x\n"+minimalYAML), Overrides{})
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("expected duration error, got %v", err)
	}
	_, err = Load(writeConfig(t, minimalYAML), Overrides{PollInterval: "nonsense"})
	if err == nil || !strings.Contains(err.Error(), "invalid poll interval") {
		t.Fatalf("expected override duration error, got %v", err)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no feeds", "notify:\n  channels:\n    - type: log\n", "at least one enabled feed"},
		{"duplicate name", minimalYAML + "  - name: golang/go\n    type: atom\n    url: https://example.com/a.atom\n", "duplicate feed name"},
		{"bad url", "notify:\n  channels:\n    - type: log\nfeeds:\n  - name: x\n    type: atom\n    url: ftp://x\n", "must start with http"},
		{"bad mode", "notify:\n  mode: hourly\n  channels:\n    - type: log\n" + validFeeds, "must be digest or per_feed"},
		{"bad concurrency", "poll:\n  concurrency: 0\n" + minimalYAML, "poll.concurrency"},
		{"negative request spacing", "poll:\n  request_spacing: -1s\n" + minimalYAML, "poll.request_spacing"},
		{"negative send spacing", "notify:\n  send_spacing: -1s\n  channels:\n    - type: log\n" + validFeeds, "notify.send_spacing"},
		{"no channels", "notify:\n  mode: digest\n" + validFeeds, "notify.channels needs at least one enabled channel"},
		{"removed type form", "notify:\n  type: email\n" + validFeeds, "old notify.type form was removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body), Overrides{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestEmailRequiresAPIKeyUnlessDryRun(t *testing.T) {
	body := `
notify:
  channels:
    - type: email
      email:
        from: "FeedWatch <noreply@example.com>"
        to: [you@example.com]
` + validFeeds
	t.Setenv("RESEND_API_KEY", "")
	p := writeConfig(t, body)
	if _, err := Load(p, Overrides{}); err == nil || !strings.Contains(err.Error(), "RESEND_API_KEY") {
		t.Fatalf("expected missing API key error, got %v", err)
	}
	if _, err := Load(p, Overrides{DryRun: true}); err != nil {
		t.Fatalf("dry-run should bypass the API key check: %v", err)
	}
	t.Setenv("RESEND_API_KEY", "re_test")
	if _, err := Load(p, Overrides{}); err != nil {
		t.Fatalf("expected success with API key set: %v", err)
	}
}

func TestRateLimitOverridesAreRead(t *testing.T) {
	body := `
poll:
  concurrency: 8
  request_spacing: 500ms
notify:
  send_spacing: 1s
  channels:
    - type: log
` + validFeeds
	c, err := Load(writeConfig(t, body), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Poll.Concurrency != 8 || c.Poll.RequestSpacing.D() != 500*time.Millisecond ||
		c.Notify.SendSpacing.D() != time.Second {
		t.Errorf("rate-limit overrides lost: poll=%+v notify=%+v", c.Poll, c.Notify)
	}
}

func TestEmailRecipientLimitFailsFast(t *testing.T) {
	to := make([]string, MaxEmailRecipients+1)
	for i := range to {
		to[i] = fmt.Sprintf("u%d@example.com", i)
	}
	body := `
notify:
  channels:
    - type: email
      email:
        from: "FeedWatch <noreply@example.com>"
        to: [` + strings.Join(to, ", ") + `]
` + validFeeds
	_, err := Load(writeConfig(t, body), Overrides{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("want a fail-fast recipient-limit error, got %v", err)
	}
}

func TestChannelsFanOutConfig(t *testing.T) {
	body := `
notify:
  on_partial_failure: strict
  options:
    retry_max: "4"
  channels:
    - type: wecom
      name: 团队群
      options:
        webhook_url: ${FW_WECOM_URL}
    - type: telegram
      enabled: false
      options:
        chat_id: "@unused"
` + validFeeds
	c, err := Load(writeConfig(t, body), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Notify.Channels) != 2 {
		t.Fatalf("channels = %d", len(c.Notify.Channels))
	}
	if !c.Notify.Channels[0].Enabled || c.Notify.Channels[1].Enabled {
		t.Errorf("enabled default/override wrong: %+v", c.Notify.Channels)
	}
	if c.Notify.Channels[0].Name != "团队群" {
		t.Errorf("name lost: %+v", c.Notify.Channels[0])
	}
	if c.Notify.OnPartialFailure != "strict" {
		t.Errorf("on_partial_failure = %q", c.Notify.OnPartialFailure)
	}
	if c.Notify.Options["retry_max"] != "4" {
		t.Errorf("global options lost: %v", c.Notify.Options)
	}
	if c.Notify.UsesEmail() {
		t.Error("UsesEmail should be false without an email channel")
	}
}

func TestChannelValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing type", "notify:\n  channels:\n    - options: {}\n" + validFeeds, "type is required"},
		{"all disabled", "notify:\n  channels:\n    - type: wecom\n      enabled: false\n" + validFeeds, "needs at least one enabled channel"},
		{"bad policy", "notify:\n  on_partial_failure: panic\n  channels:\n    - type: wecom\n" + validFeeds, "on_partial_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body), Overrides{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// Mode is a global watcher setting, not a per-channel option: it is read the
// same way whether one channel or several are configured.
func TestModeIsGlobalAcrossChannelCounts(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"one channel", "notify:\n  mode: per_feed\n  channels:\n    - type: log\n" + validFeeds},
		{"two channels", "notify:\n  mode: per_feed\n  channels:\n    - type: log\n    - type: wecom\n      options:\n        webhook_url: https://example.com/hook\n" + validFeeds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Load(writeConfig(t, tc.body), Overrides{})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Notify.Mode != "per_feed" {
				t.Errorf("mode = %q, want per_feed", c.Notify.Mode)
			}
		})
	}
}

func TestEmailChannelStillRequiresKey(t *testing.T) {
	body := `
notify:
  channels:
    - type: email
      email:
        from: "FeedWatch <noreply@example.com>"
        to: [you@example.com]
    - type: wecom
      options:
        webhook_url: https://example.com/hook
` + validFeeds
	p := writeConfig(t, body)
	t.Setenv("RESEND_API_KEY", "")
	if _, err := Load(p, Overrides{}); err == nil || !strings.Contains(err.Error(), "RESEND_API_KEY") {
		t.Fatalf("email channel must still require the API key, got %v", err)
	}
	if _, err := Load(p, Overrides{DryRun: true}); err != nil {
		t.Fatalf("dry-run should bypass the key check: %v", err)
	}
	t.Setenv("RESEND_API_KEY", "re_test")
	c, err := Load(p, Overrides{})
	if err != nil {
		t.Fatalf("Load with key: %v", err)
	}
	if !c.Notify.UsesEmail() {
		t.Error("UsesEmail should be true for an email channel")
	}
}

// Email settings belong to their channel, so two email channels can have
// different senders/recipients and each one is validated independently.
func TestEmailSettingsArePerChannel(t *testing.T) {
	body := `
notify:
  channels:
    - type: email
      email:
        from: "A <a@example.com>"
        to: [a@example.com]
        api_key_env: FW_KEY_A
    - type: email
      email:
        from: "B <b@example.com>"
        to: [b@example.com]
        api_key_env: FW_KEY_B
` + validFeeds
	c, err := Load(writeConfig(t, body), Overrides{DryRun: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Notify.Channels) != 2 {
		t.Fatalf("channels = %+v", c.Notify.Channels)
	}
	if c.Notify.Channels[0].Email.From != "A <a@example.com>" ||
		c.Notify.Channels[1].Email.To[0] != "b@example.com" ||
		c.Notify.Channels[0].Email.APIKeyEnv == c.Notify.Channels[1].Email.APIKeyEnv {
		t.Errorf("email settings leaked between channels: %+v", c.Notify.Channels)
	}

	bad := strings.Replace(body, `from: "B <b@example.com>"`, `from: ""`, 1)
	_, err = Load(writeConfig(t, bad), Overrides{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "notify.channels[1].email.from") {
		t.Fatalf("want per-channel validation with index, got %v", err)
	}
}

// config.example.yaml is the only tracked config and the Dockerfile bakes it
// in as the image default, so it must always parse and validate standalone.
func TestTrackedExampleConfigIsUsable(t *testing.T) {
	p := filepath.Join("..", "..", "config.example.yaml")
	c, err := Load(p, Overrides{DryRun: true}) // skip the API-key env check
	if err != nil {
		t.Fatalf("config.example.yaml must stay loadable (Docker image default): %v", err)
	}
	if len(c.Feeds) < 3 {
		t.Errorf("expected at least the three default feeds, got %d", len(c.Feeds))
	}
	if !c.Notify.UsesEmail() {
		t.Errorf("template should enable an email channel: %+v", c.Notify.Channels)
	}
	if len(c.Notify.Channels) != 1 || c.Notify.Channels[0].Type != "email" ||
		len(c.Notify.Channels[0].Email.To) == 0 {
		t.Errorf("template should carry its email settings inside the channel: %+v", c.Notify.Channels)
	}
}
