package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"chatgpt-space-merge/internal/model"
)

func (s *Store) CPASettings() (model.CPASettings, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := model.DefaultCPASettings()
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_key FROM cpa_settings WHERE id=1").Scan(&raw, &encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return settings, "", nil
		}
		return settings, "", err
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, "", err
	}
	if settings.StatusCheckIntervalSeconds < 10 {
		settings.StatusCheckIntervalSeconds = 120
	}
	if settings.ReloginFailureLimit < 1 || settings.ReloginFailureLimit > 20 {
		settings.ReloginFailureLimit = 2
	}
	if settings.QuotaCheckIntervalSeconds < 10 {
		settings.QuotaCheckIntervalSeconds = 120
	}
	key, err := s.decryptOptional(encrypted)
	return settings, key, err
}

func (s *Store) SaveCPASettings(settings model.CPASettings, key string) (model.CPASettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings.URL = strings.TrimRight(strings.TrimSpace(settings.URL), "/")
	if settings.StatusCheckIntervalSeconds < 10 {
		settings.StatusCheckIntervalSeconds = 120
	}
	if settings.ReloginFailureLimit < 1 || settings.ReloginFailureLimit > 20 {
		settings.ReloginFailureLimit = 2
	}
	if settings.QuotaCheckIntervalSeconds < 10 {
		settings.QuotaCheckIntervalSeconds = 120
	}
	var encrypted string
	_ = s.db.QueryRow("SELECT encrypted_key FROM cpa_settings WHERE id=1").Scan(&encrypted)
	if strings.TrimSpace(key) != "" {
		var err error
		encrypted, err = s.encrypt(strings.TrimSpace(key))
		if err != nil {
			return model.CPASettings{}, err
		}
	}
	settings.KeyPresent = encrypted != ""
	raw, err := json.Marshal(settings)
	if err != nil {
		return model.CPASettings{}, err
	}
	_, err = s.db.Exec(`INSERT INTO cpa_settings(id, profile, encrypted_key) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile, encrypted_key=excluded.encrypted_key`, string(raw), encrypted)
	return settings, err
}
