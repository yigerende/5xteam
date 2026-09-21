package store

import (
	"chatgpt-space-merge/internal/model"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) ProScheduleCandidates(limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT m.email FROM mail_accounts m WHERE json_extract(m.profile,'$.management_scope')='pro'
	AND coalesce(json_extract(m.profile,'$.space_merged_once'),0)=0 AND coalesce(json_extract(m.profile,'$.pro_workflow_running'),0)=0
	AND coalesce(json_extract(m.profile,'$.pro_auto.id'),'')='' AND json_type(m.profile,'$.pro_migration') IS NULL
	AND coalesce(json_extract(m.profile,'$.chatgpt_status'),'') NOT IN ('dead','disabled','invalid')
	AND coalesce(json_extract(m.profile,'$.push_status'),'') NOT IN ('completed','running')
	AND coalesce(json_extract(m.profile,'$.pro_invite_status'),'') NOT IN ('completed','running')
	AND NOT `+proOpenedCondition+`
	AND NOT EXISTS(SELECT 1 FROM gptpay_orders o WHERE o.email=m.email)
	AND NOT EXISTS(SELECT 1 FROM pro_card_reservations r WHERE r.email=m.email)
	AND NOT EXISTS(SELECT 1 FROM json_each(json_extract(m.profile,'$.pro_manual_stages')) j WHERE json_extract(j.value,'$.status')='running')
	ORDER BY json_extract(m.profile,'$.created_at'),m.email LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var email string
		if err = rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}
func (s *Store) ActiveProScheduleRun() (*model.ProScheduleRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT payload FROM pro_schedule_runs WHERE status='running' LIMIT 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v model.ProScheduleRun
	err = json.Unmarshal([]byte(raw), &v)
	return &v, err
}
func (s *Store) SaveProScheduleRun(v model.ProScheduleRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO pro_schedule_runs(id,status,started_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload`, v.ID, v.Status, formatTime(v.StartedAt), mustJSON(v))
	return err
}
func (s *Store) ProScheduleRuns(limit, offset int) ([]model.ProScheduleRun, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM pro_schedule_runs`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT payload FROM pro_schedule_runs ORDER BY started_at DESC,id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.ProScheduleRun{}
	for rows.Next() {
		var raw string
		var v model.ProScheduleRun
		if err = rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}
func (s *Store) ProScheduleNext() (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT next_at FROM pro_schedule_clock WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := time.Parse(time.RFC3339Nano, raw)
	return &v, err
}
func (s *Store) SetProScheduleNext(next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO pro_schedule_clock VALUES(1,?) ON CONFLICT(id) DO UPDATE SET next_at=excluded.next_at`, next.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *Store) RecoverProSchedule() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only process-start recovery releases login reservations. Submitted orders
	// remain occupied until the supplier confirms success or failure.
	_, err := s.db.Exec(`DELETE FROM pro_card_reservations`)
	return err
}

// A quota tick must not write its stale settings snapshot over a simultaneous
// save of payment caps or the scheduled-provisioning switch.
func (s *Store) UpdateProQuotaSweep(last, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE pro_settings SET profile=json_set(profile,'$.last_quota_sweep_at',?,'$.next_quota_sweep_at',?) WHERE id=1`, last.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano))
	return err
}
