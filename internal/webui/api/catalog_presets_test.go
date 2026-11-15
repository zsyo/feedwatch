//go:build webui

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"feedwatch/internal/webui/presets"
	"feedwatch/internal/webui/store"
)

// Local stand-in for the GitHub raw host. The production guard resolves DNS and
// rejects 127.0.0.1, so sync tests inject a permissive guard through Options.
type allowPresetGuard struct{}

func (allowPresetGuard) Check(context.Context, *url.URL) error { return nil }

type presetItem struct {
	PresetKey   string `json:"preset_key"`
	Version     int    `json:"version"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Enabled     bool   `json:"enabled"`
}

type presetCategoryFile struct {
	Format        string       `json:"format"`
	FormatVersion int          `json:"format_version"`
	Category      string       `json:"category"`
	Presets       []presetItem `json:"presets"`
}

type presetManifestFile struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type presetManifestFileList struct {
	Format        string               `json:"format"`
	FormatVersion int                  `json:"format_version"`
	Files         []presetManifestFile `json:"files"`
}

type presetRemote struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string]string
}

func (r *presetRemote) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(req.URL.Path, "/")
	r.mu.Lock()
	body, ok := r.files[name]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

func (r *presetRemote) set(name, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[name] = body
}

func (r *presetRemote) setJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	r.set(name, string(b))
}

func (r *presetRemote) useSingleCategory(t *testing.T, category string, items ...presetItem) {
	t.Helper()
	r.setJSON(t, presets.ManifestFileName, presetManifestFileList{
		Format: presets.ManifestFormat, FormatVersion: presets.FormatVersion,
		Files: []presetManifestFile{{Name: "a.json", Category: category}},
	})
	r.setJSON(t, "a.json", presetCategoryFile{
		Format: presets.CategoryFormat, FormatVersion: presets.FormatVersion,
		Category: category, Presets: items,
	})
}

func (r *presetRemote) useTwoFiles(t *testing.T) {
	t.Helper()
	r.setJSON(t, presets.ManifestFileName, presetManifestFileList{
		Format: presets.ManifestFormat, FormatVersion: presets.FormatVersion,
		Files: []presetManifestFile{
			{Name: "a.json", Category: "语言与运行时"},
			{Name: "b.json", Category: "编辑器"},
		},
	})
}

func newPresetSyncEnv(t *testing.T) (*httptest.Server, *store.DB, *presetRemote) {
	t.Helper()
	r := &presetRemote{files: make(map[string]string)}
	r.Server = httptest.NewServer(r)
	t.Cleanup(r.Server.Close)
	loader := &presets.Loader{
		Client: r.Client(), Guard: allowPresetGuard{}, BaseURL: r.URL,
		Timeout: 3 * time.Second, MaxBytes: presets.MaxBytes, MaxRedirects: 3,
	}
	ts, db := newTestServer(t, WithPresetLoader(loader))
	return ts, db, r
}

func makePresetItem(key string, version int, rawURL string) presetItem {
	return presetItem{
		PresetKey: key, Version: version, Name: fmt.Sprintf("K-%s-v%d", key, version),
		URL: rawURL, Description: "简介", Homepage: "https://example.com/" + key, Enabled: true,
	}
}

const (
	presetURL1 = "https://example.com/k1.atom"
	presetURL2 = "https://example.com/k2.atom"
	manualURL  = "https://example.com/manual.atom"
	presetCat  = "语言与运行时"
)

func syncPresetsNow(t *testing.T, c *testClient, want int) map[string]any {
	t.Helper()
	return c.do(http.MethodPost, "/api/admin/catalog/presets/sync", map[string]any{}, want)
}

func syncNum(out map[string]any, key string) float64 {
	v, _ := out[key].(float64)
	return v
}

type presetRowData struct {
	id      int64
	version int
	edited  int
	name    string
	url     string
	cat     string
}

func readPresetRow(t *testing.T, db *store.DB, key string) presetRowData {
	t.Helper()
	var row presetRowData
	err := db.QueryRow(`SELECT e.id, e.preset_version, e.preset_edited, e.name, e.url,
		COALESCE(c.name, '') FROM catalog_entries e
		LEFT JOIN catalog_categories c ON c.id = e.category_id WHERE e.preset_key=?`, key).
		Scan(&row.id, &row.version, &row.edited, &row.name, &row.url, &row.cat)
	if err != nil {
		t.Fatalf("read preset row %q: %v", key, err)
	}
	return row
}

func presetRowExists(t *testing.T, db *store.DB, key string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM catalog_entries WHERE preset_key=?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func expectSyncCounts(t *testing.T, out map[string]any, created, updated, skipped, protected, adopted float64) {
	t.Helper()
	got := []float64{
		syncNum(out, "created"), syncNum(out, "updated"), syncNum(out, "skipped"),
		syncNum(out, "protected"), syncNum(out, "adopted"),
	}
	want := []float64{created, updated, skipped, protected, adopted}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("counts %v, want %v (created/updated/skipped/protected/adopted): %+v", got, want, out)
		}
	}
}

// The central promise of the feature: remote files may be edited many times,
// but an equal or older version can never overwrite what is already stored.
func TestPresetSyncOnlyAppliesNewerVersions(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 1, presetURL1), makePresetItem("k2", 1, presetURL2))
	admin := login(t, ts, "admin", "admin-pass")

	out := syncPresetsNow(t, admin, http.StatusOK)
	if out["ok"] != true || out["synced_at"] == "" {
		t.Fatalf("missing success envelope: %+v", out)
	}
	if files := catalogItems(out, "files"); len(files) != 2 {
		t.Fatalf("per-file states = %d, want manifest + a.json: %+v", len(files), out)
	}
	expectSyncCounts(t, out, 2, 0, 0, 0, 0)
	for _, key := range []string{"k1", "k2"} {
		row := readPresetRow(t, db, key)
		if row.version != 1 || row.edited != 0 || row.cat != presetCat {
			t.Fatalf("%s after create: %+v", key, row)
		}
	}
	// A sync is a catalog-only operation: it must never create subscriptions,
	// pool rows or seen-ID baselines for any user.
	for _, table := range []string{"subscriptions", "feed_pool", "user_seen"} {
		if n := catalogCount(t, db, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Fatalf("sync created %d rows in %s", n, table)
		}
	}

	// Re-running the exact same document is a no-op.
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 0, 2, 0, 0)

	// Only one of the two entries moved forward.
	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 2, presetURL1), makePresetItem("k2", 1, presetURL2))
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 1, 1, 0, 0)
	if row := readPresetRow(t, db, "k1"); row.name != "K-k1-v2" || row.version != 2 || row.edited != 0 {
		t.Fatalf("remote update did not apply, or wrongly marked the row as edited: %+v", row)
	}
	if row := readPresetRow(t, db, "k2"); row.name != "K-k2-v1" || row.version != 1 {
		t.Fatalf("untouched entry was rewritten: %+v", row)
	}

	// A downgrade is skipped even though the other entry is still newer.
	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 1, presetURL1), makePresetItem("k2", 2, presetURL2))
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 1, 1, 0, 0)
	if row := readPresetRow(t, db, "k1"); row.name != "K-k1-v2" || row.version != 2 {
		t.Fatalf("an older remote version overwrote v2: %+v", row)
	}
	if row := readPresetRow(t, db, "k2"); row.name != "K-k2-v2" || row.version != 2 {
		t.Fatalf("newer version was not applied: %+v", row)
	}
}

// Pre-existing rows (seed examples, manual entries or imports) are claimed by
// URL. A row an administrator already touched stays protected after adoption.
func TestPresetSyncAdoptsExistingRowsByURL(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	oldCat := insertCatalogCategory(t, db, "旧分类")
	id1, _ := insertCatalogEntry(t, db, "手工 K1", presetURL1, oldCat)
	id2, _ := insertCatalogEntry(t, db, "手工 K2", presetURL2, oldCat)
	mustExec(t, db, `UPDATE catalog_entries SET preset_edited=1 WHERE id=?`, id2)

	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 1, presetURL1), makePresetItem("k2", 1, presetURL2))
	admin := login(t, ts, "admin", "admin-pass")
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 1, 0, 1, 2)

	updated := readPresetRow(t, db, "k1")
	if updated.id != id1 || updated.name != "K-k1-v1" || updated.version != 1 ||
		updated.edited != 0 || updated.cat != presetCat {
		t.Fatalf("clean row was not adopted and upgraded in place: %+v (id1=%d)", updated, id1)
	}
	protected := readPresetRow(t, db, "k2")
	if protected.id != id2 || protected.name != "手工 K2" || protected.version != 0 ||
		protected.edited != 1 || protected.cat != "旧分类" {
		t.Fatalf("edited row was overwritten despite protection: %+v (id2=%d)", protected, id2)
	}
	if n := catalogCount(t, db, `SELECT COUNT(*) FROM catalog_entries`); n != 2 {
		t.Fatalf("adoption duplicated rows: %d entries", n)
	}
}

// An administrator edit, an approved user proposal and the unlock endpoint all
// drive the same preset_edited flag.
func TestPresetSyncProtectsEditsAndAcceptRemoteUnlocks(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 1, presetURL1))
	admin := login(t, ts, "admin", "admin-pass")
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 1, 0, 0, 0, 0)
	id := readPresetRow(t, db, "k1").id

	admin.do(http.MethodPatch, "/api/admin/catalog/"+itoa(id),
		map[string]any{"name": "管理员改名"}, http.StatusOK)
	if row := readPresetRow(t, db, "k1"); row.name != "管理员改名" || row.edited != 1 {
		t.Fatalf("admin edit did not set the protection flag: %+v", row)
	}

	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 2, presetURL1))
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 0, 0, 1, 0)
	if row := readPresetRow(t, db, "k1"); row.name != "管理员改名" || row.version != 1 || row.edited != 1 {
		t.Fatalf("protected entry was overwritten: %+v", row)
	}

	// accept-remote is only meaningful for preset-backed rows.
	manualCat := insertCatalogCategory(t, db, "手工分类")
	manualID, _ := insertCatalogEntry(t, db, "手工", manualURL, manualCat)
	admin.do(http.MethodPost, "/api/admin/catalog/"+itoa(manualID)+"/accept-remote",
		map[string]any{}, http.StatusNotFound)

	unlockPath := "/api/admin/catalog/" + itoa(id) + "/accept-remote"
	admin.do(http.MethodPost, unlockPath, map[string]any{}, http.StatusOK)
	if row := readPresetRow(t, db, "k1"); row.edited != 0 || row.version != 1 {
		t.Fatalf("unlock changed more than the protection flag: %+v", row)
	}
	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 3, presetURL1))
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 1, 0, 0, 0)
	if row := readPresetRow(t, db, "k1"); row.name != "K-k1-v3" || row.version != 3 || row.edited != 0 {
		t.Fatalf("unlocked entry did not accept v3: %+v", row)
	}

	// A member must not be able to unlock anything.
	alice := login(t, ts, "alice", "alice-pass")
	out := alice.do(http.MethodPost, unlockPath, map[string]any{}, http.StatusForbidden)
	if code := catalogErrorCode(out); code != "forbidden" {
		t.Fatalf("member accept-remote: want forbidden, got %+v", out)
	}
}

func TestPresetSyncProtectsReviewedUserUpdates(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 1, presetURL1))
	admin := login(t, ts, "admin", "admin-pass")
	alice := login(t, ts, "alice", "alice-pass")
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 1, 0, 0, 0, 0)

	row := readPresetRow(t, db, "k1")
	var catID int64
	if err := db.QueryRow(`SELECT id FROM catalog_categories WHERE name=?`, presetCat).Scan(&catID); err != nil {
		t.Fatal(err)
	}
	created := catalogSubmit(alice, "update", &row.id, "用户维护的新名字", presetURL1, catID, http.StatusCreated)
	subID := catalogID(created, "id")
	admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(subID)+"/approve",
		map[string]any{}, http.StatusOK)
	if got := readPresetRow(t, db, "k1"); got.name != "用户维护的新名字" || got.edited != 1 {
		t.Fatalf("approved update did not protect the row: %+v", got)
	}

	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 2, presetURL1))
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 0, 0, 1, 0)
	if got := readPresetRow(t, db, "k1"); got.name != "用户维护的新名字" {
		t.Fatalf("reviewed name was overwritten: %+v", got)
	}
}

// Deleting a preset writes a tombstone: a later remote update must not bring
// back something the administrator deliberately removed.
func TestPresetSyncTombstoneNeverResurrects(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 1, presetURL1))
	admin := login(t, ts, "admin", "admin-pass")
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 1, 0, 0, 0, 0)
	id := readPresetRow(t, db, "k1").id
	admin.do(http.MethodDelete, "/api/admin/catalog/"+itoa(id), nil, http.StatusOK)

	var deleted int
	if err := db.QueryRow(`SELECT deleted FROM catalog_preset_state WHERE preset_key='k1'`).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("delete did not write a tombstone: deleted=%d err=%v", deleted, err)
	}

	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 2, presetURL1))
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 0, 1, 0, 0)
	if presetRowExists(t, db, "k1") {
		t.Fatal("tombstoned preset was resurrected")
	}
	if err := db.QueryRow(`SELECT deleted FROM catalog_preset_state WHERE preset_key='k1'`).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("sync cleared the tombstone: deleted=%d err=%v", deleted, err)
	}
}

// All network IO and document validation happen before Begin(): a broken file
// must leave the whole catalog exactly as it was.
func TestPresetSyncFetchFailureWritesNothing(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 1, presetURL1), makePresetItem("k2", 1, presetURL2))
	admin := login(t, ts, "admin", "admin-pass")
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 2, 0, 0, 0, 0)

	// The first file would have applied cleanly, but the second is broken.
	remote.useTwoFiles(t)
	remote.setJSON(t, "a.json", presetCategoryFile{
		Format: presets.CategoryFormat, FormatVersion: presets.FormatVersion,
		Category: presetCat, Presets: []presetItem{
			makePresetItem("k1", 2, presetURL1), makePresetItem("k2", 2, presetURL2),
		},
	})
	remote.set("b.json", "{not json")
	out := syncPresetsNow(t, admin, http.StatusBadGateway)
	if code := catalogErrorCode(out); code != "preset_fetch_failed" {
		t.Fatalf("want preset_fetch_failed, got %+v", out)
	}
	files := catalogItems(out, "files")
	if len(files) != 3 {
		t.Fatalf("per-file states = %d, want 3: %+v", len(files), out)
	}
	last := files[2].(map[string]any)
	if last["name"] != "b.json" || last["status"] != "failed" {
		t.Fatalf("failed file was not named: %+v", last)
	}
	for _, key := range []string{"k1", "k2"} {
		if row := readPresetRow(t, db, key); row.version != 1 || row.name != fmt.Sprintf("K-%s-v1", key) {
			t.Fatalf("%s changed despite the failed sync: %+v", key, row)
		}
	}

	// A document that parses but fails Prepare must be rejected the same way.
	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 2, presetURL1), makePresetItem("k1", 2, presetURL2))
	out = syncPresetsNow(t, admin, http.StatusBadRequest)
	if code := catalogErrorCode(out); code != "preset_invalid_document" {
		t.Fatalf("want preset_invalid_document, got %+v", out)
	}
	if row := readPresetRow(t, db, "k1"); row.version != 1 {
		t.Fatalf("invalid document changed the catalog: %+v", row)
	}
}

// applyPresets can also fail INSIDE the transaction. Here k1 updates first,
// then k2 collides with a manual row's URL: the 409 must roll k1 back too.
func TestPresetSyncConflictRollsBackWholeTransaction(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	manualCat := insertCatalogCategory(t, db, "手工分类")
	manualID, _ := insertCatalogEntry(t, db, "手工占用", manualURL, manualCat)
	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 1, presetURL1), makePresetItem("k2", 1, presetURL2))
	admin := login(t, ts, "admin", "admin-pass")
	expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 2, 0, 0, 0, 0)

	remote.useSingleCategory(t, presetCat,
		makePresetItem("k1", 2, presetURL1), makePresetItem("k2", 2, manualURL))
	out := syncPresetsNow(t, admin, http.StatusConflict)
	if code := catalogErrorCode(out); code != "preset_conflict" {
		t.Fatalf("want preset_conflict, got %+v", out)
	}
	if row := readPresetRow(t, db, "k1"); row.version != 1 || row.name != "K-k1-v1" {
		t.Fatalf("k1 update was not rolled back: %+v", row)
	}
	if row := readPresetRow(t, db, "k2"); row.version != 1 || row.url != presetURL2 {
		t.Fatalf("k2 was partially written: %+v", row)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM catalog_entries WHERE id=?`, manualID).Scan(&name); err != nil || name != "手工占用" {
		t.Fatalf("manual row was touched: name=%q err=%v", name, err)
	}
}

