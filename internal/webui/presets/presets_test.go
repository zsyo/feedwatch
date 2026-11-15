//go:build webui

package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func samplePreset(key, rawURL string) Preset {
	return Preset{
		PresetKey:   key,
		Version:     1,
		Name:        "Name " + key,
		URL:         rawURL,
		Description: "简介",
		Homepage:    "https://example.com/" + key,
	}
}

func sampleManifest() Manifest {
	return Manifest{
		Format:        ManifestFormat,
		FormatVersion: FormatVersion,
		Files: []ManifestFile{
			{Name: "a.json", Category: "语言与运行时"},
			{Name: "b.json", Category: "编辑器"},
		},
	}
}

func sampleCategory(category string, items ...Preset) CategoryFile {
	return CategoryFile{
		Format:        CategoryFormat,
		FormatVersion: FormatVersion,
		Category:      category,
		Presets:       items,
	}
}

func sampleSnapshot() *Snapshot {
	return &Snapshot{Files: []LoadedFile{
		{Name: "a.json", Category: "语言与运行时", Presets: []Preset{
			samplePreset("k1", "https://example.com/a.atom"),
			samplePreset("k2", "https://example.com/b.atom"),
		}},
		{Name: "b.json", Category: "编辑器", Presets: []Preset{
			samplePreset("k3", "https://example.com/c.atom"),
		}},
	}}
}

