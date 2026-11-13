//go:build webui

package webui

// SanitizeOptions exposes the seed-time sanitizer to the HTTP API so both write
// paths enforce the same "no plaintext secrets in SQLite" rule.
func SanitizeOptions(in map[string]string, warn func(string)) map[string]string {
	return sanitizeOptions(in, warn)
}

// IsEnvRef reports whether a value is (or contains) a ${ENV_VAR} reference.
func IsEnvRef(value string) bool { return envRefPattern.MatchString(value) }

// LooksLikeSecret reports whether an option name suggests a credential.
func LooksLikeSecret(key string) bool { return looksSecretKey(key) }
