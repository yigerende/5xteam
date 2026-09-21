package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Only automation fields are changed here. Downstream credentials and groups
// may be edited separately, so never overwrite them from an automation form.
func (s *Store) SaveProAutomationSettings(input model.ProSettings, pay gptpay.Settings, key string) (model.ProSettings, gptpay.Settings, error) {
	pay, err := gptpay.NormalizeSettings(pay)
	if err != nil {
		return input, pay, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return input, pay, err
	}
	defer tx.Rollback()
	v := model.DefaultProSettings()
	var raw string
	err = tx.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, pay, err
	}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return v, pay, err
		}
	}
	v.QuotaEnabled, v.AutoMergeEnabled = input.QuotaEnabled, input.AutoMergeEnabled
	v.ScheduledEnabled, v.MaxUnmerged, v.ScheduleIntervalSeconds = input.ScheduledEnabled, input.MaxUnmerged, input.ScheduleIntervalSeconds
	v.QuotaUsedThreshold, v.QuotaCheckIntervalSeconds = input.QuotaUsedThreshold, input.QuotaCheckIntervalSeconds
	v.TargetAdminID, v.TargetSeatType = strings.TrimSpace(input.TargetAdminID), input.TargetSeatType
	v.RetryCount, v.RetryIntervalSeconds, v.Concurrency = input.RetryCount, input.RetryIntervalSeconds, input.Concurrency
	normalizeProSettings(&v)
	v.NextQuotaSweepAt = nil
	if v.QuotaEnabled {
		next := time.Now().Add(time.Duration(v.QuotaCheckIntervalSeconds) * time.Second)
		v.NextQuotaSweepAt = &next
	}
	var sealed string
	err = tx.QueryRow("SELECT encrypted_key FROM gptpay_settings WHERE id=1").Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, pay, err
	}
	if strings.TrimSpace(key) != "" {
		sealed, err = s.encrypt(strings.TrimSpace(key))
		if err != nil {
			return v, pay, err
		}
	}
	pay.KeyPresent = sealed != ""
	if _, err = tx.Exec(`INSERT INTO pro_settings(id,profile,encrypted_sub2_password,encrypted_cpa_key) VALUES(1,?,'','') ON CONFLICT(id) DO UPDATE SET profile=excluded.profile`, mustJSON(v)); err != nil {
		return v, pay, err
	}
	if _, err = tx.Exec(`INSERT INTO gptpay_settings(id,profile,encrypted_key) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_key=excluded.encrypted_key`, mustJSON(pay), sealed); err != nil {
		return v, pay, err
	}
	if _, err = tx.Exec(`INSERT INTO pro_schedule_clock VALUES(1,?) ON CONFLICT(id) DO UPDATE SET next_at=excluded.next_at`, time.Now().Add(time.Duration(v.ScheduleIntervalSeconds)*time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		return v, pay, err
	}
	return v, pay, tx.Commit()
}

// Run once for old profiles. Prefer the old active quota schedule; otherwise
// retain the full-automation interval. Per-account running snapshots are untouched.
func (s *Store) migrateProQuotaSettings() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := model.DefaultProSettings()
	var raw string
	err = tx.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var fields map[string]json.RawMessage
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
	}
	if _, exists := fields["quota_used_threshold"]; !exists {
		var legacyRaw string
		err = tx.QueryRow("SELECT profile FROM gptpay_settings WHERE id=1").Scan(&legacyRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if legacyRaw != "" {
			var legacy struct {
				Threshold float64 `json:"quota_used_threshold"`
				Interval  int     `json:"quota_interval_seconds"`
			}
			if err = json.Unmarshal([]byte(legacyRaw), &legacy); err != nil {
				return err
			}
			if legacy.Threshold > 0 {
				v.QuotaUsedThreshold = legacy.Threshold
			}
			if !v.QuotaEnabled && legacy.Interval >= 10 {
				v.QuotaCheckIntervalSeconds = legacy.Interval
			}
		}
		normalizeProSettings(&v)
		if _, err = tx.Exec(`INSERT INTO pro_settings(id,profile,encrypted_sub2_password,encrypted_cpa_key) VALUES(1,?,'','') ON CONFLICT(id) DO UPDATE SET profile=excluded.profile`, mustJSON(v)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE gptpay_settings SET profile=json_remove(profile,'$.quota_used_threshold','$.quota_interval_seconds') WHERE id=1 AND (json_type(profile,'$.quota_used_threshold') IS NOT NULL OR json_type(profile,'$.quota_interval_seconds') IS NOT NULL)`); err != nil {
		return err
	}
	return tx.Commit()
}
