//go:build webui

package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"feedwatch/internal/webui/store"
)

type pendingNotification struct {
	id       int64
	cycleID  string
	kind     string
	markdown string
}

// PlanDeliveries turns materialized notifications into independent delivery
// groups: one network request per (notification, channel, user).
//
// There is deliberately NO cross-user merging. Email providers key sends by
// each user's own api_key, so a merged request would have to pick one person's
// key and would hide the send from the other users' provider dashboards.
func PlanDeliveries(ctx context.Context, db *sql.DB) (int, error) {
	// The store keeps a single SQLite connection, so all reads must be fully
	// drained before any write on that connection.
	rows, err := db.QueryContext(ctx, `SELECT id, cycle_id, kind, markdown
		FROM notifications WHERE merge_key='pending' ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("orchestrator: list pending notifications: %w", err)
	}
	pending := make([]pendingNotification, 0)
	for rows.Next() {
		var p pendingNotification
		if err := rows.Scan(&p.id, &p.cycleID, &p.kind, &p.markdown); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	planned := 0
	for _, p := range pending {
		var env Envelope
		if err := json.Unmarshal([]byte(p.markdown), &env); err != nil {
			return planned, fmt.Errorf("orchestrator: decode envelope for notification %d: %w", p.id, err)
		}
		channelTypes := make(map[int64]string, len(env.ChannelIDs))
		for _, id := range env.ChannelIDs {
			var typ string
			err := db.QueryRowContext(ctx, `SELECT type FROM channels WHERE id=? AND enabled=1`, id).Scan(&typ)
			if err == sql.ErrNoRows {
				continue // channel was deleted or disabled in the meantime
			}
			if err != nil {
				return planned, err
			}
			channelTypes[id] = typ
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return planned, err
		}
		kindWithID := p.kind + "#" + strconv.FormatInt(p.id, 10)
		now := store.Now()
		for _, channelID := range env.ChannelIDs {
			typ, ok := channelTypes[channelID]
			if !ok {
				continue
			}
			groupID := "grp-" + randomHex(8)
			if _, err := tx.Exec(`INSERT INTO delivery_groups
				(group_id, cycle_id, merge_key, channel_type, idempotency_key, status, attempts, created_at)
				VALUES (?,?,?,?,?,?,?,?)`,
				groupID, p.cycleID, typ, typ, p.cycleID+":"+groupID, "pending", 0, now,
			); err != nil {
				tx.Rollback()
				return planned, fmt.Errorf("orchestrator: create delivery group: %w", err)
			}
			if _, err := tx.Exec(`INSERT INTO deliveries
				(cycle_id, group_id, user_id, channel_id, kind, status, attempts)
				VALUES (?,?,?,?,?,?,?)`,
				p.cycleID, groupID, env.UserID, channelID, kindWithID, "pending", 0,
			); err != nil {
				tx.Rollback()
				return planned, fmt.Errorf("orchestrator: create delivery: %w", err)
			}
			planned++
		}
		if _, err := tx.Exec(`UPDATE notifications SET merge_key='planned' WHERE id=?`, p.id); err != nil {
			tx.Rollback()
			return planned, err
		}
		if err := tx.Commit(); err != nil {
			return planned, err
		}
	}
	return planned, nil
}
