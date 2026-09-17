package store

import (
	"encoding/json"

	"chatgpt-space-merge/internal/model"
)

// AdminAccountProfile reads display metadata without decrypting credentials or
// aggregating the mother's child accounts.
func (s *Store) AdminAccountProfile(id string) (model.AdminAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM admin_accounts WHERE id = ?", id).Scan(&raw); err != nil {
		return model.AdminAccountProfile{}, err
	}
	var profile model.AdminAccountProfile
	err := json.Unmarshal([]byte(raw), &profile)
	return profile, err
}
