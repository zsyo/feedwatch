//go:build webui

package api

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"feedwatch/internal/webui/store"
)

// Only keys that the running process actually reads are editable. Three seeded
// keys are deliberately excluded: webui.cookie_secure / webui.session_ttl come
// from the environment (changing Secure over plain HTTP would lock everyone
// out), and notify.mode is a per-subscription policy in the multi-tenant
// orchestrator, not a global switch.
type settingRule struct {
	kind   string
	min    int64
	max    int64
	maxLen int
}

var editableSettings = map[string]settingRule{
	"poll.interval":         {kind: "duration", min: int64(time.Second), max: int64(7 * 24 * time.Hour)},
	"poll.timeout":          {kind: "duration", min: int64(time.Second), max: int64(time.Hour)},
	"poll.concurrency":      {kind: "int", min: 1, max: 64},
	"poll.request_spacing":  {kind: "duration", min: 0, max: int64(time.Minute)},
	"notify.send_spacing":   {kind: "duration", min: 0, max: int64(time.Minute)},
	"notify.subject_prefix": {kind: "text", maxLen: 120},
	"http.timeout":          {kind: "duration", min: int64(time.Second), max: int64(5 * time.Minute)},
	"http.user_agent":       {kind: "text", maxLen: 200},
	"http.retry_max":        {kind: "int", min: 0, max: 10},
	"http.retry_base_delay": {kind: "duration", min: 0, max: int64(time.Minute)},
	"http.conditional":      {kind: "bool"},
}

var envOnlySettings = []string{"webui.cookie_secure", "webui.session_ttl"}
var informationalSettings = []string{"notify.mode"}

const settingsApplyNote = "设置写入数据库后在下次进程启动时由组合根读取，当前轮询/投递周期不会热更新。"

func settingHint(rule settingRule) string {
	switch rule.kind {
	case "duration":
		return fmt.Sprintf("Go duration (例如 30m / 250ms)，范围 %s ~ %s",
			time.Duration(rule.min).String(), time.Duration(rule.max).String())
	case "int":
		return fmt.Sprintf("整数，范围 %d ~ %d", rule.min, rule.max)
	case "bool":
		return "true 或 false"
	default:
		return fmt.Sprintf("文本，最长 %d 字符，不能包含换行", rule.maxLen)
	}
}

func editableHints() map[string]string {
	out := make(map[string]string, len(editableSettings))
	for k, rule := range editableSettings {
		out[k] = settingHint(rule)
	}
	return out
}

func validateSetting(key, value string) error {
	rule, ok := editableSettings[key]
	if !ok {
		return fmt.Errorf("setting %q is read-only or unknown", key)
	}
	switch rule.kind {
	case "duration":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("%s 必须是 Go duration（例如 30m），收到 %q", key, value)
		}
		if int64(d) < rule.min || int64(d) > rule.max {
			return fmt.Errorf("%s 超出范围 %s ~ %s", key,
				time.Duration(rule.min).String(), time.Duration(rule.max).String())
		}
	case "int":
		n, err := strconv.Atoi(value)
		if err != nil || int64(n) < rule.min || int64(n) > rule.max {
			return fmt.Errorf("%s 必须是 %d ~ %d 之间的整数", key, rule.min, rule.max)
		}
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s 必须是 true 或 false", key)
		}
	case "text":
		if len(value) > rule.maxLen {
			return fmt.Errorf("%s 最长 %d 字符", key, rule.maxLen)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s 不能包含换行或控制字符", key)
		}
	}
	return nil
}

