package store

import (
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/model"
)

var ErrStandardSeatBusy = errors.New("等待上一子号释放普通席位并完成间隔")

// Both workflows reserve under the same Store mutex. The owner remains durable
// through errors and restarts and disappears with its account/task record.
func (s *Store) checkStandardSeatLaneLocked(teamID, accountID, recoveryID string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM free_accounts WHERE json_extract(profile,'$.standard_removal.lane')=1 AND json_extract(profile,'$.team_account_id')=? AND id!=?`, teamID, accountID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrStandardSeatBusy
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM seat_recovery_tasks WHERE team_id=? AND lane=1 AND id!=?`, teamID, recoveryID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrStandardSeatBusy
	}
	return nil
}

func (s *Store) PendingStandardRemovals(now time.Time) ([]model.FreeAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT profile FROM free_accounts WHERE json_extract(profile,'$.standard_removal.stage') IS NOT NULL AND json_extract(profile,'$.standard_removal.stage') NOT IN ('completed','failed') AND json_extract(profile,'$.standard_removal.next_at')<=? ORDER BY json_extract(profile,'$.standard_removal.lane') DESC,json_extract(profile,'$.standard_removal.next_at') LIMIT 100`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.FreeAccountProfile{}
	for rows.Next() {
		var raw string
		var p model.FreeAccountProfile
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
