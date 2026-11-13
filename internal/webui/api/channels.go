//go:build webui

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"feedwatch/internal/webui"
)

type channel struct {
	ID      int64             `json:"id"`
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Config  webui.ChannelSpec `json:"config"`
	Enabled bool              `json:"enabled"`
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateChannelSpec refuses plaintext credentials. Secrets must be supplied
// as ${ENV_VAR} placeholders (expanded only in memory at send time) or as the
// NAME of an environment variable for email API keys.
func validateChannelSpec(spec webui.ChannelSpec) error {
	if strings.TrimSpace(spec.Type) == "" {
		return fmt.Errorf("type is required")
	}
	if spec.APIKeyEnv != "" && !envNamePattern.MatchString(spec.APIKeyEnv) {
		return fmt.Errorf("api_key_env must be an environment variable NAME, not a value or ${...}")
	}
	for k, v := range spec.Options {
		if v != "" && !webui.IsEnvRef(v) && webui.LooksLikeSecret(k) {
			return fmt.Errorf("option %q must use a ${ENV_VAR} placeholder", k)
		}
	}
	return nil
}

// maskChannelSpec hides any value that still looks like a credential. Written
// rows are already sanitized, so this is a second line of defence for rows
// imported from older data.
func maskChannelSpec(spec webui.ChannelSpec) webui.ChannelSpec {
	for k, v := range spec.Options {
		if v != "" && !webui.IsEnvRef(v) && webui.LooksLikeSecret(k) {
			spec.Options[k] = "***"
		}
	}
	return spec
}

type channelScanner interface{ Scan(dest ...any) error }

func scanChannel(row channelScanner) (channel, error) {
	var c channel
	var enabled int
	var raw string
	if err := row.Scan(&c.ID, &c.Type, &c.Name, &raw, &enabled); err != nil {
		return channel{}, err
	}
	c.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(raw), &c.Config); err != nil {
		return channel{}, fmt.Errorf("decode channel config: %w", err)
	}
	if c.Config.Options == nil {
		c.Config.Options = map[string]string{}
	}
	c.Config = maskChannelSpec(c.Config)
	return c, nil
}

func (s *Server) listChannels(w http.ResponseWriter, _ *http.Request, c Ctx) {
	rows, err := s.db.Query(`SELECT id, type, name, config_json, enabled
		FROM channels WHERE user_id=? ORDER BY id`, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := make([]channel, 0)
	for rows.Next() {
		item, err := scanChannel(rows)
		if err != nil {
			fail(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request, c Ctx) {
	var spec webui.ChannelSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := validateChannelSpec(spec); err != nil {
		fail(w, http.StatusBadRequest, "invalid_channel", err.Error())
		return
	}
	spec.Options = webui.SanitizeOptions(spec.Options, func(k string) {
		s.log.Warn("webui: dropped plaintext secret from channel", "option", k)
	})
	raw, err := json.Marshal(spec)
	if err != nil {
		fail(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := s.db.Exec(`INSERT INTO channels (user_id, type, name, config_json, enabled) VALUES (?,?,?,?,?)`,
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
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request, c Ctx) {
	id, err := parseID(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var raw string
	if err := s.db.QueryRow(`SELECT config_json FROM channels WHERE id=? AND user_id=?`, id, c.User.ID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var spec webui.ChannelSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		fail(w, http.StatusInternalServerError, "decode_failed", err.Error())
		return
	}
	var patch webui.ChannelSpec
	if err := decodeJSON(w, r, &patch); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if patch.Type != "" {
		spec.Type = patch.Type
	}
	if patch.Name != "" {
		spec.Name = patch.Name
	}
	if patch.From != "" {
		spec.From = patch.From
	}
	if patch.To != nil {
		spec.To = patch.To
	}
	if patch.Cc != nil {
		spec.Cc = patch.Cc
	}
	if patch.APIKeyEnv != "" {
		spec.APIKeyEnv = patch.APIKeyEnv
	}
	if patch.SubjectPrefix != "" {
		spec.SubjectPrefix = patch.SubjectPrefix
	}
	if patch.Options != nil {
		spec.Options = patch.Options
	}
	if err := validateChannelSpec(spec); err != nil {
		fail(w, http.StatusBadRequest, "invalid_channel", err.Error())
		return
	}
	spec.Options = webui.SanitizeOptions(spec.Options, nil)
	encoded, err := json.Marshal(spec)
	if err != nil {
		fail(w, http.StatusInternalServerError, "encode_failed", err.Error())
		return
	}
	res, err := s.db.Exec(`UPDATE channels SET type=?, name=?, config_json=? WHERE id=? AND user_id=?`,
		spec.Type, spec.Name, string(encoded), id, c.User.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	requireAffected(w, res, "channel")
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
