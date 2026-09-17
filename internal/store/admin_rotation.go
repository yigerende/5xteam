package store

import (
	"encoding/json"
	"time"

	"chatgpt-space-merge/internal/model"
)

// UpdateAdminRotation changes scheduling without touching credentials or child state.
func (s *Store) UpdateAdminRotation(id string, disabled bool) (model.AdminAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM admin_accounts WHERE id=?", id).Scan(&raw); err != nil {
		return model.AdminAccountProfile{}, err
	}
	var profile model.AdminAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.AdminAccountProfile{}, err
	}
	profile.RotationDisabled, profile.UpdatedAt = disabled, time.Now()
	encoded, err := json.Marshal(profile)
	if err != nil {
		return model.AdminAccountProfile{}, err
	}
	_, err = s.db.Exec("UPDATE admin_accounts SET profile=? WHERE id=?", string(encoded), id)
	return profile, err
}
