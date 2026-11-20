// Package github implements source.Source for GitHub Releases. It reads a
// repository's releases through the REST API, where GitHub's authoritative
// prerelease/draft flags decide version scope and paging reaches further back
// than the ~10 entries a releases.atom feed keeps. ETag and conditional
// requests keep polling cheap.
package github

import (
	"fmt"
	"regexp"
	"strings"
)

// SourceType is the registry key used in config.yaml and the WebUI.
const SourceType = "github"

var (
	repoPattern = regexp.MustCompile(`^[a-z0-9_.-]+/[a-z0-9_.-]+$`)
	// StoredPrefix is how the WebUI keeps GitHub subscriptions in the shared
	// feed_pool without colliding with an atom URL. NormalizeRepo accepts it so
	// stored values round-trip through validation unchanged.
	StoredPrefix = "github:"
	githubHosts  = []string{"https://github.com/", "http://github.com/", "github.com/", StoredPrefix}
)

// NormalizeRepo accepts "owner/repo" and full GitHub URLs such as
// "https://github.com/owner/repo" or "github.com/owner/repo.git", and returns
// a lower-cased "owner/repo". GitHub repository names are case-insensitive, so
// normalising here keeps url_key/seen-id identity stable across spelling.
func NormalizeRepo(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("github: repository is required (expected owner/repo)")
	}
	matchedPrefix := ""
	for _, prefix := range githubHosts {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			matchedPrefix = prefix
			s = s[len(prefix):]
			break
		}
	}
	// Only strip slashes when a known prefix was present. Otherwise a relative
	// URL like "/relative/feed.atom" would trim into a valid-looking "repo/name"
	// and slip past the catalog's URL safety checks.
	if matchedPrefix != "" {
		s = strings.Trim(s, "/")
		s = strings.TrimSuffix(s, ".git")
	}
	s = strings.ToLower(s)
	if !repoPattern.MatchString(s) {
		return "", fmt.Errorf("github: invalid repository %q (expected owner/repo)", strings.TrimSpace(raw))
	}
	return s, nil
}

// Repo splits a normalised "owner/repo" into its two parts.
func Repo(normalized string) (owner, name string) {
	parts := strings.SplitN(normalized, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// IsValidSourceType reports whether typ is a source type this package handles.
func IsValidSourceType(typ string) bool {
	return strings.EqualFold(strings.TrimSpace(typ), SourceType)
}
