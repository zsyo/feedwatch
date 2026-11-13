//go:build webui

package webui

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"feedwatch/internal/config"
	"go.yaml.in/yaml/v4"
)

// ChannelSpec is the JSON shape stored in channels.config_json. It deliberately
// contains no plaintext credentials: secrets are ${ENV_VAR} references (expanded
// in memory by config.ExpandEnvRefs at send time) or the NAME of an env var.
type ChannelSpec struct {
	Type          string            `json:"type"`
	Name          string            `json:"name,omitempty"`
	From          string            `json:"from,omitempty"`
	To            []string          `json:"to,omitempty"`
	Cc            []string          `json:"cc,omitempty"`
	APIKeyEnv     string            `json:"api_key_env,omitempty"`
	SubjectPrefix string            `json:"subject_prefix,omitempty"`
	Options       map[string]string `json:"options,omitempty"`
}

var envRefPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

func looksSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, part := range []string{"key", "token", "secret", "password", "passwd", "sign"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// sanitizeOptions keeps placeholders verbatim but refuses to persist a literal
// secret in SQLite. The caller can still fix it later through the UI using a
// ${ENV_VAR} reference.
func sanitizeOptions(in map[string]string, warn func(key string)) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v == "" || envRefPattern.MatchString(v) || !looksSecretKey(k) {
			out[k] = v
			continue
		}
		if warn != nil {
			warn(k)
		}
		out[k] = ""
	}
	return out
}

// LoadSeedConfig reads config.yaml loosely for global defaults and the initial
// feeds/channels. It intentionally does NOT call config.Load: WebUI takes its
// data from SQLite, so Validate's "at least one feed/channel" and API-key
// checks would wrongly reject a perfectly valid empty database. A missing file
// is also fine — it just means there is nothing to seed.
func LoadSeedConfig(path string) (config.Config, error) {
	cfg := config.Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("webui: read seed config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("webui: parse seed config %q: %w", path, err)
	}
	return cfg, nil
}
