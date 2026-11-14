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

	"feedwatch/internal/webui"
	"feedwatch/internal/webui/store"
)

// Only keys that the running process actually reads are editable. Three seeded
// keys are deliberately excluded: webui.cookie_secure / webui.session_ttl come
// from the environment (changing Secure over plain HTTP would lock everyone
// out), and notify.mode is a per-subscription policy in the multi-tenant
// orchestrator, not a global switch.
//
// summary says what the knob does (one short line for the table), detail
// explains the trade-off and what it interacts with (shown on demand), and def
// is the built-in default, asserted against config.Default in the tests so the
// page can never advertise a value the process would not actually use.
type settingRule struct {
	kind    string
	min     int64
	max     int64
	maxLen  int
	summary string
	def     string
	detail  string
}

var editableSettings = map[string]settingRule{
	"poll.interval": {
		kind: "duration", min: int64(time.Second), max: int64(7 * 24 * time.Hour),
		summary: "多久跑一轮抓取", def: "30m",
		detail: "调度器两次抓取周期之间的间隔。每轮会把所有订阅去重后的 URL 各抓一次；调小能更快发现新版本，同时增加对上游站点的请求量。它应当明显大于「单轮超时」，否则上一轮还没结束下一轮就开始了。",
	},
	"poll.timeout": {
		kind: "duration", min: int64(time.Second), max: int64(time.Hour),
		summary: "一轮抓取的总时限", def: "2m",
		detail: "整轮抓取（含并发与限速等待）的最长执行时间。超时后本轮没跑完的订阅会被跳过、留到下一轮，已经抓到的结果照常入库与投递。它不是单个请求的超时——那个由 http.timeout 管。",
	},
	"poll.concurrency": {
		kind: "int", min: 1, max: 64,
		summary: "同时抓取多少个 URL", def: "4",
		detail: "一轮内并行抓取的 URL 数量。调大能缩短一轮耗时，但会同时占用更多上游连接；「抓取间隔」是全局闸门，并发调得再高也不会突破它。",
	},
	"poll.request_spacing": {
		kind: "duration", min: 0, max: int64(time.Minute),
		summary: "两次抓取之间的最小间隔", def: "100ms",
		detail: "任意两次抓取请求发起之间的最小间隔，全局生效、不按站点分组；100ms 大致等于每秒 10 个请求。调大能降低被上游限流或封禁的概率，但如果「订阅数 × 间隔」超过「单轮超时」，一轮就会抓不完。0s 表示不额外限速。",
	},
	"notify.send_spacing": {
		kind: "duration", min: 0, max: int64(time.Minute),
		summary: "同一渠道两次发送的间隔", def: "250ms",
		detail: "每种渠道类型各自有一个独立的闸门（email / telegram / webhook …），不同类型互不阻塞。用来避开服务商的速率限制；只影响发送节奏，不影响抓取。",
	},
	"notify.subject_prefix": {
		kind: "text", maxLen: 120,
		summary: "通知标题前面的固定文案", def: "[FeedWatch]",
		detail: "拼在每条通知标题最前面，通常写项目名或 [FW]，方便邮件客户端过滤。留空时回落到 [FeedWatch]。只影响之后生成的通知，已生成待投递的投递组不会改变。",
	},
	"http.timeout": {
		kind: "duration", min: int64(time.Second), max: int64(5 * time.Minute),
		summary: "单个 HTTP 请求的超时", def: "15s",
		detail: "每个出站请求独立的超时，抓 feed 和发通知都适用，与轮询总超时无关。上游站点慢时可以调大；调得过大时，一个卡死的请求会长期占住一个并发槽。",
	},
	"http.user_agent": {
		kind: "text", maxLen: 200,
		summary: "抓取请求的 User-Agent", def: "feedwatch/1.0 (+https://github.com/zsyo/feedwatch)",
		detail: "所有抓取请求携带的 UA。部分站点（GitHub API、Cloudflare 后面的博客）会按 UA 限流或直接返回 403，写上自己的联系方式更容易被放行。",
	},
	"http.retry_max": {
		kind: "int", min: 0, max: 10,
		summary: "单个请求最多重试几次", def: "3",
		detail: "遇到 5xx、429 与网络错误时的最大重试次数，0 表示不重试；其他 4xx 一律不重试。调大能熬过上游抖动，但会拖长一轮的耗时。",
	},
	"http.retry_base_delay": {
		kind: "duration", min: 0, max: int64(time.Minute),
		summary: "重试的起始等待时间", def: "500ms",
		detail: "第 n 次重试前等待 base × 2ⁿ⁻¹，单次上限 30s；上游返回 Retry-After 时按它等（单次不超过 60s）。500ms 大致是 0.5s / 1s / 2s。",
	},
	"http.conditional": {
		kind:    "bool",
		summary: "用 ETag / 304 减少重复下载", def: "true",
		detail: "开启后复用上次的 ETag 与 Last-Modified，内容没变时上游返回 304，既省流量也省时间。只有在怀疑上游对条件请求返回了错误内容时才需要关掉。",
	},
}

