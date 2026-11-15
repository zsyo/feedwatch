//go:build webui

package store

import "fmt"

// Settings returns the DB-backed global settings written on first start and
// editable later through /api/admin/settings.
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT setting_key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("webui: load settings: %w", err)
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

// Setting is a convenience read with a fallback for missing keys.
func (s *Store) Setting(key, fallback string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE setting_key=?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Upsert(`INSERT INTO settings (setting_key, value, updated_at) VALUES (?,?,?)`,
		"setting_key", []string{"value", "updated_at"}, key, value, Now())
	return err
}
