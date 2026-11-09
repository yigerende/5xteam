package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

func (s *Store) ProQualitySettings() (model.ProQualitySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := model.DefaultProQualitySettings()
	var raw string
	err := s.db.QueryRow("SELECT payload FROM pro_quality_settings WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v)
	}
	return v, err
}

func (s *Store) SaveProQualitySettings(v model.ProQualitySettings) (model.ProQualitySettings, error) {
	if err := v.Validate(); err != nil {
		return v, err
	}
	statuses, _ := v.ConnectionFailureStatuses()
	parts := make([]string, len(statuses))
	for i, code := range statuses {
		parts[i] = strconv.Itoa(code)
	}
	v.ConnectionFailureHTTPStatuses = strings.Join(parts, ",")
	s.mu.Lock()
	defer s.mu.Unlock()
	v.Revision = strconv.FormatInt(time.Now().UnixNano(), 10)
	raw, err := json.Marshal(v)
	if err == nil {
		_, err = s.db.Exec("INSERT INTO pro_quality_settings(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", string(raw))
	}
	return v, err
}
