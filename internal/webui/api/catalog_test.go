//go:build webui

package api

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"feedwatch/internal/webui"
	"feedwatch/internal/webui/store"
)

func insertCatalogCategory(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO catalog_categories (name, created_at, updated_at) VALUES (?,?,?)`,
		name, store.Now(), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertCatalogEntry(t *testing.T, db *sql.DB, name, rawURL string, categoryID int64) (int64, string) {
	t.Helper()
	normalized, key, err := webui.CatalogURLKey(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO catalog_entries
		(url, url_key, name, category_id, description, homepage, source_type, enabled, created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,'atom',1,NULL,?,?)`,
		normalized, key, name, categoryID, "简介", "https://example.com", store.Now(), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id, key
}

func catalogCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func catalogID(out map[string]any, key string) int64 {
	return int64(out[key].(float64))
}

func catalogItems(out map[string]any, key string) []any {
	items, _ := out[key].([]any)
	return items
}

func catalogErrorCode(out map[string]any) string {
	errMap, _ := out["error"].(map[string]any)
	code, _ := errMap["code"].(string)
	return code
}

func catalogSubmit(c *testClient, kind string, entryID *int64, name, rawURL string, categoryID int64, want int) map[string]any {
	body := map[string]any{
		"kind": kind, "name": name, "url": rawURL,
		"category_id": categoryID, "description": "测试简介",
	}
	if entryID != nil {
		body["entry_id"] = *entryID
	}
	return c.do(http.MethodPost, "/api/catalog/submissions", body, want)
}

func TestCatalogRoleGuard(t *testing.T) {
	ts, _ := newTestServer(t)
	admin := login(t, ts, "admin", "admin-pass")
	alice := login(t, ts, "alice", "alice-pass")

	memberEndpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/catalog"},
		{http.MethodGet, "/api/catalog/pending"},
		{http.MethodGet, "/api/catalog/submissions/mine"},
		{http.MethodPost, "/api/catalog/submissions"},
		{http.MethodDelete, "/api/catalog/submissions/1"},
		{http.MethodPost, "/api/catalog/1/subscribe"},
	}
	for _, e := range memberEndpoints {
		out := admin.do(e.method, e.path, nil, http.StatusForbidden)
		if code := catalogErrorCode(out); code != "role_not_allowed" {
			t.Errorf("admin %s %s: want role_not_allowed, got %v", e.method, e.path, out)
		}
	}
	adminEndpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/admin/catalog"},
		{http.MethodPost, "/api/admin/catalog"},
		{http.MethodPatch, "/api/admin/catalog/1"},
		{http.MethodDelete, "/api/admin/catalog/1"},
		{http.MethodGet, "/api/admin/catalog/submissions"},
		{http.MethodGet, "/api/admin/catalog/categories"},
		{http.MethodPost, "/api/admin/catalog/categories"},
		{http.MethodGet, "/api/admin/catalog/export"},
		{http.MethodPost, "/api/admin/catalog/import"},
		{http.MethodPost, "/api/admin/catalog/presets/sync"},
		{http.MethodPost, "/api/admin/catalog/1/accept-remote"},
	}
	for _, e := range adminEndpoints {
		out := alice.do(e.method, e.path, nil, http.StatusForbidden)
		if code := catalogErrorCode(out); code != "forbidden" {
			t.Errorf("member %s %s: want forbidden, got %v", e.method, e.path, out)
		}
	}
	anon := &testClient{t: t, base: ts.URL, http: &http.Client{}}
	anon.do(http.MethodGet, "/api/catalog", nil, http.StatusUnauthorized)
}

