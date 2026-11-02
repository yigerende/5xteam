package store

import (
	"chatgpt-space-merge/internal/model"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// A pinned proxy is encrypted separately; it never enters account/list JSON.
func (s *Store) ProMergeV2Proxy(email string, started time.Time, candidate string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stamp, sealed string
	err := s.db.QueryRow("SELECT started_at,encrypted_proxy FROM pro_merge_v2_proxies WHERE email=?", strings.ToLower(email)).Scan(&stamp, &sealed)
	if err == nil && stamp == formatTime(started) {
		return s.decrypt(sealed)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if candidate == "" {
		return "", nil
	}
	sealed, err = s.encrypt(candidate)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec("INSERT INTO pro_merge_v2_proxies(email,started_at,encrypted_proxy) VALUES(?,?,?) ON CONFLICT(email) DO UPDATE SET started_at=excluded.started_at,encrypted_proxy=excluded.encrypted_proxy", strings.ToLower(email), formatTime(started), sealed)
	return candidate, err
}

func (s *Store) DueProMergeV2Recovery(now time.Time) ([]model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT profile FROM mail_accounts WHERE
 (json_extract(profile,'$.management_scope')='pro' OR (json_extract(profile,'$.management_scope')='mail' AND json_type(profile,'$.mail_pro_actions')='object'))
 AND json_extract(profile,'$.pro_merge_v2.completed_at') IS NULL
 AND json_type(profile,'$.pro_merge_v2')='object'
 AND COALESCE(json_extract(profile,'$.pro_workflow_running'),0)=0
 AND ((COALESCE(json_extract(profile,'$.pro_auto.stop_requested'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_auto.status'),'')<>'stopped')
 OR (json_type(profile,'$.pro_dead')='object'
 AND json_extract(profile,'$.pro_merge_v2.recovery.phase')='cleanup'
 AND json_extract(profile,'$.pro_merge_v2.recovery.cleanup_stop_version')=json_extract(profile,'$.pro_auto.stop_version')))
 AND COALESCE(json_extract(profile,'$.pro_migration.paused'),0)=0
 AND COALESCE(json_extract(profile,'$.pro_merge_v2.recovery.exhausted'),0)=0
 AND ((julianday(json_extract(profile,'$.pro_merge_v2.recovery.next_at'))<=julianday(?))
 OR (json_type(profile,'$.pro_merge_v2.recovery') IS NULL AND json_extract(profile,'$.pro_merge_v2.submitted_at') IS NOT NULL AND json_extract(profile,'$.pro_merge_v2.accepted_at') IS NULL))
 ORDER BY COALESCE(json_extract(profile,'$.pro_merge_v2.recovery.next_at'),json_extract(profile,'$.pro_merge_v2.updated_at')) LIMIT 10`, formatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MailAccountProfile
	for rows.Next() {
		var raw string
		var p model.MailAccountProfile
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
