package filter

import (
	"testing"

	"feedwatch/internal/domain"
)

func rel(title string) domain.Release {
	return domain.Release{Title: title, ID: "id-" + title}
}

func TestDefaultStableOnlyFilter(t *testing.T) {
	f, err := New(Rules{StableOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		title string
		want  bool
	}{
		{"go1.27.3", true},
		{"1.99.1", true},
		{"v3.15.2", true},
		{"go1.27.0", true},
		{"go1.26.10", true},
		{"v3.14.10", true},
		{"go1.27rc3", false},
		{"v3.16.0a1", false},
		{"1.99.0-beta.1", false},
		{"v2.0.0rc1", false},
		{"v2.0.0b2", false},
		{"3.10", false}, // CPython branch / EOL tag, not a version
		{"weekly.2026-01", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			_, got := f.Keep(rel(tc.title))
			if got != tc.want {
				t.Errorf("Keep(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}

func TestKeepFillsVersionAndStability(t *testing.T) {
	f, _ := New(Rules{StableOnly: true})
	r, ok := f.Keep(rel("go1.27.3"))
	if !ok || r.CoreVersion != "1.27.3" || !r.IsStable || r.Version != "go1.27.3" {
		t.Fatalf("unexpected kept release: %+v / ok=%v", r, ok)
	}
	r, ok = f.Keep(rel("go1.27rc3"))
	if ok || r.IsStable {
		t.Fatalf("prerelease should be rejected and marked unstable: %+v / ok=%v", r, ok)
	}
}

func TestApplyPreservesOrderAndFilters(t *testing.T) {
	f, _ := New(Rules{StableOnly: true})
	in := []domain.Release{rel("go1.27.3"), rel("go1.27rc3"), rel("go1.27.2")}
	out := f.Apply(in)
	if len(out) != 2 || out[0].Title != "go1.27.3" || out[1].Title != "go1.27.2" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestCustomVersionPatternAllowsTwoSegments(t *testing.T) {
	f, err := New(Rules{StableOnly: true, VersionPattern: `^v?(\d+\.\d+)$`})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Keep(rel("v3.10")); !ok {
		t.Error("custom pattern should accept v3.10")
	}
	if _, ok := f.Keep(rel("v3.15.2")); ok {
		t.Error("custom pattern should reject 3-segment versions")
	}
}

func TestCustomIncludeAndExclude(t *testing.T) {
	f, err := New(Rules{StableOnly: true, Include: []string{`^go`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Keep(rel("go1.27.3")); !ok {
		t.Error("include should keep go-prefixed versions")
	}
	if _, ok := f.Keep(rel("v3.15.2")); ok {
		t.Error("include should drop anything else")
	}

	f, err = New(Rules{StableOnly: true, Exclude: []string{`(?i)lts`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Keep(rel("go1.27.3-lts")); ok {
		t.Error("custom exclude should apply")
	}
}

func TestStableOnlyFalsePassesPrereleases(t *testing.T) {
	f, _ := New(Rules{StableOnly: false})
	r, ok := f.Keep(rel("v3.16.0a1"))
	if !ok {
		t.Fatal("stable_only=false should keep prereleases")
	}
	if r.IsStable {
		t.Error("prerelease must still be marked unstable")
	}
	if _, ok := f.Keep(rel("go1.27.3")); !ok {
		t.Error("stable release should still pass")
	}
}

func TestRealWorldGitHubReleaseTitles(t *testing.T) {
	f, err := New(Rules{StableOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		title   string
		version string
		core    string
		want    bool
	}{
		{"[release-branch.go1.27] go1.27.3", "go1.27.3", "1.27.3", true},
		{"[release-branch.go1.26] go1.26.10", "go1.26.10", "1.26.10", true},
		{"Rust 1.99.1", "1.99.1", "1.99.1", true},
		{"v3.15.2", "v3.15.2", "3.15.2", true},
		{"[release-branch.go1.27] go1.27rc3", "", "", false},
		{"v3.16.0a1", "", "", false},
		{"Python 3.10 end of life", "", "", false},
		{"1.99.0-beta.1", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			r, ok := f.Keep(rel(tc.title))
			if ok != tc.want {
				t.Fatalf("Keep = %v, want %v", ok, tc.want)
			}
			if !tc.want {
				return
			}
			if r.Version != tc.version || r.CoreVersion != tc.core {
				t.Errorf("got version=%q core=%q, want %q / %q", r.Version, r.CoreVersion, tc.version, tc.core)
			}
		})
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.27.3", "1.26.10", 1},
		{"3.11.18", "3.15.2", -1},
		{"1.9.0", "1.10.0", -1},
		{"1.2.3", "1.2.3", 0},
		{"", "1.2.3", 0},
		{"bad", "1.2.3", 0},
	}
	for _, tc := range cases {
		if got := CompareVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersion(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestInvalidRegexFailsAtConstruction(t *testing.T) {
	for _, r := range []Rules{
		{StableOnly: true, VersionPattern: "("},
		{StableOnly: true, Include: []string{"["}},
		{StableOnly: true, Exclude: []string{"*"}},
	} {
		if _, err := New(r); err == nil {
			t.Errorf("expected a compile error for %+v", r)
		}
	}
}
