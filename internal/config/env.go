package config

import (
	"os"
	"regexp"
	"sort"
)

// envRef matches only ${NAME} (upper-case env-var style). Deliberately NOT
// os.Expand: Teams Adaptive Cards contain a literal "$schema" field, which a
// bare $NAME rule would silently eat.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnvRefs resolves ${ENV_VAR} placeholders in option values. It returns
// the expanded map and the sorted list of variables that were referenced but
// unset/empty, so the composition root can fail fast with an actionable error
// instead of sending a request to a half-empty URL.
//
// Only values are expanded, never keys: header names and option names stay
// literal.
func ExpandEnvRefs(values map[string]string) (map[string]string, []string) {
	out := make(map[string]string, len(values))
	missing := make(map[string]struct{})
	for k, v := range values {
		out[k] = envRef.ReplaceAllStringFunc(v, func(ref string) string {
			name := ref[2 : len(ref)-1]
			val := os.Getenv(name)
			if val == "" {
				missing[name] = struct{}{}
			}
			return val
		})
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names
}
