package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/restock"
)

// Caller holds the account/start gates and excludes running workers. This only
// ends provisioning occupancy; uncertain orders, cards and budget stay intact.
func (s *Store) IsolateProRestockTask(t *restock.Task) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockProCardAdmission(tx); err != nil {
		return false, err
	}
	var raw string
	err = tx.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, t.ID).Scan(&raw)
	external := errors.Is(err, sql.ErrNoRows) && t.ManagedExternally && t.ObservedPayload == ""
	if err != nil && !external {
		return false, err
	}
	var current restock.Task
	if external {
		current = *t
	} else {
		if err = json.Unmarshal([]byte(raw), &current); err != nil {
			return false, err
		}
	}
	if raw != t.ObservedPayload || !current.Active() || current.StoppedAt != nil || (current.Status != "attention" && current.Status != "unknown") {
		return false, nil
	}
	p, err := replacementProfile(tx, current.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	owned := err == nil && p.ProRestockTaskID == current.ID
	if external {
		owned = err == nil && p.ProRestockTaskID == "" && p.ProAuto.ID == current.ID
		if !owned {
			return false, nil
		}
	}
	if owned && (p.ProWorkflowRunning || p.PushStatus == "completed" || p.ProAuto.RetryAt != nil ||
		(p.ProAuto.Stage == "oauth" && p.ProAuto.OAuthRecovery != nil && p.ProAuto.OAuthRecovery.NextAt != nil && !p.ProAuto.OAuthRecovery.Exhausted) ||
		(p.ProAuto.Replacement != nil && (p.ProAuto.Replacement.Pending || p.ProAuto.Replacement.Queued))) {
		return false, nil
	}
	now := time.Now()
	current.Status, current.AutoIsolated, current.StoppedAt, current.UpdatedAt = "cancelled", true, &now, now
	current.ETA, current.NextCardCheckAt = nil, nil
	current.Message = "异常任务已隔离，已释放补货占位并按当前缺口换号继续；原开卡、付款记录保留。原因：" + current.Message
	if owned {
		if external {
			p.ProRestockTaskID = current.ID
		}
		p.ProAuto.StopVersion++
		p.ProAuto.StopRequested, p.ProAuto.Status = true, "stopped"
		p.ProAuto.NextCheckAt, p.ProAuto.RetryAt = nil, nil
		p.ProAuto.Error, p.ProAuto.UpdatedAt = current.Message, now
		if err = saveReplacementProfile(tx, p); err != nil {
			return false, err
		}
	}
	if external {
		_, err = tx.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, current.ID, current.Email, current.Status, formatTime(current.CreatedAt), current.Cost, mustJSON(current))
	} else {
		_, err = tx.Exec(`UPDATE pro_restock_tasks SET status=?,payload=? WHERE id=?`, current.Status, mustJSON(current), current.ID)
	}
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	*t = current
	return true, nil
}
