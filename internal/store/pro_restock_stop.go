package store

import (
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

var ErrProRestockStopConflict = errors.New("补货任务已结束或账号已进入其他流程，请刷新后查看")

// Read history for manual continuation without applying automatic dispatch guards.
func (s *Store) ProRestockTask(id string) (restock.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var task restock.Task
	if err := s.db.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, id).Scan(&raw); err != nil {
		return task, err
	}
	err := json.Unmarshal([]byte(raw), &task)
	task.ObservedPayload = raw
	return task, err
}

// Stop only this durable intent. Never alter payment/issuance facts or release
// an unresolved money operation; another account may be evaluated next cycle.
func (s *Store) StopProRestockTask(id string) (restock.Task, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var task restock.Task
	tx, err := s.db.Begin()
	if err != nil {
		return task, false, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, id).Scan(&raw); err != nil {
		return task, false, err
	}
	if err = json.Unmarshal([]byte(raw), &task); err != nil {
		return task, false, err
	}
	if task.StoppedAt != nil {
		return task, false, nil
	}
	if task.ManagedExternally || !task.Active() {
		return task, false, ErrProRestockStopConflict
	}
	if err = tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, task.Email).Scan(&raw); err != nil {
		return task, false, err
	}
	var p model.MailAccountProfile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return task, false, err
	}
	if p.ManagementScope != "pro" || p.ProRestockTaskID != task.ID || p.ProWorkflowRunning || p.DispositionStarted() || p.SpaceMergedOnce {
		return task, false, ErrProRestockStopConflict
	}
	now := time.Now()
	p.ProAuto.StopVersion++
	p.ProAuto.StopRequested = true
	p.ProAuto.Status = "stopped"
	p.ProAuto.NextCheckAt = nil
	p.ProAuto.Error = "用户已停止本次补货"
	p.ProAuto.UpdatedAt = now
	p.UpdatedAt = now
	task.StoppedFailure = task.Status == "attention" || task.Status == "unknown"
	task.StoppedAt, task.UpdatedAt = &now, now
	task.Status, task.ETA, task.NextCardCheckAt = "cancelled", nil, nil
	task.Message = "已手动停止本次补货；自动补货将在监控刷新后按当前规则重新判断"
	if task.SubmittedAt == nil {
		task.Cost = 0
	} else {
		task.Message += "；已提交的开卡或付款记录保留核验"
	}
	if _, err = tx.Exec(`UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?`, mustJSON(p), formatTime(now), p.Email); err != nil {
		return task, false, err
	}
	if _, err = tx.Exec(`UPDATE pro_restock_tasks SET status=?,cost=?,payload=? WHERE id=?`, task.Status, task.Cost, mustJSON(task), task.ID); err != nil {
		return task, false, err
	}
	if _, err = tx.Exec(`DELETE FROM pro_restock_replacements WHERE task_id=?`, task.ID); err != nil {
		return task, false, err
	}
	if err = tx.Commit(); err != nil {
		return task, false, err
	}
	return task, true, nil
}