// adminOverview is the dashboard summary. Counts are additive so an older
// frontend keeps working; recent cycles and delivery status are the extra
// operational detail the management pages need.
func (s *Server) adminOverview(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	counts := make(map[string]int, 11)
	for key, query := range map[string]string{
		"users":           `SELECT COUNT(*) FROM users`,
		"sessions":        `SELECT COUNT(*) FROM sessions`,
		"subscriptions":   `SELECT COUNT(*) FROM subscriptions WHERE deleted_at IS NULL`,
		"channels":        `SELECT COUNT(*) FROM channels`,
		"feed_pool":       `SELECT COUNT(*) FROM feed_pool`,
		"notifications":   `SELECT COUNT(*) FROM notifications`,
		"cycles":          `SELECT COUNT(*) FROM cycles`,
		"audit_events":    `SELECT COUNT(*) FROM audit_log`,
		"pending":         `SELECT COUNT(*) FROM delivery_groups WHERE status IN ('pending','retry')`,
		"delivery_sent":   `SELECT COUNT(*) FROM delivery_groups WHERE status='sent'`,
		"delivery_failed": `SELECT COUNT(*) FROM delivery_groups WHERE status='abandoned'`,
	} {
		var n int
		if err := s.db.QueryRow(query).Scan(&n); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		counts[key] = n
	}

	deliveryStatus := make(map[string]int)
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM delivery_groups GROUP BY status`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		deliveryStatus[status] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	rows.Close()

	cycles, err := s.recentCycles(5)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"counts":          counts,
		"delivery_status": deliveryStatus,
		"recent_cycles":   cycles,
	})
}

type cycleRow struct {
	ID         string `json:"id"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	URLs       int    `json:"urls"`
	Sent       int    `json:"sent"`
	Failed     int    `json:"failed"`
}

func (s *Server) recentCycles(limit int) ([]cycleRow, error) {
	rows, err := s.db.Query(`SELECT id, started_at, COALESCE(finished_at,''), urls, sent, failed
		FROM cycles ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]cycleRow, 0)
	for rows.Next() {
		var c cycleRow
		if err := rows.Scan(&c.ID, &c.StartedAt, &c.FinishedAt, &c.URLs, &c.Sent, &c.Failed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type poolRow struct {
	URLKey       string `json:"url_key"`
	URL          string `json:"url"`
	SourceType   string `json:"source_type"`
	HasETag      bool   `json:"has_etag"`
	HasModified  bool   `json:"has_last_modified"`
	LastChecked  string `json:"last_checked"`
	LastVersion  string `json:"last_version"`
	Subscribers  int    `json:"subscribers"`
	Users        int    `json:"users"`
	SeenReleases int    `json:"seen_releases"`
}

// maskFeedURL keeps the host/path useful for troubleshooting while removing
// credentials that may be embedded in userinfo or query parameters. Feed URLs
// are user supplied, so they must never be echoed verbatim to an admin page.
func maskFeedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparsable url]"
	}
	// "REDACTED" is used instead of "***" so url.URL keeps it readable
	// (asterisks would be percent-encoded as %2A).
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			lower := strings.ToLower(k)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "key") || strings.Contains(lower, "password") ||
				strings.Contains(lower, "auth") || strings.Contains(lower, "sig") {
				q.Set(k, "REDACTED")
			}
		}
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	return u.String()
}

func (s *Server) adminPool(w http.ResponseWriter, r *http.Request, _ Ctx) {
	limit := queryInt(r, "limit", 100, 1, 500)
	rows, err := s.db.Query(`SELECT p.url_key, p.url, p.source_type,
			(p.etag <> ''), (p.last_modified <> ''),
			COALESCE(p.last_checked, ''), p.last_version,
			(SELECT COUNT(*) FROM subscriptions s
			   WHERE s.url_key = p.url_key AND s.deleted_at IS NULL AND s.enabled = 1),
			(SELECT COUNT(DISTINCT s.user_id) FROM subscriptions s
			   WHERE s.url_key = p.url_key AND s.deleted_at IS NULL),
			(SELECT COUNT(*) FROM seen_ids si WHERE si.url_key = p.url_key)
		FROM feed_pool p ORDER BY p.url LIMIT ?`, limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]poolRow, 0)
	for rows.Next() {
		var item poolRow
		var hasETag, hasModified int
		if err := rows.Scan(&item.URLKey, &item.URL, &item.SourceType, &hasETag, &hasModified,
			&item.LastChecked, &item.LastVersion, &item.Subscribers, &item.Users, &item.SeenReleases); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		item.HasETag, item.HasModified = hasETag != 0, hasModified != 0
		item.URL = maskFeedURL(item.URL)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pool": out, "limit": limit})
}

func (s *Server) adminRateLimit(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	settings, err := s.settingsMap()
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	byChannel := make(map[string]map[string]int)
	rows, err := s.db.Query(`SELECT channel_type, status, COUNT(*) FROM delivery_groups GROUP BY channel_type, status`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	for rows.Next() {
		var typ, status string
		var n int
		if err := rows.Scan(&typ, &status, &n); err != nil {
			rows.Close()
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		if byChannel[typ] == nil {
			byChannel[typ] = make(map[string]int)
		}
		byChannel[typ][status] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	rows.Close()

	buckets := s.limiter.snapshot(time.Now())
	masked := make([]map[string]any, 0, len(buckets))
	for _, b := range buckets {
		masked = append(masked, map[string]any{
			"key": maskBucketKey(b.Key), "count": b.Count, "reset_at": b.ResetAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"poll": map[string]string{
			"interval":        settings["poll.interval"],
			"timeout":         settings["poll.timeout"],
			"concurrency":     settings["poll.concurrency"],
			"request_spacing": settings["poll.request_spacing"],
		},
		"notify": map[string]any{
			"send_spacing":     settings["notify.send_spacing"],
			"gate_granularity": "channel_type",
			"note":             "每种渠道类型一个独立限速门；不同服务互不阻塞，不同用户的通知各自独立请求。",
		},
		"http_limiter": map[string]any{
			"login_per_minute":  10,
			"writes_per_minute": 60,
			"active_buckets":    len(masked),
			"buckets":           masked,
		},
		"delivery_groups_by_channel": byChannel,
		"applies":                    "restart",
	})
}

// maskBucketKey hides the client address half of "ip|path" so the rate-limit
// page can show hot endpoints without doubling as an IP log.
func maskBucketKey(key string) string {
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return key
	}
	return "***|" + parts[1]
}

type auditRow struct {
	ID            int64  `json:"id"`
	TS            string `json:"ts"`
	ActorID       int64  `json:"actor_id"`
	ActorUsername string `json:"actor_username"`
	Action        string `json:"action"`
	Target        string `json:"target"`
	DetailJSON    string `json:"detail_json"`
	IP            string `json:"ip"`
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request, _ Ctx) {
	limit := queryInt(r, "limit", 50, 1, 200)
	offset := queryInt(r, "offset", 0, 0, 1_000_000)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&total); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	rows, err := s.db.Query(`SELECT a.id, a.ts, COALESCE(a.actor_id, 0), COALESCE(u.username, ''),
			a.action, a.target, a.detail_json, a.ip
		FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id
		ORDER BY a.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]auditRow, 0)
	for rows.Next() {
		var item auditRow
		if err := rows.Scan(&item.ID, &item.TS, &item.ActorID, &item.ActorUsername,
			&item.Action, &item.Target, &item.DetailJSON, &item.IP); err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": out, "total": total, "limit": limit, "offset": offset,
	})
}