// The most important invariant of quick subscribe: it must copy the current
// seen_ids baseline, otherwise the next cycle replays every historical
// release as if it were new.
func TestQuickSubscribeCopiesBaseline(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	entryID, key := insertCatalogEntry(t, db, "示例项目", "https://example.com/feed.atom", cat)
	mustExec(t, db, `INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)`, key, "r1", store.Now())
	mustExec(t, db, `INSERT INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)`, key, "r2", store.Now())

	alice := login(t, ts, "alice", "alice-pass")
	out := alice.do(http.MethodPost, "/api/catalog/"+itoa(entryID)+"/subscribe", map[string]any{}, http.StatusCreated)
	if out["url_key"] != key {
		t.Fatalf("url_key = %v, want %s", out["url_key"], key)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM subscriptions`); got != 1 {
		t.Fatalf("subscriptions = %d, want 1", got)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM feed_pool`); got != 1 {
		t.Fatalf("feed_pool = %d, want 1", got)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM user_seen WHERE url_key=?`, key); got != 2 {
		t.Fatalf("user_seen baseline = %d, want 2", got)
	}

	// The listing must expose the subscription so the SPA can offer "cancel".
	list := alice.do(http.MethodGet, "/api/catalog", nil, http.StatusOK)
	entries := catalogItems(list, "entries")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	first := entries[0].(map[string]any)
	if first["subscribed"] != true || first["subscriber_count"].(float64) != 1 {
		t.Fatalf("listing did not report the subscription: %+v", first)
	}
	subID := int64(first["subscription_id"].(float64))

	// A second click is a clean 409, not a duplicate row.
	alice.do(http.MethodPost, "/api/catalog/"+itoa(entryID)+"/subscribe", map[string]any{}, http.StatusConflict)
	alice.do(http.MethodDelete, "/api/subscriptions/"+itoa(subID), nil, http.StatusOK)
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM subscriptions WHERE deleted_at IS NULL`); got != 0 {
		t.Fatalf("live subscriptions after cancel = %d, want 0", got)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM catalog_entries`); got != 1 {
		t.Fatalf("cancel must not delete the catalog template: %d", got)
	}
}

func TestCatalogSubmissionDeduplicationAndLimits(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	insertCatalogEntry(t, db, "已存在", "https://example.com/existing.atom", cat)
	alice := login(t, ts, "alice", "alice-pass")
	mustUser(t, db, "bob", "bob-pass", "user", 0)
	bob := login(t, ts, "bob", "bob-pass")

	// Colliding with an approved entry, with a live proposal, and with an
	// equivalent spelling of that proposal's URL.
	out := catalogSubmit(alice, "new", nil, "撞目录", "https://example.com/existing.atom", cat, http.StatusConflict)
	if code := catalogErrorCode(out); code != "duplicate_catalog" {
		t.Fatalf("want duplicate_catalog, got %v", out)
	}
	catalogSubmit(alice, "new", nil, "新项目", "https://example.com/new.atom?a=1&b=2", cat, http.StatusCreated)
	// NormalizeURL keeps query values, so an equivalent spelling must repeat
	// them (only their order, the case and the default port may differ).
	out = catalogSubmit(bob, "new", nil, "别名重复", "HTTPS://EXAMPLE.com:443/new.atom?b=2&a=1#frag", cat, http.StatusConflict)
	if code := catalogErrorCode(out); code != "duplicate_pending" {
		t.Fatalf("equivalent URL should collide with the pending proposal, got %v", out)
	}

	for _, raw := range []string{
		"ftp://example.com/feed.atom",
		"https://user:pass@example.com/feed.atom",
		"https://example.com/feed.atom?token=secret",
		"/relative/feed.atom",
	} {
		out := catalogSubmit(bob, "new", nil, "非法链接", raw, cat, http.StatusBadRequest)
		if code := catalogErrorCode(out); code != "invalid_url" {
			t.Errorf("%q: want invalid_url, got %v", raw, out)
		}
	}
	out = catalogSubmit(bob, "new", nil, strings.Repeat("a", 90), "https://example.com/long.atom", cat, http.StatusBadRequest)
	if code := catalogErrorCode(out); code != "invalid_name" {
		t.Errorf("long name: want invalid_name, got %v", out)
	}
	out = catalogSubmit(bob, "new", nil, "分类不存在", "https://example.com/nocat.atom", 9999, http.StatusBadRequest)
	if code := catalogErrorCode(out); code != "invalid_category" {
		t.Errorf("missing category: want invalid_category, got %v", out)
	}
	out = catalogSubmit(bob, "new", nil, "缺少分类", "https://example.com/nocat2.atom", 0, http.StatusBadRequest)
	if code := catalogErrorCode(out); code != "invalid_category" {
		t.Errorf("category 0: want invalid_category, got %v", out)
	}

	// One account may not flood the review queue. alice already has one.
	for i := 0; i < webui.MaxPendingPerUser-1; i++ {
		catalogSubmit(alice, "new", nil, "批量", "https://example.com/bulk"+itoa(int64(i))+".atom", cat, http.StatusCreated)
	}
	out = catalogSubmit(alice, "new", nil, "超额", "https://example.com/overflow.atom", cat, http.StatusTooManyRequests)
	if code := catalogErrorCode(out); code != "pending_limit" {
		t.Fatalf("want pending_limit, got %v", out)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM submissions WHERE status='pending'`); got != webui.MaxPendingPerUser {
		t.Fatalf("pending rows = %d, want %d", got, webui.MaxPendingPerUser)
	}
}

