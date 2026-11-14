//go:build webui

package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"feedwatch/internal/domain"
	"feedwatch/internal/notifier"
	emailnotify "feedwatch/internal/notifier/email"
	lognotify "feedwatch/internal/notifier/log"
	webhooknotify "feedwatch/internal/notifier/webhook"
	"feedwatch/internal/pace"
	"feedwatch/internal/webui"
	"feedwatch/internal/webui/secrets"
	"feedwatch/internal/webui/store"
)

const (
	maxDeliveryAttempts = 5
	deliveryTTL         = 24 * time.Hour
)

// DeliveryWorker drains pending delivery groups. Each group is exactly ONE
// network request for ONE user's channel, so no user's api_key or provider
// dashboard is shared with anybody else.
type DeliveryWorker struct {
	db          *sql.DB
	reg         *notifier.Registry
	sealer      *secrets.Sealer
	spacing     time.Duration
	gates       map[string]*pace.Gate
	mu          sync.Mutex
	log         *slog.Logger
	now         func() time.Time
	maxAttempts int
}

func NewDeliveryWorker(st *store.Store, log *slog.Logger, spacing time.Duration, sealer *secrets.Sealer) *DeliveryWorker {
	if log == nil {
		log = slog.Default()
	}
	reg := notifier.NewRegistry()
	reg.Register("email", emailnotify.New)
	reg.Register("log", lognotify.New)
	for typ, a := range webhooknotify.Adapters() {
		reg.Register(typ, webhooknotify.Factory(a))
	}
	return &DeliveryWorker{
		db: st.DB(), reg: reg, sealer: sealer, spacing: spacing,
		gates: make(map[string]*pace.Gate), log: log,
		now: time.Now, maxAttempts: maxDeliveryAttempts,
	}
}

// gateFor returns the per-channel-type limiter. Different providers are
// different services, so a slow Telegram webhook must not hold up Resend (or
// another provider); channels of the same type still share one global pacing.
func (w *DeliveryWorker) gateFor(channelType string) *pace.Gate {
	w.mu.Lock()
	defer w.mu.Unlock()
	if g, ok := w.gates[channelType]; ok {
		return g
	}
	g := pace.New(1, w.spacing)
	w.gates[channelType] = g
	return g
}

type deliveryGroup struct {
	id          string
	cycleID     string
	channelType string
	attempts    int
	createdAt   string
}

type deliveryRow struct {
	id        int64
	userID    int64
	channelID int64
	kind      string
}