func (s *Server) settingsMap() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Server) adminGetSettings(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	writeJSON(w, http.StatusOK, s.settingsResponse())
}

func (s *Server) settingsResponse() map[string]any {
	rows := make([]map[string]any, 0)
	if result, err := s.db.Query(`SELECT key, value, updated_at FROM settings ORDER BY key`); err == nil {
		for result.Next() {
			var key, value, updated string
			if result.Scan(&key, &value, &updated) == nil {
				_, editable := editableSettings[key]
				rows = append(rows, map[string]any{
					"key": key, "value": value, "updated_at": updated, "editable": editable,
				})
			}
		}
		result.Close()
	}
	return map[string]any{
		"settings":      rows,
		"editable":      editableHints(),
		"env_only":      envOnlySettings,
		"informational": informationalSettings,
		"applies":       "restart",
		"note":          settingsApplyNote,
	}
}

func (s *Server) adminPutSettings(w http.ResponseWriter, r *http.Request, _ Ctx) {
	var req struct {
		Settings map[string]string `json:"settings"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(req.Settings) == 0 {
		fail(w, http.StatusBadRequest, "empty_patch", "settings must contain at least one key")
		return
	}
	keys := make([]string, 0, len(req.Settings))
	for k, v := range req.Settings {
		if err := validateSetting(k, v); err != nil {
			fail(w, http.StatusBadRequest, "invalid_setting", err.Error())
			return
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Validate-then-write in one transaction: a bad value must never leave a
	// half-updated settings table behind.
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	now := store.Now()
	for _, k := range keys {
		if _, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			k, req.Settings[k], now); err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse())
}

func queryInt(r *http.Request, key string, def, min, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return def
	}
	return n
}
