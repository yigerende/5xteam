package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/model"
)

const qualityAccountsWhere = `COALESCE(json_extract(profile,'$.remove_status'),'')<>'completed' AND (
 (COALESCE(json_extract(profile,'$.dead'),0)=0 AND json_extract(profile,'$.accept_status')='completed' AND json_extract(profile,'$.push_status')='completed'
 AND json_extract(profile,'$.remote_removed_at') IS NULL AND COALESCE(json_extract(profile,'$.push_provider'),'sub2')<>'cpa' AND COALESCE(json_extract(profile,'$.sub2_account_id'),0)>0)
 OR (json_extract(profile,'$.quality.action')='kick' AND json_extract(profile,'$.quality.action_status') IN ('pending','failed') AND json_extract(profile,'$.remote_removed_at') IS NOT NULL))`

func (s *Store) QualityAccounts() ([]model.FreeAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT profile FROM free_accounts WHERE " + qualityAccountsWhere)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.FreeAccountProfile{}
	for rows.Next() {
		var raw string
		var p model.FreeAccountProfile
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// QualitySchedule only reads the durable summary; page polling never fetches
// credentials, calls Sub2, or changes the scheduler deadline.
func (s *Store) QualitySchedule(revision string) (int, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	var raw sql.NullString
	err := s.db.QueryRow(`SELECT COUNT(*),MIN(CASE WHEN COALESCE(json_extract(profile,'$.quality.revision'),'')<>? OR json_extract(profile,'$.quality.next_at') IS NULL THEN '' ELSE json_extract(profile,'$.quality.next_at') END)
 FROM free_accounts WHERE `+qualityAccountsWhere, revision).Scan(&count, &raw)
	next, _ := time.Parse(time.RFC3339Nano, raw.String)
	return count, next, err
}

func (s *Store) QualitySettings() (model.QualitySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := model.DefaultQualitySettings()
	var raw string
	err := s.db.QueryRow("SELECT payload FROM quality_settings WHERE id=1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	// Legacy saved settings keep their original question, not the new defaults.
	v.Questions = nil
	err = json.Unmarshal([]byte(raw), &v)
	return v, err
}

// One local aggregate, with no credential decryption or remote requests.
func (s *Store) QualitySummary(revision string) (model.QualitySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out model.QualitySummary
	var last sql.NullString
	err := s.db.QueryRow(`WITH items AS (
 SELECT json_extract(profile,'$.quality') AS q,
 COALESCE(json_extract(profile,'$.dead'),0)=0 AND json_extract(profile,'$.accept_status')='completed'
 AND json_extract(profile,'$.push_status')='completed' AND COALESCE(json_extract(profile,'$.remove_status'),'')<>'completed'
 AND json_extract(profile,'$.remote_removed_at') IS NULL AND COALESCE(json_extract(profile,'$.push_provider'),'sub2')<>'cpa'
 AND COALESCE(json_extract(profile,'$.sub2_account_id'),0)>0 AS active,
 json_extract(profile,'$.remote_removed_at') IS NOT NULL AS removed FROM free_accounts
 ) SELECT
 COALESCE(SUM(active),0),
 COALESCE(SUM(active AND COALESCE(json_extract(q,'$.degraded'),0)=1),0),
 COALESCE(SUM(active AND json_extract(q,'$.status')='normal' AND COALESCE(json_extract(q,'$.degraded'),0)=0 AND json_extract(q,'$.revision')=?),0),
 COALESCE(SUM(active AND json_extract(q,'$.status')='error' AND json_extract(q,'$.revision')=?),0),
 COALESCE(SUM(active AND json_extract(q,'$.status')='suspect' AND json_extract(q,'$.revision')=?),0),
 COALESCE(SUM(active AND json_extract(q,'$.routed')=1),0),
 COALESCE(SUM(active AND json_extract(q,'$.content_passed')=1 AND json_extract(q,'$.revision')=?),0),
 COALESCE(SUM(active AND json_extract(q,'$.time_passed')=1 AND json_extract(q,'$.revision')=?),0),
 COALESCE(SUM(removed AND json_extract(q,'$.action')='kick'),0),
 COALESCE(SUM((active AND json_extract(q,'$.routed')=1) OR (removed AND json_extract(q,'$.action')='kick')),0),
 COALESCE(SUM(active AND json_extract(q,'$.model_audit.status') IN ('normal','variant') AND json_extract(q,'$.model_audit.revision')=?),0),
 COALESCE(SUM(active AND json_extract(q,'$.model_audit.failures')>0 AND json_extract(q,'$.model_audit.revision')=?),0),
 COALESCE(SUM(active AND json_extract(q,'$.model_audit.status')='error' AND json_extract(q,'$.model_audit.revision')=?),0),
 MAX(json_extract(q,'$.checked_at')) FROM items`, revision, revision, revision, revision, revision, revision, revision, revision).Scan(
		&out.Total, &out.Degraded, &out.Normal, &out.Errors, &out.Suspect, &out.Routed, &out.ContentPassed, &out.TimePassed, &out.Kicked, &out.Processed, &out.ModelNormal, &out.ModelSuspect, &out.ModelErrors, &last)
	if last.Valid {
		if value, parseErr := time.Parse(time.RFC3339Nano, last.String); parseErr == nil {
			out.LastCheckedAt = &value
		}
	}
	return out, err
}

func (s *Store) QualityHistory(id string, limit int) ([]model.AutoRotationEvent, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT payload FROM auto_rotation_events WHERE account_id=? AND json_extract(payload,'$.stage')='quality' ORDER BY created_at DESC,id DESC LIMIT ?", id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []model.AutoRotationEvent{}
	for rows.Next() {
		var raw string
		var event model.AutoRotationEvent
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func nextCycleQuality(q model.AccountQuality) model.AccountQuality {
	return model.AccountQuality{Status: q.Status, Degraded: q.Degraded, Excluded: q.Excluded, QuestionDegraded: q.QuestionDegraded, ModelAudit: model.ModelAuditState{Degraded: q.ModelAudit.Degraded}}
}
func (s *Store) SaveQualitySettings(v model.QualitySettings) (model.QualitySettings, error) {
	if err := v.Validate(); err != nil {
		return v, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldRaw string
	var old model.QualitySettings
	if err := s.db.QueryRow("SELECT payload FROM quality_settings WHERE id=1").Scan(&oldRaw); err == nil {
		_ = json.Unmarshal([]byte(oldRaw), &old)
	}
	v.ModelAuditEnabledAt = old.ModelAuditEnabledAt
	if v.Enabled && v.ModelAuditEnabled && (!old.Enabled || !old.ModelAuditEnabled || old.ModelAuditEnabledAt == nil) {
		now := time.Now().UTC()
		v.ModelAuditEnabledAt = &now
	}
	v.Revision = fmt.Sprint(time.Now().UnixNano())
	raw, err := json.Marshal(v)
	if err == nil {
		_, err = s.db.Exec("INSERT INTO quality_settings(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", string(raw))
	}
	return v, err
}

func (s *Store) QualityModelSchedule(revision string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw sql.NullString
	err := s.db.QueryRow(`SELECT MIN(CASE WHEN COALESCE(json_extract(profile,'$.quality.model_audit.revision'),'')<>? OR json_extract(profile,'$.quality.model_audit.next_at') IS NULL THEN '' ELSE json_extract(profile,'$.quality.model_audit.next_at') END) FROM free_accounts WHERE `+qualityAccountsWhere, revision).Scan(&raw)
	next, _ := time.Parse(time.RFC3339Nano, raw.String)
	return next, err
}