func TestPresetSyncRequiresConfiguredLoader(t *testing.T) {
	ts, _ := newTestServer(t, WithPresetLoader(nil))
	admin := login(t, ts, "admin", "admin-pass")
	out := syncPresetsNow(t, admin, http.StatusInternalServerError)
	if code := catalogErrorCode(out); code != "db_error" {
		t.Fatalf("want db_error, got %+v", out)
	}
}

func TestCatalogListExposesPresetState(t *testing.T) {
	ts, _, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 1, presetURL1))
	admin := login(t, ts, "admin", "admin-pass")
	syncPresetsNow(t, admin, http.StatusOK)

	for _, c := range []*testClient{admin, login(t, ts, "alice", "alice-pass")} {
		path := "/api/catalog"
		if c == admin {
			path = "/api/admin/catalog"
		}
		list := c.do(http.MethodGet, path, nil, http.StatusOK)
		entries := catalogItems(list, "entries")
		if len(entries) != 1 {
			t.Fatalf("%s entries = %d, want 1", path, len(entries))
		}
		row := entries[0].(map[string]any)
		if row["preset_key"] != "k1" || row["preset_version"] != float64(1) || row["preset_edited"] != false {
			t.Fatalf("%s missing preset state: %+v", path, row)
		}
	}
}

