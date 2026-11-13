//go:build webui

package webui

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"feedwatch/internal/config"
	"feedwatch/internal/webui/store"
	"golang.org/x/crypto/bcrypt"
)

// SeedResult reports what the first-start import did. Password is non-empty
// only when an administrator was actually generated.
type SeedResult struct {
	Created      bool
	Username     string
	Password     string
	Feeds        int
	Channels     int
	BaselineKind string
}

// Seed imports config.yaml's feeds/channels into SQLite exactly once, when the
// users table is empty. From then on the database is the only source of truth;
// users added later subscribe silently with a fresh baseline instead of
// replaying history. The single baseline notification is materialized here and
// delivered by the orchestrator's pending-delivery loop.
func Seed(st *store.Store, cfg config.Config, log *slog.Logger) (SeedResult, error) {
	if log == nil {
		log = slog.Default()
	}
	var users int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		return SeedResult{}, fmt.Errorf("webui: count users: %w", err)
	}
	if users > 0 {
		return SeedResult{}, nil
	}

	password, err := randomToken(24)
	if err != nil {
		return SeedResult{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return SeedResult{}, fmt.Errorf("webui: hash admin password: %w", err)
	}

	tx, err := st.DB().Begin()
	if err != nil {
		return SeedResult{}, fmt.Errorf("webui: begin seed: %w", err)
	}
	defer tx.Rollback()

	now := store.Now()
	res, err := tx.Exec(`INSERT INTO users
		(username, password_hash, role, is_active, must_change_password, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`, "admin", string(hash), "admin", 1, 1, now, now)
	if err != nil {
		return SeedResult{}, fmt.Errorf("webui: create admin: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return SeedResult{}, fmt.Errorf("webui: admin id: %w", err)
	}

	feedCount, err := seedFeeds(tx, userID, cfg, now, log)
	if err != nil {
		return SeedResult{}, err
	}
	channelIDs, channelTypes, err := seedChannels(tx, userID, cfg, log)
	if err != nil {
		return SeedResult{}, err
	}
	if err := seedSettings(tx, cfg, now); err != nil {
		return SeedResult{}, err
	}

	cycleID := "seed-" + mustToken(8)
	if err := seedBaseline(tx, cycleID, userID, cfg, feedCount, channelIDs, channelTypes, now); err != nil {
		return SeedResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SeedResult{}, fmt.Errorf("webui: commit seed: %w", err)
	}

	// Printed exactly once, ever. The admin is forced to change it on first login.
	log.Info("webui: initial administrator created (shown once)",
		"username", "admin", "password", password,
		"note", "change this password at first login")
	return SeedResult{
		Created: true, Username: "admin", Password: password,
		Feeds: feedCount, Channels: len(channelIDs), BaselineKind: "baseline",
	}, nil
}

func seedFeeds(tx *sql.Tx, userID int64, cfg config.Config, now string, log *slog.Logger) (int, error) {
	mode := cfg.Notify.Mode
	if mode == "" {
		mode = "digest"
	}
	count := 0
	for _, f := range cfg.Feeds {
		if !f.Enabled {
			continue
		}
		normalized, key, err := URLKey(f.URL)
		if err != nil {
			log.Warn("webui: skipping invalid seed feed", "name", f.Name, "error", err)
			continue
		}
		filterJSON, err := json.Marshal(map[string]any{
			"stable_only":     f.Filter.StableOnly,
			"include":         f.Filter.Include,
			"exclude":         f.Filter.Exclude,
			"version_pattern": f.Filter.VersionPattern,
		})
		if err != nil {
			return 0, fmt.Errorf("webui: encode filter for %q: %w", f.Name, err)
		}
		sourceType := f.Type
		if sourceType == "" {
			sourceType = "atom"
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO subscriptions
			(user_id, url, url_key, display_name, source_type, filter_json, notify_policy, mode, enabled, seeded_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			userID, normalized, key, f.Name, sourceType, string(filterJSON), "on_update", mode, 1, now,
		); err != nil {
			return 0, fmt.Errorf("webui: seed feed %q: %w", f.Name, err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO feed_pool (url_key, url, source_type) VALUES (?,?,?)`,
			key, normalized, sourceType,
		); err != nil {
			return 0, fmt.Errorf("webui: seed feed pool %q: %w", f.Name, err)
		}
		count++
	}
	return count, nil
}

func seedChannels(tx *sql.Tx, userID int64, cfg config.Config, log *slog.Logger) ([]int64, map[int64]string, error) {
	ids := make([]int64, 0, len(cfg.Notify.Channels))
	types := make(map[int64]string)
	for _, ch := range cfg.Notify.Channels {
		if !ch.Enabled {
			continue
		}
		spec := ChannelSpec{
			Type:          ch.Type,
			Name:          ch.Name,
			From:          ch.Email.From,
			To:            ch.Email.To,
			Cc:            ch.Email.Cc,
			APIKeyEnv:     ch.Email.APIKeyEnv,
			SubjectPrefix: cfg.Notify.SubjectPrefix,
			Options: sanitizeOptions(ch.Options, func(key string) {
				log.Warn("webui: dropping plaintext secret from seed channel; use ${ENV_VAR} instead",
					"channel", ch.Type, "option", key)
			}),
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return nil, nil, fmt.Errorf("webui: encode channel %q: %w", ch.Type, err)
		}
		res, err := tx.Exec(`INSERT INTO channels (user_id, type, name, config_json, enabled) VALUES (?,?,?,?,?)`,
			userID, ch.Type, ch.Name, string(raw), 1)
		if err != nil {
			return nil, nil, fmt.Errorf("webui: seed channel %q: %w", ch.Type, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, nil, fmt.Errorf("webui: channel id: %w", err)
		}
		ids = append(ids, id)
		types[id] = ch.Type
	}
	return ids, types, nil
}

func seedSettings(tx *sql.Tx, cfg config.Config, now string) error {
	settings := map[string]string{
		"poll.interval":         cfg.Poll.Interval.String(),
		"poll.timeout":          cfg.Poll.Timeout.String(),
		"poll.concurrency":      strconv.Itoa(cfg.Poll.Concurrency),
		"poll.request_spacing":  cfg.Poll.RequestSpacing.String(),
		"notify.mode":           cfg.Notify.Mode,
		"notify.send_spacing":   cfg.Notify.SendSpacing.String(),
		"notify.subject_prefix": cfg.Notify.SubjectPrefix,
		"http.timeout":          cfg.HTTP.Timeout.String(),
		"http.user_agent":       cfg.HTTP.UserAgent,
		"http.retry_max":        strconv.Itoa(cfg.HTTP.RetryMax),
		"http.retry_base_delay": cfg.HTTP.RetryBaseDelay.String(),
		"http.conditional":      strconv.FormatBool(cfg.HTTP.Conditional),
		"webui.cookie_secure":   "false",
		"webui.session_ttl":     "168h",
	}
	for k, v := range settings {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO settings (key, value, updated_at) VALUES (?,?,?)`, k, v, now); err != nil {
			return fmt.Errorf("webui: seed setting %q: %w", k, err)
		}
	}
	return nil
}

// seedBaseline materializes one summary notification plus one pending delivery
// group per channel. It does not touch the network: the orchestrator drains
// pending groups and retries them independently of feed fetches.
func seedBaseline(tx *sql.Tx, cycleID string, userID int64, cfg config.Config, feedCount int,
	channelIDs []int64, channelTypes map[int64]string, now string) error {

	prefix := cfg.Notify.SubjectPrefix
	if prefix == "" {
		prefix = config.DefaultSubjectPrefix
	}
	subject := prefix + " WebUI 初始化基线"
	text := fmt.Sprintf(
		"FeedWatch WebUI 已初始化，导入 %d 个订阅、%d 个通知渠道。\n这是首轮基线汇总，之后只会在检测到新稳定版时推送。",
		feedCount, len(channelIDs))
	if _, err := tx.Exec(`INSERT INTO notifications
		(cycle_id, merge_key, kind, subject, "text", html, markdown, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, cycleID, "seed-baseline", "baseline", subject, text, "", "", now,
	); err != nil {
		return fmt.Errorf("webui: seed baseline notification: %w", err)
	}
	for _, id := range channelIDs {
		groupID := fmt.Sprintf("seed-%d-%s", id, mustToken(6))
		if _, err := tx.Exec(`INSERT INTO delivery_groups
			(group_id, cycle_id, merge_key, channel_type, idempotency_key, status, attempts, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			groupID, cycleID, "seed-baseline", channelTypes[id], cycleID+":"+groupID, "pending", 0, now,
		); err != nil {
			return fmt.Errorf("webui: seed baseline group: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO deliveries
			(cycle_id, group_id, user_id, channel_id, kind, status, attempts)
			VALUES (?,?,?,?,?,?,?)`, cycleID, groupID, userID, id, "baseline", "pending", 0,
		); err != nil {
			return fmt.Errorf("webui: seed baseline delivery: %w", err)
		}
	}
	return nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webui: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func mustToken(n int) string {
	s, err := randomToken(n)
	if err != nil {
		panic(err)
	}
	return strings.ReplaceAll(s, "-", "0")
}