func TestCatalogNewSubmissionReviewFlow(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	alice := login(t, ts, "alice", "alice-pass")
	admin := login(t, ts, "admin", "admin-pass")

	created := catalogSubmit(alice, "new", nil, "待审项目", "https://example.com/pending.atom", cat, http.StatusCreated)
	subID := catalogID(created, "id")
	pending := alice.do(http.MethodGet, "/api/catalog/pending", nil, http.StatusOK)
	items := catalogItems(pending, "submissions")
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "new" {
		t.Fatalf("pending list = %+v", items)
	}
	list := alice.do(http.MethodGet, "/api/catalog", nil, http.StatusOK)
	if len(catalogItems(list, "entries")) != 0 {
		t.Fatal("a pending proposal must not appear in the browsable catalog")
	}

	approved := admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(subID)+"/approve", map[string]any{}, http.StatusOK)
	entryID := catalogID(approved, "entry_id")
	if entryID <= 0 {
		t.Fatalf("approve returned no entry id: %+v", approved)
	}
	list = alice.do(http.MethodGet, "/api/catalog", nil, http.StatusOK)
	if len(catalogItems(list, "entries")) != 1 {
		t.Fatal("approved entry should appear in the catalog")
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM submissions WHERE status='approved'`); got != 1 {
		t.Fatalf("approved submissions = %d", got)
	}
	out := admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(subID)+"/approve", map[string]any{}, http.StatusConflict)
	if code := catalogErrorCode(out); code != "not_pending" {
		t.Fatalf("second approve: want not_pending, got %v", out)
	}

	// A rejection must carry a reason back to the author.
	rejected := catalogSubmit(alice, "new", nil, "会被驳回", "https://example.com/reject.atom", cat, http.StatusCreated)
	rejectID := catalogID(rejected, "id")
	out = admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(rejectID)+"/reject", map[string]any{}, http.StatusBadRequest)
	if code := catalogErrorCode(out); code != "bad_request" {
		t.Errorf("empty reason should be rejected, got %v", out)
	}
	admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(rejectID)+"/reject",
		map[string]any{"reason": "链接无法访问"}, http.StatusOK)
	mine := alice.do(http.MethodGet, "/api/catalog/submissions/mine", nil, http.StatusOK)
	found := false
	for _, raw := range catalogItems(mine, "submissions") {
		row := raw.(map[string]any)
		if int64(row["id"].(float64)) == rejectID {
			found = true
			if row["status"] != "rejected" || row["reject_reason"] != "链接无法访问" {
				t.Fatalf("rejection not surfaced: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("rejected proposal missing from my submissions")
	}
}

func TestCatalogUpdateSubmissionReviewFlow(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	otherCat := insertCatalogCategory(t, db, "运行时")
	entryID, _ := insertCatalogEntry(t, db, "原名称", "https://example.com/original.atom", cat)
	alice := login(t, ts, "alice", "alice-pass")
	admin := login(t, ts, "admin", "admin-pass")

	created := catalogSubmit(alice, "update", &entryID, "新名称",
		"https://example.com/updated.atom", otherCat, http.StatusCreated)
	subID := catalogID(created, "id")
	queue := admin.do(http.MethodGet, "/api/admin/catalog/submissions", nil, http.StatusOK)
	var before map[string]any
	for _, raw := range catalogItems(queue, "submissions") {
		row := raw.(map[string]any)
		if int64(row["id"].(float64)) == subID {
			before, _ = row["before_json"].(map[string]any)
		}
	}
	if before == nil || before["name"] != "原名称" {
		t.Fatalf("before_json snapshot is missing: %+v", queue)
	}
	admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(subID)+"/approve", map[string]any{}, http.StatusOK)
	var name, url string
	var categoryID int64
	if err := db.QueryRow(`SELECT name, url, category_id FROM catalog_entries WHERE id=?`, entryID).
		Scan(&name, &url, &categoryID); err != nil {
		t.Fatal(err)
	}
	if name != "新名称" || url != "https://example.com/updated.atom" || categoryID != otherCat {
		t.Fatalf("update was not applied: name=%q url=%q category=%d", name, url, categoryID)
	}

	// A new proposal whose URL is added directly by an administrator while it
	// sits in review must fail with conflict_url instead of overwriting it.
	conflict := catalogSubmit(alice, "new", nil, "稍后被占用", "https://example.com/race.atom", cat, http.StatusCreated)
	conflictID := catalogID(conflict, "id")
	admin.do(http.MethodPost, "/api/admin/catalog", map[string]any{
		"name": "管理员先加的", "url": "https://example.com/race.atom", "category_id": cat,
	}, http.StatusCreated)
	out := admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(conflictID)+"/approve", map[string]any{}, http.StatusConflict)
	if code := catalogErrorCode(out); code != "conflict_url" {
		t.Fatalf("want conflict_url, got %v", out)
	}

	// Deleting the target of an update proposal leaves its entry_id NULL.
	gone := catalogSubmit(alice, "update", &entryID, "目标将删除",
		"https://example.com/updated.atom", cat, http.StatusCreated)
	goneID := catalogID(gone, "id")
	admin.do(http.MethodDelete, "/api/admin/catalog/"+itoa(entryID), nil, http.StatusOK)
	out = admin.do(http.MethodPost, "/api/admin/catalog/submissions/"+itoa(goneID)+"/approve", map[string]any{}, http.StatusConflict)
	if code := catalogErrorCode(out); code != "entry_gone" {
		t.Fatalf("want entry_gone, got %v", out)
	}
}

func TestCatalogWithdrawSubmission(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	alice := login(t, ts, "alice", "alice-pass")
	created := catalogSubmit(alice, "new", nil, "可撤回", "https://example.com/withdraw.atom", cat, http.StatusCreated)
	subID := catalogID(created, "id")
	alice.do(http.MethodDelete, "/api/catalog/submissions/"+itoa(subID), nil, http.StatusOK)
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM submissions WHERE status='withdrawn'`); got != 1 {
		t.Fatalf("withdrawn rows = %d, want 1", got)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM submissions WHERE status='pending'`); got != 0 {
		t.Fatalf("pending rows after withdraw = %d", got)
	}
	// The partial index releases the URL, so the author may try again.
	catalogSubmit(alice, "new", nil, "重新提交", "https://example.com/withdraw.atom", cat, http.StatusCreated)
}

func TestDeleteCategoryReassignsToUncategorized(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "待删除")
	entryA, _ := insertCatalogEntry(t, db, "A", "https://example.com/a.atom", cat)
	entryB, _ := insertCatalogEntry(t, db, "B", "https://example.com/b.atom", cat)
	alice := login(t, ts, "alice", "alice-pass")
	admin := login(t, ts, "admin", "admin-pass")
	catalogSubmit(alice, "new", nil, "待审", "https://example.com/c.atom", cat, http.StatusCreated)

	out := admin.do(http.MethodDelete, "/api/admin/catalog/categories/"+itoa(cat), nil, http.StatusOK)
	if int(out["reassigned"].(float64)) != 3 {
		t.Fatalf("reassigned = %v, want 3", out)
	}
	for _, id := range []int64{entryA, entryB} {
		var categoryID sql.NullInt64
		if err := db.QueryRow(`SELECT category_id FROM catalog_entries WHERE id=?`, id).Scan(&categoryID); err != nil {
			t.Fatal(err)
		}
		if categoryID.Valid {
			t.Fatalf("entry %d still has a category", id)
		}
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM submissions WHERE category_id IS NOT NULL`); got != 0 {
		t.Fatalf("proposal still references the deleted category: %d", got)
	}
	list := alice.do(http.MethodGet, "/api/catalog", nil, http.StatusOK)
	for _, raw := range catalogItems(list, "entries") {
		if raw.(map[string]any)["category_name"] != uncategorizedLabel {
			t.Fatalf("entry is not labelled uncategorized: %+v", raw)
		}
	}
}

