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

type NotifyConfig struct {
	Type    string            `yaml:"type"`
	Mode    string            `yaml:"mode"`
	Email   EmailConfig       `yaml:"email"`
	Options map[string]string `yaml:"options"`
}

type EmailConfig struct {
	Provider      string   `yaml:"provider"`
	From          string   `yaml:"from"`
	To            []string `yaml:"to"`
	Cc            []string `yaml:"cc"`
	SubjectPrefix string   `yaml:"subject_prefix"`
	APIKeyEnv     string   `yaml:"api_key_env"`
}

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
			Interval:   Duration(30 * time.Minute),
			RunOnStart: true,
			Timeout:    Duration(2 * time.Minute),
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
			Type: "email",
			Mode: "digest",
			Email: EmailConfig{
				Provider:      "resend",
				SubjectPrefix: "[FeedWatch]",
				APIKeyEnv:     "RESEND_API_KEY",
			},
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
	if c.Notify.Type == "" {
		return fmt.Errorf("notify.type is required")
	}
	if c.Notify.Mode != "digest" && c.Notify.Mode != "per_feed" {
		return fmt.Errorf("notify.mode must be digest or per_feed, got %q", c.Notify.Mode)
	}
	if c.Notify.Type == "email" {
		e := c.Notify.Email
		if e.Provider != "" && e.Provider != "resend" {
			return fmt.Errorf("unsupported email provider %q", e.Provider)
		}
		if strings.TrimSpace(e.From) == "" {
			return fmt.Errorf("notify.email.from is required (Resend verified domain)")
		}
		if len(e.To) == 0 {
			return fmt.Errorf("notify.email.to must contain at least one recipient")
		}
		if e.APIKeyEnv == "" {
			return fmt.Errorf("notify.email.api_key_env is required")
		}
		if !dryRun && os.Getenv(e.APIKeyEnv) == "" {
			return fmt.Errorf("environment variable %s is not set (set it, or use -dry-run / notify.type: log)", e.APIKeyEnv)
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
