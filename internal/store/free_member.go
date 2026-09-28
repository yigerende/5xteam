package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"chatgpt-space-merge/internal/model"
)

// FreeAccountForEmail reads just the matching profile, without loading all
// rotation records or decrypting unrelated account credentials.
func (s *Store) FreeAccountForEmail(email string) (model.FreeAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.FreeAccountProfile
	var raw string
	err := s.db.QueryRow("SELECT profile FROM free_accounts WHERE LOWER(COALESCE(json_extract(profile,'$.email'),''))=? ORDER BY updated_at DESC, id DESC LIMIT 1", strings.ToLower(strings.TrimSpace(email))).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