func TestAdminCatalogEditsSkipReview(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	admin := login(t, ts, "admin", "admin-pass")
	alice := login(t, ts, "alice", "alice-pass")

	created := admin.do(http.MethodPost, "/api/admin/catalog", map[string]any{
		"name": "管理员添加", "url": "https://example.com/admin.atom", "category_id": cat,
	}, http.StatusCreated)
	entryID := catalogID(created, "id")
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM submissions`); got != 0 {
		t.Fatalf("admin edit created %d review rows", got)
	}
	if len(catalogItems(alice.do(http.MethodGet, "/api/catalog", nil, http.StatusOK), "entries")) != 1 {
		t.Fatal("admin-created entry should be immediately visible")
	}
	admin.do(http.MethodPatch, "/api/admin/catalog/"+itoa(entryID),
		map[string]any{"name": "改过的名字", "enabled": false}, http.StatusOK)
	if len(catalogItems(alice.do(http.MethodGet, "/api/catalog", nil, http.StatusOK), "entries")) != 0 {
		t.Fatal("disabled entry must disappear from the public catalog")
	}
	adminList := admin.do(http.MethodGet, "/api/admin/catalog", nil, http.StatusOK)
	if len(catalogItems(adminList, "entries")) != 1 {
		t.Fatal("admin listing must include disabled entries")
	}
	alice.do(http.MethodPost, "/api/catalog/"+itoa(entryID)+"/subscribe", map[string]any{}, http.StatusConflict)
	admin.do(http.MethodDelete, "/api/admin/catalog/"+itoa(entryID), nil, http.StatusOK)
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM catalog_entries`); got != 0 {
		t.Fatalf("catalog entries after delete = %d", got)
	}
}

