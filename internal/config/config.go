// Package config loads, defaults and validates the YAML configuration.
// It has no dependency on any other internal package: main translates a Config
// into the Spec values consumed by the pluggable registries.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// Duration is a time.Duration that can be written as "30m" in YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" {
		return nil
	}
	v, err := time.ParseDuration(strings.TrimSpace(n.Value))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }
func (d Duration) String() string   { return time.Duration(d).String() }

type Config struct {
	Poll   PollConfig   `yaml:"poll"`
	HTTP   HTTPConfig   `yaml:"http"`
	Store  StoreConfig  `yaml:"store"`
	Notify NotifyConfig `yaml:"notify"`
	Log    LogConfig    `yaml:"log"`
	Feeds  []FeedConfig `yaml:"feeds"`
}

type PollConfig struct {
	Interval   Duration `yaml:"interval"`
	RunOnStart bool     `yaml:"run_on_start"`
	Timeout    Duration `yaml:"timeout"`
	// Concurrency caps simultaneous feed fetches; RequestSpacing paces the
	// start of each request. Both exist so a config with many feeds (often on
	// the same host, e.g. github.com) cannot burst the upstream.
	Concurrency    int      `yaml:"concurrency"`
	RequestSpacing Duration `yaml:"request_spacing"`
}

type HTTPConfig struct {
	Timeout        Duration `yaml:"timeout"`
	UserAgent      string   `yaml:"user_agent"`
	RetryMax       int      `yaml:"retry_max"`
	RetryBaseDelay Duration `yaml:"retry_base_delay"`
	Conditional    bool     `yaml:"conditional"`
}

type StoreConfig struct {
	Type    string            `yaml:"type"`
	Path    string            `yaml:"path"`
	Options map[string]string `yaml:"options"`
}

// NotifyConfig holds only settings that apply to every channel. Everything
// channel-specific lives inside its channels[] entry: webhook channels use
// options, email channels use their own email block.
// Mode is deliberately first: it is a global watcher setting (how many
// notifications one cycle produces), not a per-channel option. Keeping it
// here mirrors the top of config.example.yaml.
type NotifyConfig struct {
	Mode string `yaml:"mode"` // global, channel-independent
	// SendSpacing paces every channel delivery across all batches. It matters
	// most for per_feed mode, where one cycle can produce many notifications.
	SendSpacing      Duration          `yaml:"send_spacing"`
	SubjectPrefix    string            `yaml:"subject_prefix"`
	Options          map[string]string `yaml:"options"`
	Channels         []ChannelConfig   `yaml:"channels"`
	OnPartialFailure string            `yaml:"on_partial_failure"`
}

// ChannelConfig describes one delivery channel. Options are passed straight
// to the implementation; ${ENV_VAR} references in values are expanded by the
// composition root so webhook URLs / tokens / secrets never live in YAML.
// Email is read only when Type == "email"; it keeps the recipient list typed
// instead of flattening it into a string option.
type ChannelConfig struct {
	Type    string            `yaml:"type"`
	Name    string            `yaml:"name"`
	Enabled bool              `yaml:"enabled"`
	Options map[string]string `yaml:"options"`
	Email   EmailConfig       `yaml:"email"`
}

// UnmarshalYAML makes enabled default to true for list elements.
func (c *ChannelConfig) UnmarshalYAML(n *yaml.Node) error {
	type raw ChannelConfig
	v := raw{Enabled: true}
	if err := n.Decode(&v); err != nil {
		return err
	}
	*c = ChannelConfig(v)
	return nil
}

// UsesEmail reports whether the Resend notifier is among the enabled channels.
func (c NotifyConfig) UsesEmail() bool {
	for _, ch := range c.Channels {
		if ch.Enabled && ch.Type == "email" {
			return true
		}
	}
	return false
}