func TestValidateManifest(t *testing.T) {
	if err := ValidateManifest(sampleManifest()); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"bad format", func(m *Manifest) { m.Format = "something-else" }, "unsupported manifest format"},
		{"bad version", func(m *Manifest) { m.FormatVersion = 2 }, "unsupported manifest format"},
		{"no files", func(m *Manifest) { m.Files = nil }, "lists no category files"},
		{"path traversal", func(m *Manifest) { m.Files[0].Name = "../secret.json" }, "invalid category file name"},
		{"absolute path", func(m *Manifest) { m.Files[0].Name = "/etc/passwd" }, "invalid category file name"},
		{"non-json", func(m *Manifest) { m.Files[0].Name = "a.yaml" }, "invalid category file name"},
		{"duplicate file", func(m *Manifest) { m.Files[1].Name = "a.json" }, "duplicate category file"},
		{"empty category", func(m *Manifest) { m.Files[0].Category = " " }, "invalid category"},
		{"category with newline", func(m *Manifest) { m.Files[0].Category = "a\nb" }, "invalid category"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sampleManifest()
			tc.mutate(&m)
			err := ValidateManifest(m)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateCategoryFile(t *testing.T) {
	cf := sampleCategory("语言与运行时", samplePreset("k1", "https://example.com/a.atom"))
	if err := ValidateCategoryFile("a.json", "语言与运行时", cf); err != nil {
		t.Fatalf("valid category rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*CategoryFile)
		want   string
	}{
		{"format mismatch", func(f *CategoryFile) { f.Format = ManifestFormat }, "unsupported format"},
		{"version mismatch", func(f *CategoryFile) { f.FormatVersion = 99 }, "unsupported format"},
		{"category drift", func(f *CategoryFile) { f.Category = "别的分类" }, "does not match manifest"},
		{"empty category", func(f *CategoryFile) { f.Category = "" }, "invalid category"},
		{"no presets", func(f *CategoryFile) { f.Presets = nil }, "no presets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cf
			tc.mutate(&f)
			err := ValidateCategoryFile("a.json", "语言与运行时", f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestPrepareNormalizesAndDefaults(t *testing.T) {
	snap := sampleSnapshot()
	// An omitted `enabled` means true; an explicit false must be preserved.
	p := snap.Files[0].Presets[1]
	no := false
	p.Enabled = &no
	snap.Files[0].Presets[1] = p

	got, err := Prepare(snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("prepared %d entries, want 3", len(got))
	}
	if got[0].FileName != "a.json" || got[0].Category != "语言与运行时" {
		t.Fatalf("first entry lost its file/category: %+v", got[0])
	}
	if !got[0].Enabled || got[1].Enabled {
		t.Fatalf("enabled defaults are wrong: %v / %v", got[0].Enabled, got[1].Enabled)
	}
	// URLKey collapses the fragment and default port, so downstream matching
	// is stable even if a maintainer edits only the cosmetic part of a URL.
	if got[0].URLKey == "" || got[0].URL != "https://example.com/a.atom" {
		t.Fatalf("URL was not normalized: %+v", got[0])
	}
	if got[2].Category != "编辑器" || got[2].PresetKey != "k3" {
		t.Fatalf("third entry lost its file context: %+v", got[2])
	}
}

func TestPrepareRejectsBadDocuments(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{"invalid key", func(s *Snapshot) { s.Files[0].Presets[0].PresetKey = "../k1" }, "invalid preset_key"},
		{"empty key", func(s *Snapshot) { s.Files[0].Presets[0].PresetKey = "" }, "invalid preset_key"},
		{"duplicate key across files", func(s *Snapshot) { s.Files[1].Presets[0].PresetKey = "k1" }, "duplicate preset_key"},
		{"zero version", func(s *Snapshot) { s.Files[0].Presets[0].Version = 0 }, "version must be a positive integer"},
		{"negative version", func(s *Snapshot) { s.Files[0].Presets[0].Version = -1 }, "version must be a positive integer"},
		{"empty name", func(s *Snapshot) { s.Files[0].Presets[0].Name = "  " }, "name is empty"},
		{"name with newline", func(s *Snapshot) { s.Files[0].Presets[0].Name = "a\nb" }, "must not contain line breaks"},
		{"userinfo in feed URL", func(s *Snapshot) { s.Files[0].Presets[0].URL = "https://u:p@example.com/a.atom" }, "invalid url"},
		{"credential query in feed URL", func(s *Snapshot) { s.Files[0].Presets[0].URL = "https://example.com/a.atom?token=x" }, "invalid url"},
		{"ftp feed URL", func(s *Snapshot) { s.Files[0].Presets[0].URL = "ftp://example.com/a.atom" }, "invalid url"},
		{"duplicate url across files", func(s *Snapshot) {
			s.Files[1].Presets[0].URL = "https://example.com/a.atom#mirror"
		}, "duplicate url"},
		{"bad homepage", func(s *Snapshot) { s.Files[0].Presets[0].Homepage = "javascript:alert(1)" }, "homepage"},
		{"entry category drift", func(s *Snapshot) { s.Files[0].Presets[0].Category = "编辑器" }, "does not match file category"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := sampleSnapshot()
			tc.mutate(snap)
			_, err := Prepare(snap)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The JSON files under presets/ are shipped documentation-as-data: a typo in a
// key or a duplicate URL would break every deployment on the first sync. Pin
// them to the same validators the remote loader uses.
func TestCheckedInPresetFilesAreValid(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "presets")
	read := func(name string) []byte {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return body
	}
	var m Manifest
	if err := json.Unmarshal(read(ManifestFileName), &m); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("shipped manifest is invalid: %v", err)
	}
	snap := &Snapshot{}
	for _, f := range m.Files {
		var cf CategoryFile
		if err := json.Unmarshal(read(f.Name), &cf); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if err := ValidateCategoryFile(f.Name, f.Category, cf); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		snap.Files = append(snap.Files, LoadedFile{Name: f.Name, Category: cf.Category, Presets: cf.Presets})
	}
	items, err := Prepare(snap)
	if err != nil {
		t.Fatalf("shipped presets are invalid: %v", err)
	}
	if len(items) < 20 {
		t.Fatalf("only %d usable presets shipped; a file may have been truncated", len(items))
	}
	keys := make(map[string]bool, len(items))
	for _, p := range items {
		if keys[p.PresetKey] {
			t.Fatalf("duplicate preset_key %q in shipped files", p.PresetKey)
		}
		keys[p.PresetKey] = true
		if p.Version < 1 || p.URLKey == "" || p.Category == "" {
			t.Fatalf("incomplete preset %q: %+v", p.PresetKey, p)
		}
	}
}
