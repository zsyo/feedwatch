package github

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"feedwatch/internal/domain"
)

// ReleaseScope selects which releases are turned into domain releases.
type ReleaseScope string

const (
	ScopeStable     ReleaseScope = "stable"
	ScopePrerelease ReleaseScope = "prerelease"
	ScopeAll        ReleaseScope = "all"
)

// ParseScope validates an option value and returns the normalised scope.
// An empty option means "stable" (the same default as the filter package).
func ParseScope(raw string) (ReleaseScope, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ScopeStable, nil
	}
	scope := ReleaseScope(s)
	switch scope {
	case ScopeStable, ScopePrerelease, ScopeAll:
		return scope, nil
	default:
		return "", fmt.Errorf("github: release_scope must be stable, prerelease or all, got %q", raw)
	}
}

// REST shapes. GitHub returns null for name/body/published_at on some drafts,
// so pointers are used for those fields.
type restRelease struct {
	ID          int64      `json:"id"`
	TagName     string     `json:"tag_name"`
	Name        *string    `json:"name"`
	Body        *string    `json:"body"`
	HTMLURL     string     `json:"html_url"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Prerelease  bool       `json:"prerelease"`
	Draft       bool       `json:"draft"`
}

// graphQLResponse is the envelope for a batched aliases query. Each key in
// Repositories is an alias like "r0"; a null value means the repository does
// not exist or the token cannot see it.
type graphQLResponse struct {
	Data map[string]struct {
		Repository *struct {
			Releases struct {
				Nodes []graphQLRelease `json:"nodes"`
			} `json:"releases"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type graphQLRelease struct {
	DatabaseID      *int64     `json:"databaseId"`
	Name            string     `json:"name"`
	TagName         string     `json:"tagName"`
	Description     string     `json:"description"`
	DescriptionHTML string     `json:"descriptionHTML"`
	URL             string     `json:"url"`
	PublishedAt     *time.Time `json:"publishedAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	IsPrerelease    bool       `json:"isPrerelease"`
	IsDraft         bool       `json:"isDraft"`
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	whitespaceRe  = regexp.MustCompile(`\s+`)
	coreRe        = regexp.MustCompile(`\d+(?:\.\d+)+`)
)

const maxSummaryRunes = 200

func stripHTML(s string) string {
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = whitespaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > maxSummaryRunes {
		s = string(runes[:maxSummaryRunes]) + "…"
	}
	return s
}

// contentPolicy mirrors the atom source's option so a single deployment can
// use the same setting for both source types.
type contentPolicy string

const (
	policyNone contentPolicy = "none"
	policyText contentPolicy = "text"
	policyHTML contentPolicy = "html"
)

func parsePolicy(s string) contentPolicy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none":
		return policyNone
	case "html":
		return policyHTML
	default:
		return policyText
	}
}

func fillBody(r *domain.Release, raw string, policy contentPolicy) {
	if raw == "" {
		return
	}
	switch policy {
	case policyHTML:
		r.ContentHTML = raw
		r.Summary = stripHTML(raw)
	case policyText:
		r.Summary = stripHTML(raw)
	case policyNone:
		// deliberately empty
	}
}

// inScope applies the authoritative prerelease/draft flags. Drafts are always
// skipped: they are not published releases and have no stable identity.
func inScope(prerelease bool, scope ReleaseScope) bool {
	switch scope {
	case ScopeStable:
		return !prerelease
	case ScopePrerelease:
		return prerelease
	case ScopeAll:
		return true
	default:
		return false
	}
}

func makeRelease(id, title, version, url, body, feedName string, published, updated time.Time, prerelease bool, policy contentPolicy) domain.Release {
	title = strings.TrimSpace(title)
	version = strings.TrimSpace(version)
	if title == "" {
		title = version
	}
	if published.IsZero() {
		published = updated
	}
	// The tag is the authoritative version; display titles such as "Version 2.0"
	// often carry a shorter number, so derive CoreVersion from the tag first.
	core := coreRe.FindString(version)
	if core == "" {
		core = coreRe.FindString(title)
	}
	r := domain.Release{
		ID:          id,
		Feed:        feedName,
		Title:       title,
		Version:     version,
		CoreVersion: core,
		URL:         strings.TrimSpace(url),
		PublishedAt: published,
		UpdatedAt:   updated,
		IsStable:    !prerelease,
	}
	fillBody(&r, body, policy)
	return r
}

// parseREST maps a GitHub REST releases response. The numeric release id is a
// durable, globally unique identity, so it is preferred over the tag name
// (which can be retagged or renamed).
func parseREST(data []byte, feedName string, scope ReleaseScope, policy contentPolicy, limit int) ([]domain.Release, error) {
	var rows []restRelease
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("github: parse releases response: %w", err)
	}
	out := make([]domain.Release, 0, len(rows))
	for _, row := range rows {
		if row.Draft || !inScope(row.Prerelease, scope) {
			continue
		}
		title := ""
		if row.Name != nil {
			title = *row.Name
		}
		body := ""
		if row.Body != nil {
			body = *row.Body
		}
		pub := time.Time{}
		if row.PublishedAt != nil {
			pub = *row.PublishedAt
		}
		out = append(out, makeRelease(
			fmt.Sprintf("github:%d", row.ID), title, row.TagName, row.HTMLURL, body, feedName,
			pub, row.UpdatedAt, row.Prerelease, policy,
		))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// parseGraphQL maps one alias's nodes from a batched response.
func parseGraphQL(nodes []graphQLRelease, feedName string, scope ReleaseScope, policy contentPolicy, limit int) []domain.Release {
	out := make([]domain.Release, 0, len(nodes))
	for _, node := range nodes {
		if node.IsDraft || !inScope(node.IsPrerelease, scope) {
			continue
		}
		id := fmt.Sprintf("github-gql:%s", node.TagName)
		if node.DatabaseID != nil {
			id = fmt.Sprintf("github:%d", *node.DatabaseID)
		}
		pub := time.Time{}
		if node.PublishedAt != nil {
			pub = *node.PublishedAt
		}
		body := node.DescriptionHTML
		if body == "" {
			body = node.Description
		}
		out = append(out, makeRelease(
			id, node.Name, node.TagName, node.URL, body, feedName,
			pub, node.UpdatedAt, node.IsPrerelease, policy,
		))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
