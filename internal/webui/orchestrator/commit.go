//go:build webui

package orchestrator

import (
	"database/sql"
	"fmt"

	"feedwatch/internal/source"
)

type Subscription struct {
	ID               int64
	UserID           int64
	URL              string
	URLKey           string
	DisplayName      string
	SourceType       string
	FilterJSON       string
	Policy           string
	Mode             string
	PoolETag         string
	PoolLastModified string
	PoolLastVersion  string
}

type ChannelRef struct {
	ID         int64
	UserID     int64
	Type       string
	ConfigJSON string
}

func loadSubscriptions(db *sql.DB) ([]Subscription, error) {
	rows, err := db.Query(`SELECT s.id, s.user_id, s.url, s.url_key, s.display_name, s.source_type,
		s.filter_json, s.notify_policy, s.mode,
		COALESCE(p.etag,''), COALESCE(p.last_modified,''), COALESCE(p.last_version,'')
		FROM subscriptions s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN feed_pool p ON p.url_key = s.url_key
		WHERE s.enabled=1 AND s.deleted_at IS NULL AND u.is_active=1
		ORDER BY s.url_key, s.user_id`)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: load subscriptions: %w", err)
	}
	defer rows.Close()
	out := make([]Subscription, 0)
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.UserID, &s.URL, &s.URLKey, &s.DisplayName, &s.SourceType,
			&s.FilterJSON, &s.Policy, &s.Mode, &s.PoolETag, &s.PoolLastModified, &s.PoolLastVersion); err != nil {
			return nil, fmt.Errorf("orchestrator: scan subscription: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadChannelIDs(db *sql.DB) (map[int64][]int64, error) {
	rows, err := db.Query(`SELECT id, user_id FROM channels WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: load channels: %w", err)
	}
	defer rows.Close()
	byUser := make(map[int64][]int64)
	for rows.Next() {
		var id, userID int64
		if err := rows.Scan(&id, &userID); err != nil {
			return nil, err
		}
		byUser[userID] = append(byUser[userID], id)
	}
	return byUser, rows.Err()
}

type seenSet map[string]map[string]struct{}

func loadGlobalSeen(db *sql.DB) (seenSet, error) {
	rows, err := db.Query(`SELECT url_key, release_id FROM seen_ids`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(seenSet)
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		if out[key] == nil {
			out[key] = make(map[string]struct{})
		}
		out[key][id] = struct{}{}
	}
	return out, rows.Err()
}

type userSeenSet map[int64]map[string]map[string]struct{}

func loadUserSeen(db *sql.DB) (userSeenSet, error) {
	rows, err := db.Query(`SELECT user_id, url_key, release_id FROM user_seen`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(userSeenSet)
	for rows.Next() {
		var userID int64
		var key, id string
		if err := rows.Scan(&userID, &key, &id); err != nil {
			return nil, err
		}
		if out[userID] == nil {
			out[userID] = make(map[string]map[string]struct{})
		}
		if out[userID][key] == nil {
			out[userID][key] = make(map[string]struct{})
		}
		out[userID][key][id] = struct{}{}
	}
	return out, rows.Err()
}

// commitFetch persists global fetch state in one transaction, completely
// independently of notification delivery. A broken user channel therefore
// cannot hold back seen IDs / ETags and cause duplicate pushes next cycle.
func commitFetch(tx *sql.Tx, now string, subs []Subscription, results map[string]source.Result,
	seen seenSet) error {

	byKey := make(map[string]Subscription)
	for _, s := range subs {
		byKey[s.URLKey] = s
	}
	for key, res := range results {
		s := byKey[key]
		if _, err := tx.Exec(`INSERT OR IGNORE INTO feed_pool (url_key, url, source_type) VALUES (?,?,?)`,
			key, s.URL, s.SourceType); err != nil {
			return fmt.Errorf("orchestrator: ensure feed_pool %q: %w", key, err)
		}
		if res.NotModified {
			if _, err := tx.Exec(`UPDATE feed_pool SET last_checked=? WHERE url_key=?`, now, key); err != nil {
				return err
			}
			continue
		}
		for _, r := range res.Releases {
			if r.ID == "" {
				continue
			}
			if _, ok := seen[key][r.ID]; ok {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO seen_ids (url_key, release_id, seen_at) VALUES (?,?,?)`,
				key, r.ID, now); err != nil {
				return fmt.Errorf("orchestrator: insert seen_id: %w", err)
			}
			if seen[key] == nil {
				seen[key] = make(map[string]struct{})
			}
			seen[key][r.ID] = struct{}{}
		}
		version := latestStable(res.Releases)
		if version == "" {
			version = s.PoolLastVersion
		}
		if _, err := tx.Exec(`UPDATE feed_pool SET etag=?, last_modified=?, last_checked=?, last_version=? WHERE url_key=?`,
			res.ETag, res.LastModified, now, version, key); err != nil {
			return fmt.Errorf("orchestrator: update feed_pool: %w", err)
		}
	}
	return nil
}

func insertUserSeen(tx *sql.Tx, userID int64, key string, ids []string, now string) error {
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_seen (user_id, url_key, release_id, seen_at) VALUES (?,?,?,?)`,
			userID, key, id, now); err != nil {
			return err
		}
	}
	return nil
}

// insertNotification stores a rendered notification with merge_key='pending'.
// The delivery planner claims pending rows and turns them into merged groups.
func insertNotification(tx *sql.Tx, cycleID string, r renderedNotification, now string) (int64, error) {
	res, err := tx.Exec(`INSERT INTO notifications
		(cycle_id, merge_key, kind, subject, "text", html, markdown, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, cycleID, "pending", r.Kind, r.Subject, r.Text, r.HTML, r.Markdown, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertCycle(tx *sql.Tx, cycleID, now string, urls, notifications int) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO cycles (id, started_at, urls, sent, failed) VALUES (?,?,?,?,?)`,
		cycleID, now, urls, notifications, 0)
	return err
}
