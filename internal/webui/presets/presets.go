//go:build webui

// Package presets loads versioned catalog templates from a remote manifest.
// It performs no database work: the api package owns the transaction that
// applies a Snapshot. Everything here stays pure and independently testable.
package presets

import (
	"fmt"
	"regexp"

	"feedwatch/internal/webui"
)

const (
	ManifestFormat = "feedwatch-presets"
	CategoryFormat = "feedwatch-presets-category"
	FormatVersion  = 1

	// MaxBytes caps each remote JSON document. The API's decodeJSON uses the
	// same 1 MiB limit for the manual import path.
	MaxBytes = 1 << 20
)

var (
	fileNameRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.json$`)
	presetKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type Manifest struct {
	Format        string         `json:"format"`
	FormatVersion int            `json:"format_version"`
	Files         []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type CategoryFile struct {
	Format        string   `json:"format"`
	FormatVersion int      `json:"format_version"`
	Category      string   `json:"category"`
	Presets       []Preset `json:"presets"`
}

// Enabled is a pointer so an omitted field means "true", not "false".
type Preset struct {
	PresetKey   string `json:"preset_key"`
	Version     int    `json:"version"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Enabled     *bool  `json:"enabled"`
	Category    string `json:"category,omitempty"`
}

type FileState struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" | "failed"
	Reason string `json:"reason,omitempty"`
}

// Snapshot is the fully parsed, not-yet-validated remote payload. States is
// returned even on error so the API can name the file that failed.
type Snapshot struct {
	Files  []LoadedFile
	States []FileState
}

type LoadedFile struct {
	Name     string
	Category string
	Presets  []Preset
}

// Prepared is one validated entry, ready for the database transaction.
type Prepared struct {
	FileName    string
	PresetKey   string
	Version     int
	Name        string
	URL         string
	URLKey      string
	Description string
	Homepage    string
	Category    string
	Enabled     bool
}

// ValidateManifest checks the manifest header and its file list. Names are
// restricted to plain basenames so a manifest cannot reach outside baseURL.
func ValidateManifest(m Manifest) error {
	if m.Format != ManifestFormat || m.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported manifest format %q version %d", m.Format, m.FormatVersion)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("manifest lists no category files")
	}
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if !fileNameRE.MatchString(f.Name) {
			return fmt.Errorf("invalid category file name %q", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("duplicate category file %q", f.Name)
		}
		seen[f.Name] = true
		name, err := webui.CleanCatalogText(f.Category, webui.MaxCatalogCategoryLen, "category")
		if err != nil || name == "" {
			return fmt.Errorf("invalid category for %q", f.Name)
		}
	}
	return nil
}

// ValidateCategoryFile cross-checks a category file against its manifest row.
// The file's own category is authoritative; the manifest copy exists so the
// loader can spot drift without reading every file.
func ValidateCategoryFile(fileName, declaredCategory string, f CategoryFile) error {
	if f.Format != CategoryFormat || f.FormatVersion != FormatVersion {
		return fmt.Errorf("%s: unsupported format %q version %d", fileName, f.Format, f.FormatVersion)
	}
	cat, err := webui.CleanCatalogText(f.Category, webui.MaxCatalogCategoryLen, "category")
	if err != nil || cat == "" {
		return fmt.Errorf("%s: invalid category", fileName)
	}
	declared, err := webui.CleanCatalogText(declaredCategory, webui.MaxCatalogCategoryLen, "category")
	if err != nil || cat != declared {
		return fmt.Errorf("%s: category %q does not match manifest %q", fileName, cat, declared)
	}
	if len(f.Presets) == 0 {
		return fmt.Errorf("%s: no presets", fileName)
	}
	return nil
}

// Prepare validates every entry in one pass and deduplicates preset_key and
// url_key ACROSS files. It must be called before any transaction is opened so
// a bad document can never leave a half-updated catalog behind.
func Prepare(snap *Snapshot) ([]Prepared, error) {
	out := make([]Prepared, 0)
	keys := make(map[string]bool)
	urls := make(map[string]bool)
	for _, f := range snap.Files {
		for i, p := range f.Presets {
			where := fmt.Sprintf("%s entry %d", f.Name, i+1)
			if !presetKeyRE.MatchString(p.PresetKey) {
				return nil, fmt.Errorf("%s: invalid preset_key %q", where, p.PresetKey)
			}
			if keys[p.PresetKey] {
				return nil, fmt.Errorf("%s: duplicate preset_key %q", where, p.PresetKey)
			}
			keys[p.PresetKey] = true
			if p.Version < 1 {
				return nil, fmt.Errorf("%s: version must be a positive integer", where)
			}
			name, err := webui.CleanCatalogText(p.Name, webui.MaxCatalogNameLen, "name")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			if name == "" {
				return nil, fmt.Errorf("%s: name is empty", where)
			}
			normalized, urlKey, err := webui.CatalogURLKey(p.URL)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid url: %w", where, err)
			}
			if urls[urlKey] {
				return nil, fmt.Errorf("%s: duplicate url %q", where, p.URL)
			}
			urls[urlKey] = true
			desc, err := webui.CleanCatalogText(p.Description, webui.MaxCatalogDescriptionLen, "description")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			home, err := webui.CleanCatalogHomepage(p.Homepage)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			category := f.Category
			if p.Category != "" {
				c, err := webui.CleanCatalogText(p.Category, webui.MaxCatalogCategoryLen, "category")
				if err != nil || c != category {
					return nil, fmt.Errorf("%s: entry category %q does not match file category %q", where, p.Category, category)
				}
			}
			enabled := true
			if p.Enabled != nil {
				enabled = *p.Enabled
			}
			out = append(out, Prepared{
				FileName: f.Name, PresetKey: p.PresetKey, Version: p.Version,
				Name: name, URL: normalized, URLKey: urlKey,
				Description: desc, Homepage: home, Category: category, Enabled: enabled,
			})
		}
	}
	return out, nil
}