// EmailConfig belongs to one email channel, not to notify itself. APIKeyEnv
// names the environment variable holding the Resend key; the key value itself
// is never read from YAML.
type EmailConfig struct {
	Provider  string   `yaml:"provider"`
	From      string   `yaml:"from"`
	To        []string `yaml:"to"`
	Cc        []string `yaml:"cc"`
	APIKeyEnv string   `yaml:"api_key_env"`
}

const (
	DefaultSubjectPrefix = "[FeedWatch]"
	DefaultAPIKeyEnv     = "RESEND_API_KEY"
	// Resend accepts at most 50 addresses in a single "to" field. All
	// recipients are sent in ONE API call, so exceeding this would otherwise
	// fail on every poll cycle with an opaque 400.
	MaxEmailRecipients = 50
)

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type FeedConfig struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"`
	URL     string            `yaml:"url"`
	Enabled bool              `yaml:"enabled"`
	Filter  FilterConfig      `yaml:"filter"`
	Options map[string]string `yaml:"options"`
}

// UnmarshalYAML gives feed-level defaults that the decoder cannot infer for
// slice elements: enabled=true and stable_only=true when the keys are omitted.
// The *yaml.Node type must come from the same module as Unmarshal: go-yaml v4
// rejects v3 node hooks with a migration error.
func (f *FeedConfig) UnmarshalYAML(n *yaml.Node) error {
	type raw FeedConfig
	v := raw{Enabled: true, Filter: FilterConfig{StableOnly: true}}
	if err := n.Decode(&v); err != nil {
		return err
	}
	*f = FeedConfig(v)
	return nil
}

type FilterConfig struct {
	StableOnly     bool     `yaml:"stable_only"`
	Include        []string `yaml:"include"`
	Exclude        []string `yaml:"exclude"`
	VersionPattern string   `yaml:"version_pattern"`
}

// Overrides are applied on top of the YAML file: flag > env > yaml > default.
type Overrides struct {
	StatePath    string
	LogLevel     string
	LogFormat    string
	PollInterval string
	DryRun       bool
}