// DeliverPending drains every retryable group once. A failed group does not
// block the others, and the global seen IDs were already committed by the poll
// cycle, so a broken channel never causes duplicate pushes next round.
func (w *DeliveryWorker) DeliverPending(ctx context.Context) (sent int, failed int, err error) {
	rows, err := w.db.QueryContext(ctx, `SELECT group_id, cycle_id, channel_type, attempts, created_at
		FROM delivery_groups WHERE status IN ('pending','retry') ORDER BY created_at, group_id`)
	if err != nil {
		return 0, 0, fmt.Errorf("orchestrator: list delivery groups: %w", err)
	}
	groups := make([]deliveryGroup, 0)
	for rows.Next() {
		var g deliveryGroup
		if err := rows.Scan(&g.id, &g.cycleID, &g.channelType, &g.attempts, &g.createdAt); err != nil {
			rows.Close()
			return 0, 0, err
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()

	for _, g := range groups {
		if w.expired(g) {
			if err := w.markAbandoned(g, "retry window exhausted"); err != nil {
				return sent, failed, err
			}
			failed++
			continue
		}
		release, err := w.gateFor(g.channelType).Acquire(ctx)
		if err != nil {
			return sent, failed, err
		}
		sendErr := w.sendGroup(ctx, g)
		release()
		if sendErr != nil {
			if err := w.markFailure(g, sendErr); err != nil {
				return sent, failed, err
			}
			w.log.Warn("webui: delivery failed", "group", g.id,
				"channel_type", g.channelType, "attempt", g.attempts+1, "error", sendErr)
			failed++
			continue
		}
		if err := w.markSent(g); err != nil {
			return sent, failed, err
		}
		sent++
	}
	return sent, failed, nil
}

func (w *DeliveryWorker) expired(g deliveryGroup) bool {
	if g.attempts >= w.maxAttempts {
		return true
	}
	created, err := time.Parse(time.RFC3339Nano, g.createdAt)
	if err != nil {
		return false
	}
	return w.now().UTC().Sub(created) > deliveryTTL
}

func (w *DeliveryWorker) sendGroup(ctx context.Context, g deliveryGroup) error {
	rows, err := w.db.QueryContext(ctx, `SELECT id, user_id, channel_id, kind
		FROM deliveries WHERE group_id=? AND status IN ('pending','retry') ORDER BY id`, g.id)
	if err != nil {
		return err
	}
	items := make([]deliveryRow, 0)
	for rows.Next() {
		var d deliveryRow
		if err := rows.Scan(&d.id, &d.userID, &d.channelID, &d.kind); err != nil {
			rows.Close()
			return err
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(items) == 0 {
		return nil
	}

	note, err := w.loadNote(g.cycleID, items[0].kind)
	if err != nil {
		return err
	}
	spec, err := w.specFor(items[0].channelID)
	if err != nil {
		return err
	}
	n, err := w.reg.Build(spec, w.log)
	if err != nil {
		return fmt.Errorf("orchestrator: build notifier %q: %w", spec.Type, err)
	}
	if err := n.Send(ctx, note); err != nil {
		return err
	}
	return nil
}

// loadNote resolves the materialized notification. Planned rows encode the id
// in deliveries.kind ("<kind>#<id>"); the first-start seed baseline uses the
// original bare kind, so fall back to (cycle_id, kind).
func (w *DeliveryWorker) loadNote(cycleID, kind string) (domain.Notification, error) {
	var raw string
	var err error
	baseKind := kind
	if i := strings.LastIndex(kind, "#"); i >= 0 {
		id, parseErr := strconv.ParseInt(kind[i+1:], 10, 64)
		if parseErr != nil {
			return domain.Notification{}, fmt.Errorf("orchestrator: bad delivery kind %q: %w", kind, parseErr)
		}
		baseKind = kind[:i]
		err = w.db.QueryRow(`SELECT markdown FROM notifications WHERE id=?`, id).Scan(&raw)
	} else {
		err = w.db.QueryRow(`SELECT markdown FROM notifications WHERE cycle_id=? AND kind=? ORDER BY id LIMIT 1`,
			cycleID, kind).Scan(&raw)
	}
	if err != nil {
		return domain.Notification{}, fmt.Errorf("orchestrator: load notification: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return domain.Notification{Kind: domain.NotificationKind(baseKind), GeneratedAt: w.now()}, nil
	}
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return domain.Notification{}, fmt.Errorf("orchestrator: decode envelope: %w", err)
	}
	note := env.Note
	if note.GeneratedAt.IsZero() {
		note.GeneratedAt = w.now()
	}
	return note, nil
}

// specFor rebuilds one channel's notifier spec in memory, merging the sealed
// credentials back into the options the adapters expect. The WebUI never reads
// environment variables here: a user-supplied variable name must not be able to
// pull the operator's own secrets into somebody else's delivery.
func (w *DeliveryWorker) specFor(channelID int64) (notifier.NotifierSpec, error) {
	var typ, raw, sealed string
	if err := w.db.QueryRow(`SELECT c.type, c.config_json, COALESCE(s.ciphertext, '')
		FROM channels c LEFT JOIN channel_secrets s ON s.channel_id = c.id
		WHERE c.id=?`, channelID).
		Scan(&typ, &raw, &sealed); err != nil {
		return notifier.NotifierSpec{}, fmt.Errorf("orchestrator: load channel %d: %w", channelID, err)
	}
	var cs webui.ChannelSpec
	if err := json.Unmarshal([]byte(raw), &cs); err != nil {
		return notifier.NotifierSpec{}, fmt.Errorf("orchestrator: decode channel %d: %w", channelID, err)
	}
	opts := make(map[string]string, len(cs.Options)+4)
	for k, v := range cs.Options {
		opts[k] = v
	}
	var apiKey string
	if strings.TrimSpace(sealed) != "" {
		values, err := w.sealer.OpenMap(sealed, secrets.ChannelAAD(channelID))
		if err != nil {
			return notifier.NotifierSpec{}, fmt.Errorf("orchestrator: decrypt channel %d credentials: %w", channelID, err)
		}
		for k, v := range values {
			switch k {
			case "api_key":
				apiKey = v
			case "headers_json":
				if err := mergeHeaders(opts, v); err != nil {
					return notifier.NotifierSpec{}, fmt.Errorf("orchestrator: channel %d headers: %w", channelID, err)
				}
			default:
				opts[k] = v
			}
		}
	}
	if typ == "" {
		typ = cs.Type
	}
	return notifier.NotifierSpec{
		Type: typ, Name: cs.Name, From: cs.From, To: cs.To, Cc: cs.Cc,
		APIKey: apiKey, Options: opts,
		SubjectPrefix: cs.SubjectPrefix,
	}, nil
}

// mergeHeaders expands the generic webhook's stored header map into the
// "header.<Name>" option keys the adapter reads.
func mergeHeaders(opts map[string]string, raw string) error {
	headers := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return fmt.Errorf("headers_json is not an object of strings")
	}
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		opts["header."+name] = value
	}
	return nil
}

func (w *DeliveryWorker) markSent(g deliveryGroup) error {
	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := w.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE delivery_groups SET status='sent', last_error='' WHERE group_id=?`, g.id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE deliveries SET status='sent', sent_at=? WHERE group_id=? AND status IN ('pending','retry')`, now, g.id); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *DeliveryWorker) markFailure(g deliveryGroup, sendErr error) error {
	attempts := g.attempts + 1
	status := "retry"
	if attempts >= w.maxAttempts {
		status = "abandoned"
	}
	tx, err := w.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE delivery_groups SET status=?, attempts=?, last_error=? WHERE group_id=?`,
		status, attempts, sendErr.Error(), g.id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE deliveries SET status=?, attempts=? WHERE group_id=? AND status IN ('pending','retry')`,
		status, attempts, g.id); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *DeliveryWorker) markAbandoned(g deliveryGroup, reason string) error {
	_, err := w.db.Exec(`UPDATE delivery_groups SET status='abandoned', last_error=? WHERE group_id=?`, reason, g.id)
	return err
}