// Guard against a subtle regression: an earlier implementation set
// preset_edited=1 on remote updates, which would have made every later sync
// permanently report "protected". Asserted directly on the column as well as
// through the API response.
func TestPresetSyncRemoteUpdateNeverSetsEditedFlag(t *testing.T) {
	ts, db, remote := newPresetSyncEnv(t)
	remote.useSingleCategory(t, presetCat, makePresetItem("k1", 1, presetURL1))
	admin := login(t, ts, "admin", "admin-pass")
	syncPresetsNow(t, admin, http.StatusOK)
	for v := 2; v <= 4; v++ {
		remote.useSingleCategory(t, presetCat, makePresetItem("k1", v, presetURL1))
		expectSyncCounts(t, syncPresetsNow(t, admin, http.StatusOK), 0, 1, 0, 0, 0)
	}
	row := readPresetRow(t, db, "k1")
	if row.version != 4 || row.edited != 0 {
		t.Fatalf("consecutive updates stopped working: %+v", row)
	}
	var stateVersion int
	if err := db.QueryRow(`SELECT last_version FROM catalog_preset_state WHERE preset_key='k1'`).
		Scan(&stateVersion); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	} else if stateVersion != 4 {
		t.Fatalf("state last_version = %d, want 4", stateVersion)
	}
}