// Default returns a fully usable configuration before any YAML is read.
func Default() Config {
	return Config{
		Poll: PollConfig{
			Interval:       Duration(30 * time.Minute),
			RunOnStart:     true,
			Timeout:        Duration(2 * time.Minute),
			Concurrency:    4,
			RequestSpacing: Duration(100 * time.Millisecond),
		},
		HTTP: HTTPConfig{
			Timeout:        Duration(15 * time.Second),
			UserAgent:      "feedwatch/1.0 (+https://github.com/zsyo/feedwatch)",
			RetryMax:       3,
			RetryBaseDelay: Duration(500 * time.Millisecond),
			Conditional:    true,
		},
		Store: StoreConfig{Type: "jsonfile", Path: "data/state.json"},
		Notify: NotifyConfig{
			Mode:             "digest",
			SendSpacing:      Duration(250 * time.Millisecond),
			SubjectPrefix:    DefaultSubjectPrefix,
			OnPartialFailure: "best_effort",
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// Load reads path, applies defaults and overrides, then validates.
func Load(path string, ov Overrides) (Config, error) {
	c := Default()
	b, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	if ov.PollInterval != "" {
		d, err := time.ParseDuration(ov.PollInterval)
		if err != nil {
			return Config{}, fmt.Errorf("invalid poll interval %q: %w", ov.PollInterval, err)
		}
		c.Poll.Interval = Duration(d)
	}
	if ov.StatePath != "" {
		c.Store.Path = ov.StatePath
	}
	if ov.LogLevel != "" {
		c.Log.Level = ov.LogLevel
	}
	if ov.LogFormat != "" {
		c.Log.Format = ov.LogFormat
	}
	if err := c.Validate(ov.DryRun); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return c, nil
}

// Validate performs all cheap, fail-fast checks. Regex compilation lives in
// filter.New and unknown registry types are reported while building.
func (c Config) Validate(dryRun bool) error {
	if c.Poll.Interval.D() <= 0 {
		return fmt.Errorf("poll.interval must be positive")
	}
	if c.Poll.Timeout.D() <= 0 {
		return fmt.Errorf("poll.timeout must be positive")
	}
	if c.HTTP.Timeout.D() <= 0 {
		return fmt.Errorf("http.timeout must be positive")
	}
	if c.HTTP.RetryMax < 0 {
		return fmt.Errorf("http.retry_max must not be negative")
	}
	if c.Store.Type == "" || c.Store.Path == "" {
		return fmt.Errorf("store.type and store.path are required")
	}
	if c.Poll.Concurrency < 1 {
		return fmt.Errorf("poll.concurrency must be at least 1, got %d", c.Poll.Concurrency)
	}
	if c.Poll.RequestSpacing.D() < 0 {
		return fmt.Errorf("poll.request_spacing must not be negative")
	}
	if c.Notify.SendSpacing.D() < 0 {
		return fmt.Errorf("notify.send_spacing must not be negative")
	}
	if c.Notify.Mode != "digest" && c.Notify.Mode != "per_feed" {
		return fmt.Errorf("notify.mode must be digest or per_feed, got %q", c.Notify.Mode)
	}
	if p := c.Notify.OnPartialFailure; p != "" && p != "best_effort" && p != "strict" {
		return fmt.Errorf("notify.on_partial_failure must be best_effort or strict, got %q", p)
	}
	channelCount := 0
	for i, ch := range c.Notify.Channels {
		if !ch.Enabled {
			continue
		}
		channelCount++
		if strings.TrimSpace(ch.Type) == "" {
			return fmt.Errorf("notify.channels[%d].type is required", i)
		}
	}
	if channelCount == 0 {
		return fmt.Errorf("notify.channels needs at least one enabled channel (the old notify.type form was removed; see config.example.yaml)")
	}
	for i, ch := range c.Notify.Channels {
		if !ch.Enabled || ch.Type != "email" {
			continue
		}
		e := ch.Email
		if e.Provider != "" && e.Provider != "resend" {
			return fmt.Errorf("notify.channels[%d].email: unsupported email provider %q", i, e.Provider)
		}
		if strings.TrimSpace(e.From) == "" {
			return fmt.Errorf("notify.channels[%d].email.from is required (Resend verified domain)", i)
		}
		if len(e.To) == 0 {
			return fmt.Errorf("notify.channels[%d].email.to must contain at least one recipient", i)
		}
		if len(e.To) > MaxEmailRecipients {
			return fmt.Errorf("notify.channels[%d].email.to has %d recipients, Resend allows at most %d per email (split into several email channels)",
				i, len(e.To), MaxEmailRecipients)
		}
		keyEnv := e.APIKeyEnv
		if keyEnv == "" {
			keyEnv = DefaultAPIKeyEnv
		}
		if !dryRun && os.Getenv(keyEnv) == "" {
			return fmt.Errorf("environment variable %s is not set (set it, or use -dry-run / a channels entry with type: log)", keyEnv)
		}
	}

	enabled := 0
	names := make(map[string]struct{})
	for _, f := range c.Feeds {
		if !f.Enabled {
			continue
		}
		enabled++
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("feed.name is required")
		}
		if _, dup := names[f.Name]; dup {
			return fmt.Errorf("duplicate feed name %q (names are used as state keys)", f.Name)
		}
		names[f.Name] = struct{}{}
		if strings.TrimSpace(f.URL) == "" {
			return fmt.Errorf("feed %q: url is required", f.Name)
		}
		if !strings.HasPrefix(f.URL, "http://") && !strings.HasPrefix(f.URL, "https://") {
			return fmt.Errorf("feed %q: url must start with http:// or https://", f.Name)
		}
		if f.Type == "" {
			return fmt.Errorf("feed %q: type is required", f.Name)
		}
	}
	if enabled == 0 {
		return fmt.Errorf("at least one enabled feed is required")
	}
	return nil
}