var envOnlySettings = []string{"webui.cookie_secure", "webui.session_ttl"}
var informationalSettings = []string{"notify.mode"}

// readonlyDocs covers the three keys the UI must not edit (see the comment on
// editableSettings). They still deserve a purpose line of their own.
var readonlyDocs = map[string]settingDoc{
	"webui.cookie_secure": {
		Summary: "会话 Cookie 是否只在 HTTPS 下发送",
		Format:  "由环境变量 FEEDWATCH_WEBUI_COOKIE_SECURE 决定",
		Detail:  "必须在启动时确定：纯 HTTP 直连误开该选项会让浏览器不再回传 Cookie，结果是所有人都登录不上，所以不提供页面开关。经 HTTPS 反向代理时应设为 true。",
	},
	"webui.session_ttl": {
		Summary: "登录会话的有效期",
		Format:  "由环境变量 FEEDWATCH_WEBUI_SESSION_TTL 决定，默认 168h",
		Detail:  "超过这个时长没有任何操作就需要重新登录；已登录会话每被访问一次都会顺延。修改密码会让全部会话立即失效。",
	},
	"notify.mode": {
		Summary: "每条订阅自己的通知合并策略",
		Format:  "digest（合并成一条）或 per_feed（每个订阅一条），在订阅里设置",
		Detail:  "多租户模式下这不是全局开关：每个订阅各自选择 digest 还是 per_feed，所以它在这里只作展示。要改就去「我的订阅」里改对应订阅。",
	},
}

const settingsApplyNote = "设置写入数据库后在下次进程启动时由组合根读取，当前轮询/投递周期不会热更新。"

// settingDoc is what the settings page renders: a one-line purpose next to the
// key, the accepted format under the input, and the long explanation behind a
// per-row toggle. Keeping the long text out of the table stops the layout from
// wrapping when a description runs to three sentences.
type settingDoc struct {
	Summary string `json:"summary"`
	Format  string `json:"format"`
	Default string `json:"default,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func settingFormat(rule settingRule) string {
	switch rule.kind {
	case "duration":
		return fmt.Sprintf("时长，如 30m / 250ms；范围 %s ~ %s",
			webui.CompactDuration(time.Duration(rule.min)), webui.CompactDuration(time.Duration(rule.max)))
	case "int":
		return fmt.Sprintf("整数；范围 %d ~ %d", rule.min, rule.max)
	case "bool":
		return "true 或 false"
	default:
		return fmt.Sprintf("文本，最长 %d 字符", rule.maxLen)
	}
}

func settingDocs() map[string]settingDoc {
	out := make(map[string]settingDoc, len(editableSettings)+len(readonlyDocs))
	for k, rule := range editableSettings {
		out[k] = settingDoc{
			Summary: rule.summary,
			Format:  settingFormat(rule),
			Default: rule.def,
			Detail:  rule.detail,
		}
	}
	for k, doc := range readonlyDocs {
		out[k] = doc
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
		"docs":          settingDocs(),
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
