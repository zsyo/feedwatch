//go:build webui

package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"

	githubsource "feedwatch/internal/source/github"
)

// NormalizeURL produces the canonical form used as the poll-pool identity.
// scheme/host are lower-cased, default ports and fragments removed, and query
// keys sorted so two users subscribing to the same feed with reordered query
// parameters still share one fetch. The query itself is preserved because
// webhook credentials legitimately live there.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("webui: parse URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("webui: subscription URL must be absolute: %q", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	switch {
	case port == "":
		u.Host = host
	case (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443"):
		u.Host = host
	default:
		u.Host = net.JoinHostPort(host, port)
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = u.Query().Encode() // url.Values.Encode sorts by key
	u.ForceQuery = false
	return u.String(), nil
}

// URLKey returns the normalized URL plus its stable sha256 identity.
func URLKey(raw string) (string, string, error) {
	normalized, err := NormalizeURL(raw)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(normalized))
	return normalized, hex.EncodeToString(sum[:]), nil
}

// RepoURLKey does the same for GitHub Releases subscriptions, which identify
// a repository as "owner/repo" instead of an absolute URL. The stored url is
// prefixed with "github:" so it can never collide with an atom URL in the
// shared feed_pool, while the source receives the bare "owner/repo".
func RepoURLKey(raw string) (string, string, error) {
	repo, err := githubsource.NormalizeRepo(raw)
	if err != nil {
		return "", "", err
	}
	stored := "github:" + repo
	sum := sha256.Sum256([]byte(stored))
	return stored, hex.EncodeToString(sum[:]), nil
}
