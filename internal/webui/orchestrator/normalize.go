//go:build webui

package orchestrator

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"feedwatch/internal/domain"
	"feedwatch/internal/filter"
	githubsource "feedwatch/internal/source/github"
)

type feedRules struct {
	StableOnly bool `json:"stable_only"`
	// ReleaseScope is the GitHub-specific stable/prerelease/all choice. The
	// source applies it using GitHub's authoritative flags, so it must not be
	// re-filtered by the title heuristic. Kept here so a subscription's
	// filter_json can carry both settings in one column.
	ReleaseScope         string   `json:"release_scope"`
	TrustSourceStability bool     `json:"trust_source_stability"`
	Include              []string `json:"include"`
	Exclude              []string `json:"exclude"`
	VersionPattern       string   `json:"version_pattern"`
}

// releaseScopeFrom decodes the GitHub release_scope from filter_json. An empty
// or absent value means "stable" (the source's own default).
func releaseScopeFrom(raw string) string {
	var r feedRules
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if json.Unmarshal([]byte(raw), &r) != nil {
		return ""
	}
	return r.ReleaseScope
}

// filterFor decodes a subscription's own filter_json. Defaults match the base
// config's feed defaults (stable_only=true). Two users sharing one fetched URL
// can therefore see different release sets.
func filterFor(raw, sourceType string) (filter.Filter, error) {
	r := feedRules{StableOnly: true}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("orchestrator: decode filter_json: %w", err)
		}
	}
	// GitHub already filtered by release_scope using is_prerelease/is_draft.
	// Re-applying the title heuristic would both mislabel and drop releases
	// whose titles are human readable, so trust the source and disable the
	// stable-only rule for those subscriptions.
	isGitHub := githubsource.IsValidSourceType(sourceType)
	trust := isGitHub || r.TrustSourceStability
	return filter.New(filter.Rules{
		StableOnly:           r.StableOnly && !trust,
		TrustSourceStability: trust,
		Include:              r.Include,
		Exclude:              r.Exclude,
		VersionPattern:       r.VersionPattern,
	})
}

// latestStable returns the highest numeric core version in the feed window.
// It is used for feed_pool.last_version and for `always` status summaries.
func latestStable(rs []domain.Release) string {
	best := ""
	bestParts := []int{}
	for _, r := range rs {
		if !r.IsStable {
			continue
		}
		core := r.CoreVersion
		if core == "" {
			core = strings.TrimPrefix(r.Version, "v")
		}
		parts, ok := numericParts(core)
		if !ok {
			continue
		}
		if compareParts(parts, bestParts) > 0 {
			best, bestParts = r.Version, parts
		}
	}
	return best
}

func numericParts(v string) ([]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	if v == "" {
		return nil, false
	}
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

func compareParts(a, b []int) int {
	n := max(len(a), len(b))
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func releaseItem(r domain.Release, displayName string, isNew bool) domain.ReleaseItem {
	if displayName == "" {
		displayName = r.Feed
	}
	return domain.ReleaseItem{
		Feed: displayName, Version: r.Version, URL: r.URL,
		PublishedAt: r.EffectiveTime(), IsNew: isNew,
	}
}
