package store

import (
	"chatgpt-space-merge/internal/model"
	"encoding/json"
)

func mergeV2RestartEventID(account, request string) string {
	return "merge-v2-restart:" + account + ":" + request
}

func (s *Store) ProMergeV2RestartSeen(account, request string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	err := s.db.QueryRow("SELECT count(*) FROM auto_rotation_events WHERE id=?", mergeV2RestartEventID(account, request)).Scan(&count)
	return count != 0, err
}

func (s *Store) PendingProMergeV2Restarts() ([]model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT profile FROM mail_accounts WHERE json_extract(profile,'$.pro_merge_v2_restart.status') IN ('pending','starting') ORDER BY json_extract(profile,'$.pro_merge_v2_restart.requested_at') LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MailAccountProfile
	for rows.Next() {
		var raw string
		var p model.MailAccountProfile
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
