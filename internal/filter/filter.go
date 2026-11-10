// Package filter decides which feed entries count as stable releases and
// normalises their version strings. Rules are configured per feed.
package filter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"feedwatch/internal/domain"
)

type Rules struct {
	StableOnly     bool
	Include        []string
	Exclude        []string
	VersionPattern string
}

// GitHub release titles are human-readable, not bare version strings, e.g.
//
//	"[release-branch.go1.27] go1.27.3"  (golang/go)
//	"Rust 1.99.1"                       (rust-lang/rust)
//	"v3.15.2"                           (python/cpython)
//
// So instead of anchoring the whole title we extract a 3~4 segment numeric
// core and inspect what follows it. Requiring 3 segments also drops non-version
// tags such as the CPython "Python 3.10 end of life" entry.
const defaultVersionPattern = `(\d+\.\d+\.\d+(?:\.\d+)?)`

var defaultVersionRe = regexp.MustCompile(defaultVersionPattern)

var defaultExcludePatterns = []string{
	`(?i)[-._]?(rc|alpha|beta|pre|dev|nightly|snapshot)\d*$`,
	`(?i)[ab]\d+$`,
}

var defaultExcludeRe = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(defaultExcludePatterns))
	for _, p := range defaultExcludePatterns {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}()

var (
	fallbackCoreRe   = regexp.MustCompile(`\d+(?:\.\d+)+`)
	harmlessSuffixRe = regexp.MustCompile(`^[\s\])}>,;]*$`)
	trimTokenCutset  = "[](){}<>,;\"'"
)

type Filter interface {
	Apply(rs []domain.Release) []domain.Release
	Keep(r domain.Release) (domain.Release, bool)
}

type compiled struct {
	stableOnly bool
	versionRe  *regexp.Regexp
	include    []*regexp.Regexp
	exclude    []*regexp.Regexp
}

func compileAll(patterns []string, label string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("filter: %s %q: %w", label, p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// New compiles all rules up front so a bad regex fails during startup rather
// than in the middle of a poll cycle.
func New(r Rules) (Filter, error) {
	pattern := r.VersionPattern
	if pattern == "" {
		pattern = defaultVersionPattern
	}
	versionRe, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("filter: version_pattern %q: %w", pattern, err)
	}
	include, err := compileAll(r.Include, "include")
	if err != nil {
		return nil, err
	}
	// When stable_only is false the user wants every entry, so only explicit
	// custom excludes apply; the built-in prerelease patterns are informational
	// (they still drive Release.IsStable) rather than filtering.
	var excludePatterns []string
	if r.StableOnly {
		excludePatterns = append(excludePatterns, defaultExcludePatterns...)
	}
	excludePatterns = append(excludePatterns, r.Exclude...)
	exclude, err := compileAll(excludePatterns, "exclude")
	if err != nil {
		return nil, err
	}
	return &compiled{
		stableOnly: r.StableOnly,
		versionRe:  versionRe,
		include:    include,
		exclude:    exclude,
	}, nil
}

// extractCore finds the last numeric version triple in a title and returns the
// core plus the match boundaries. A non-empty meaningful suffix (rc1, a1,
// -beta.1, ...) marks a prerelease.
func extractCore(title string) (core string, start, end int, ok bool) {
	matches := defaultVersionRe.FindAllStringSubmatchIndex(title, -1)
	if len(matches) == 0 {
		return "", 0, 0, false
	}
	m := matches[len(matches)-1]
	core = title[m[2]:m[3]]
	if !harmlessSuffixRe.MatchString(title[m[1]:]) {
		return core, m[0], m[1], false
	}
	return core, m[0], m[1], true
}

// looksStable is metadata only; it is independent of the configured rules so
// a notifier can still label prereleases when stable_only is false.
func looksStable(title string) bool {
	_, _, _, ok := extractCore(title)
	if !ok {
		return false
	}
	for _, re := range defaultExcludeRe {
		if re.MatchString(title) {
			return false
		}
	}
	return true
}

// displayVersion returns the whitespace-delimited token containing the version
// match, e.g. "go1.27.3" from "[release-branch.go1.27] go1.27.3", or "1.99.1"
// from "Rust 1.99.1". This is what appears in the email table.
func displayVersion(title string, matchStart, matchEnd int) string {
	start := matchStart
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(title[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	end := matchEnd
	for end < len(title) {
		r, size := utf8.DecodeRuneInString(title[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += size
	}
	return strings.Trim(title[start:end], trimTokenCutset)
}

func (f *compiled) Keep(r domain.Release) (domain.Release, bool) {
	title := strings.TrimSpace(r.Title)
	if title == "" {
		return r, false
	}
	r.IsStable = looksStable(title)

	matches := f.versionRe.FindAllStringSubmatchIndex(title, -1)
	if f.stableOnly && len(matches) == 0 {
		return r, false
	}
	if len(matches) > 0 {
		m := matches[len(matches)-1]
		if f.stableOnly && !harmlessSuffixRe.MatchString(title[m[1]:]) {
			return r, false
		}
		if len(m) >= 4 {
			r.CoreVersion = title[m[2]:m[3]]
		} else {
			r.CoreVersion = title[m[0]:m[1]]
		}
		if display := displayVersion(title, m[0], m[1]); display != "" {
			r.Version = display
		}
	}
	if r.CoreVersion == "" {
		r.CoreVersion = fallbackCoreRe.FindString(title)
	}
	if r.Version == "" {
		r.Version = title
	}

	for _, re := range f.exclude {
		if re.MatchString(title) {
			return r, false
		}
	}
	if len(f.include) > 0 {
		matched := false
		for _, re := range f.include {
			if re.MatchString(title) {
				matched = true
				break
			}
		}
		if !matched {
			return r, false
		}
	}
	return r, true
}

func (f *compiled) Apply(rs []domain.Release) []domain.Release {
	out := make([]domain.Release, 0, len(rs))
	for _, r := range rs {
		if kept, ok := f.Keep(r); ok {
			out = append(out, kept)
		}
	}
	return out
}

// CompareVersion compares two dotted numeric cores (1.9.0 < 1.10.0).
// Invalid cores compare equal, letting the caller fall back to timestamps.
func CompareVersion(a, b string) int {
	as, okA := versionSegments(a)
	bs, okB := versionSegments(b)
	if !okA || !okB {
		return 0
	}
	n := max(len(bs), len(as))
	for i := range n {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionSegments(core string) ([]int, bool) {
	if core == "" {
		return nil, false
	}
	parts := strings.Split(core, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
