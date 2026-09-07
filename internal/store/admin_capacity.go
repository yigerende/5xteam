package store

import (
	"encoding/json"
	"time"

	"chatgpt-space-merge/internal/model"
)

// SaveAdminCapacitySnapshot persists the latest capacity obtained by the
// Team account-management page. Automatic rotation deliberately consumes
// these snapshots instead of issuing a remote capacity request on every
// scheduler tick.
func (s *Store) SaveAdminCapacitySnapshot(adminID string, capacity model.AdminSeatCapacity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if capacity.FetchedAt.IsZero() {
		capacity.FetchedAt = time.Now()
	}
	payload, err := json.Marshal(capacity)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO admin_capacity_snapshots(admin_account_id,payload,fetched_at)
		VALUES(?,?,?) ON CONFLICT(admin_account_id) DO UPDATE SET payload=excluded.payload,fetched_at=excluded.fetched_at`,
		adminID, string(payload), formatTime(capacity.FetchedAt))
	return err
}

// AdminCapacitySnapshots returns the latest locally persisted capacity for
// each mother account. Missing or malformed snapshots are ignored so one bad
// record cannot prevent other accounts from participating in a decision.
func (s *Store) AdminCapacitySnapshots() map[string]model.AdminSeatCapacity {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT admin_account_id,payload,fetched_at FROM admin_capacity_snapshots")
	if err != nil {
		return map[string]model.AdminSeatCapacity{}
	}
	defer rows.Close()
	result := make(map[string]model.AdminSeatCapacity)
	for rows.Next() {
		var id, raw, fetchedAt string
		var capacity model.AdminSeatCapacity
		if rows.Scan(&id, &raw, &fetchedAt) != nil || json.Unmarshal([]byte(raw), &capacity) != nil {
			continue
		}
		if capacity.FetchedAt.IsZero() {
			if parsed, parseErr := parseTime(fetchedAt); parseErr == nil {
				capacity.FetchedAt = parsed
			}
		}
		result[id] = capacity
	}
	return result
}

// AdminCapacitySnapshot returns one snapshot and whether it exists.
func (s *Store) AdminCapacitySnapshot(adminID string) (model.AdminSeatCapacity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, fetchedAt string
	if err := s.db.QueryRow("SELECT payload,fetched_at FROM admin_capacity_snapshots WHERE admin_account_id=?", adminID).Scan(&raw, &fetchedAt); err != nil {
		return model.AdminSeatCapacity{}, false
	}
	var capacity model.AdminSeatCapacity
	if json.Unmarshal([]byte(raw), &capacity) != nil {
		return model.AdminSeatCapacity{}, false
	}
	if capacity.FetchedAt.IsZero() {
		if parsed, parseErr := parseTime(fetchedAt); parseErr == nil {
			capacity.FetchedAt = parsed
		}
	}
	return capacity, true
}
