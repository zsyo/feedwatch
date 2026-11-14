//go:build webui

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"feedwatch/internal/webui"
	"feedwatch/internal/webui/secrets"
	"feedwatch/internal/webui/store"
)

// channelView is what the browser sees: the channel definition plus the NAMES
// of the credentials that are stored. Values never leave the server, not even
// masked, so a stolen admin session cannot read somebody's bot token.
type channelView struct {
	ID           int64             `json:"id"`
	Type         string            `json:"type"`
	Name         string            `json:"name"`
	Config       webui.ChannelSpec `json:"config"`
	Enabled      bool              `json:"enabled"`
	SecretFields []string          `json:"secret_fields"`
	SecretHints  map[string]string `json:"secret_hints,omitempty"`
	SecretState  string            `json:"secret_state"`
}

// channelInput is the write shape. fields carries every declared non-secret
// value as a string (lists comma separated, bools "true"/"false"), so the SPA
// can drive the whole form from webui.ChannelFields.
type channelInput struct {
	Type         string            `json:"type"`
	Name         string            `json:"name"`
	Fields       map[string]string `json:"fields"`
	Secrets      map[string]string `json:"secrets"`
	ClearSecrets []string          `json:"clear_secrets"`
	Enabled      *bool             `json:"enabled"`
}

const (
	secretStateOK      = "ok"
	secretStateMissing = "missing"
	secretStateError   = "error"
	secretStateNone    = "none"
)

// channelFields serves the form schema so the SPA renders exactly the fields the
// server validates. Keeping it here means a new channel option is one Go edit.
func (s *Server) channelFields(w http.ResponseWriter, _ *http.Request, _ Ctx) {
	writeJSON(w, http.StatusOK, map[string]any{
		"types":  webui.ChannelTypes(),
		"fields": webui.ChannelFields,
	})
}

