// Package github implements source.Source for GitHub Releases. It covers
// repositories that do not publish a releases.atom feed: the REST API exposes
// the same information with authoritative prerelease/draft flags, plus ETag
// and conditional requests for cheap polling.
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
	githubHosts = []string{"https://github.com/", "http://github.com/", "github.com/"}
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
	for _, prefix := range githubHosts {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			s = s[len(prefix):]
			break
		}
	}
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
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