func TestCatalogImportExport(t *testing.T) {
	ts, db := newTestServer(t)
	cat := insertCatalogCategory(t, db, "工具")
	insertCatalogEntry(t, db, "可导出", "https://example.com/export.atom", cat)
	admin := login(t, ts, "admin", "admin-pass")

	doc := admin.do(http.MethodGet, "/api/admin/catalog/export", nil, http.StatusOK)
	if doc["format"] != webui.CatalogDocumentFormat || len(catalogItems(doc, "entries")) != 1 {
		t.Fatalf("unexpected export: %+v", doc)
	}
	mustExec(t, db, `DELETE FROM catalog_entries`)
	mustExec(t, db, `DELETE FROM catalog_categories`)

	doc["strategy"] = webui.ImportStrategySkip
	out := admin.do(http.MethodPost, "/api/admin/catalog/import", doc, http.StatusOK)
	if out["created"].(float64) != 1 {
		t.Fatalf("import created = %v, want 1", out)
	}
	out = admin.do(http.MethodPost, "/api/admin/catalog/import", doc, http.StatusOK)
	if out["skipped"].(float64) != 1 {
		t.Fatalf("second import skipped = %v, want 1", out)
	}
	doc["strategy"] = webui.ImportStrategyOverwrite
	out = admin.do(http.MethodPost, "/api/admin/catalog/import", doc, http.StatusOK)
	if out["updated"].(float64) != 1 {
		t.Fatalf("overwrite updated = %v, want 1", out)
	}
	doc["strategy"] = webui.ImportStrategyError
	out = admin.do(http.MethodPost, "/api/admin/catalog/import", doc, http.StatusConflict)
	if code := catalogErrorCode(out); code != "conflict" {
		t.Fatalf("error strategy: want conflict, got %v", out)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM catalog_entries`); got != 1 {
		t.Fatalf("conflicting import left %d rows behind", got)
	}

	bad := map[string]any{
		"format": webui.CatalogDocumentFormat, "version": webui.CatalogDocumentV1,
		"categories": []any{}, "entries": []any{
			map[string]any{"name": "坏链接", "url": "ftp://example.com/a.atom", "enabled": true},
		},
	}
	out = admin.do(http.MethodPost, "/api/admin/catalog/import", bad, http.StatusBadRequest)
	if code := catalogErrorCode(out); code != "invalid_import" {
		t.Fatalf("bad URL: want invalid_import, got %v", out)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM catalog_entries`); got != 1 {
		t.Fatalf("failed import changed the catalog: %d", got)
	}
	// Import is metadata only: it must never create a real subscription.
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM subscriptions`); got != 0 {
		t.Fatalf("import created %d subscriptions", got)
	}
	if got := catalogCount(t, db, `SELECT COUNT(*) FROM feed_pool`); got != 0 {
		t.Fatalf("import created %d pool rows", got)
	}
}