func (s *Server) channelsFor(userID int64) ([]channelView, error) {
	rows, err := s.db.Query(`SELECT c.id, c.type, c.name, c.config_json, c.enabled,
			COALESCE(sec.ciphertext, '')
		FROM channels c LEFT JOIN channel_secrets sec ON sec.channel_id = c.id
		WHERE c.user_id=? ORDER BY c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]channelView, 0)
	for rows.Next() {
		var (
			item    channelView
			raw     string
			enabled int
			sealed  string
		)
		if err := rows.Scan(&item.ID, &item.Type, &item.Name, &raw, &enabled, &sealed); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		if err := json.Unmarshal([]byte(raw), &item.Config); err != nil {
			return nil, fmt.Errorf("decode channel %d config: %w", item.ID, err)
		}
		if item.Config.Options == nil {
			item.Config.Options = map[string]string{}
		}
		s.decorateSecrets(&item, sealed)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// decorateSecrets only reports which secret fields exist and a scheme://host
// hint for URLs. Decrypted values are dropped immediately.
func (s *Server) decorateSecrets(item *channelView, sealed string) {
	declared := webui.SecretKeys(item.Type)
	item.SecretFields = make([]string, 0, len(declared))
	if len(declared) == 0 {
		item.SecretState = secretStateNone
		return
	}
	if strings.TrimSpace(sealed) == "" {
		item.SecretState = secretStateMissing
		return
	}
	values, err := s.sealer.OpenMap(sealed, secrets.ChannelAAD(item.ID))
	if err != nil {
		// Most likely the master key was replaced. Delivering will fail too, so
		// surface it instead of pretending the channel is fine.
		item.SecretState = secretStateError
		s.log.Warn("webui: cannot decrypt channel secrets", "channel", item.ID, "error", err)
		return
	}
	have := make(map[string]bool, len(values))
	hints := make(map[string]string, len(values))
	for _, key := range declared {
		value, ok := values[key]
		if !ok || value == "" {
			continue
		}
		item.SecretFields = append(item.SecretFields, key)
		have[key] = true
		if hint := webui.SecretHint(key, value); hint != "" {
			hints[key] = hint
		}
	}
	if len(hints) > 0 {
		item.SecretHints = hints
	}
	if err := webui.ValidateSecrets(item.Type, nil, item.SecretFields); err != nil {
		item.SecretState = secretStateMissing
		return
	}
	item.SecretState = secretStateOK
}

func (s *Server) loadChannel(userID, id int64) (channelView, error) {
	var (
		item    channelView
		raw     string
		enabled int
		sealed  string
	)
	err := s.db.QueryRow(`SELECT c.id, c.type, c.name, c.config_json, c.enabled,
			COALESCE(sec.ciphertext, '')
		FROM channels c LEFT JOIN channel_secrets sec ON sec.channel_id = c.id
		WHERE c.id=? AND c.user_id=?`, id, userID).
		Scan(&item.ID, &item.Type, &item.Name, &raw, &enabled, &sealed)
	if err != nil {
		return channelView{}, err
	}
	item.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(raw), &item.Config); err != nil {
		return channelView{}, fmt.Errorf("decode channel %d config: %w", id, err)
	}
	if item.Config.Options == nil {
		item.Config.Options = map[string]string{}
	}
	s.decorateSecrets(&item, sealed)
	return item, nil
}

func (s *Server) listChannels(w http.ResponseWriter, _ *http.Request, c Ctx) {
	items, err := s.channelsFor(c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": items})
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request, c Ctx) {
	var input channelInput
	if err := decodeJSON(w, r, &input); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, ok := webui.FieldsOf(input.Type); !ok {
		fail(w, http.StatusBadRequest, "invalid_channel_type", "unsupported channel type")
		return
	}
	spec, err := webui.BuildSpec(input.Type, input.Name, input.Fields)
	if err != nil {
		writeChannelError(w, err)
		return
	}
	if err := webui.ValidateSecrets(input.Type, input.Secrets, nil); err != nil {
		writeChannelError(w, err)
		return
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		fail(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO channels (user_id, type, name, config_json, enabled) VALUES (?,?,?,?,?)`,
		c.User.ID, spec.Type, spec.Name, string(raw), 1)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if err := s.sealInto(tx, id, input.Secrets); err != nil {
		fail(w, http.StatusInternalServerError, "seal_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var input channelInput
	if err := decodeJSON(w, r, &input); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	existing, err := s.loadChannel(c.User.ID, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// A toggle from the list only carries enabled; leave everything else alone.
	if input.Type == "" && input.Name == "" && input.Fields == nil && input.Secrets == nil &&
		input.ClearSecrets == nil && input.Enabled != nil {
		res, err := s.db.Exec(`UPDATE channels SET enabled=? WHERE id=? AND user_id=?`,
			boolInt(*input.Enabled), id, c.User.ID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		requireAffected(w, res, "channel")
		return
	}

	typ := strings.TrimSpace(input.Type)
	if typ == "" {
		typ = existing.Type
	}
	if _, ok := webui.FieldsOf(typ); !ok {
		fail(w, http.StatusBadRequest, "invalid_channel_type", "unsupported channel type")
		return
	}
	spec, err := webui.BuildSpec(typ, input.Name, input.Fields)
	if err != nil {
		writeChannelError(w, err)
		return
	}

	merged, err := s.mergeSecrets(existing, typ, input)
	if err != nil {
		writeChannelError(w, err)
		return
	}
	if err := webui.ValidateSecrets(typ, merged, nil); err != nil {
		writeChannelError(w, err)
		return
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		fail(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	enabled := existing.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	tx, err := s.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE channels SET type=?, name=?, config_json=?, enabled=? WHERE id=? AND user_id=?`,
		spec.Type, spec.Name, string(raw), boolInt(enabled), id, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if affected == 0 {
		fail(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	if err := s.sealInto(tx, id, merged); err != nil {
		fail(w, http.StatusInternalServerError, "seal_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// mergeSecrets starts from the stored values, applies supplied non-empty ones,
// drops explicitly cleared ones, and finally keeps only the keys the (possibly
// new) type still declares. Switching type therefore cannot leave orphans.
func (s *Server) mergeSecrets(existing channelView, typ string, input channelInput) (map[string]string, error) {
	merged := make(map[string]string)
	if existing.SecretState != secretStateNone && existing.SecretState != secretStateMissing {
		var sealed string
		if err := s.db.QueryRow(`SELECT ciphertext FROM channel_secrets WHERE channel_id=?`, existing.ID).Scan(&sealed); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		} else {
			values, err := s.sealer.OpenMap(sealed, secrets.ChannelAAD(existing.ID))
			if err != nil {
				// A replaced master key must not permanently brick the row: the
				// user is allowed to overwrite the credentials from here.
				s.log.Warn("webui: replacing unreadable channel secrets", "channel", existing.ID, "error", err)
			} else {
				for k, v := range values {
					merged[k] = v
				}
			}
		}
	}
	for key, value := range input.Secrets {
		if value = strings.TrimSpace(value); value != "" {
			merged[key] = value
		}
	}
	for _, key := range input.ClearSecrets {
		delete(merged, key)
	}
	allowed := make(map[string]bool)
	for _, key := range webui.SecretKeys(typ) {
		allowed[key] = true
	}
	for key := range merged {
		if !allowed[key] {
			delete(merged, key)
		}
	}
	return merged, nil
}

// sealInto writes (or clears) the sealed blob for one channel inside tx.
func (s *Server) sealInto(tx *sql.Tx, channelID int64, values map[string]string) error {
	sealed, err := s.sealer.SealMap(values, secrets.ChannelAAD(channelID))
	if err != nil {
		return err
	}
	if sealed == "" {
		_, err = tx.Exec(`DELETE FROM channel_secrets WHERE channel_id=?`, channelID)
		return err
	}
	_, err = tx.Exec(`INSERT INTO channel_secrets (channel_id, ciphertext, updated_at) VALUES (?,?,?)
		ON CONFLICT(channel_id) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`,
		channelID, sealed, store.Now())
	return err
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := s.db.Exec(`DELETE FROM channels WHERE id=? AND user_id=?`, id, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "channel")
}

// writeChannelError turns a validation error into a 400 with its stable code.
func writeChannelError(w http.ResponseWriter, err error) {
	var ce *webui.ChannelError
	if errors.As(err, &ce) {
		fail(w, http.StatusBadRequest, ce.Code, ce.Message)
		return
	}
	fail(w, http.StatusBadRequest, "invalid_channel", err.Error())
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
