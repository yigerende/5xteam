package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

const ProRestockStallTimeout = 10 * time.Minute

type restockProgress struct {
	FlowID string          `json:"flow_id"`
	At     time.Time       `json:"at"`
	Seen   map[string]bool `json:"seen"`
}

type ProRestockWatchdogResult struct {
	Task        restock.Task
	CancelEmail string
}

// Only first entry into a stage or a completed milestone is progress. Polling,
// error text, retries and UpdatedAt heartbeats never extend the deadline.
func observeRestockProgress(t restock.Task, p model.MailAccountProfile, old restockProgress, now time.Time) restockProgress {
	fresh := old.Seen == nil || old.FlowID != p.ProAuto.ID
	if fresh {
		old = restockProgress{FlowID: p.ProAuto.ID, At: t.CreatedAt, Seen: map[string]bool{}}
	}
	add := func(key string, at time.Time) {
		if old.Seen[key] {
			return
		}
		old.Seen[key] = true
		if at.IsZero() {
			if !fresh {
				at = now
			} else {
				at = t.CreatedAt
			}
		}
		if at.After(now) {
			at = now
		}
		if at.After(old.At) {
			old.At = at
		}
	}
	if t.SubmittedAt != nil {
		add("submitted", *t.SubmittedAt)
	}
	if p.ProAuto.ID != "" {
		add("flow", p.ProAuto.StartedAt)
	}
	if t.CardID != "" {
		add("card", t.Stages["card:completed"])
	}
	for key, at := range p.ProAuto.StageTimes {
		if strings.HasSuffix(key, ":running") || strings.HasSuffix(key, ":completed") {
			add(key, at)
		}
	}
	for key, status := range p.ProAuto.Steps {
		if status == "completed" {
			add(key+":completed", p.ProAuto.StageTimes[key+":completed"])
		}
	}
	if p.ProAuto.Stage != "" {
		add(p.ProAuto.Stage+":running", p.ProAuto.StageTimes[p.ProAuto.Stage+":running"])
	}
	if old.At.IsZero() {
		old.At = now
	}
	return old
}

func restockFinalCompleted(p model.MailAccountProfile) bool {
	// Old disposition flags cannot finish a freshly restarted provisioning flow.
	switch p.ProAuto.Stage {
	case "login", "recharge", "oauth", "push", "quota":
		return false
	}
	return p.SIWCWorkflowMarker("", 0, time.Time{}, "").Completed
}

// Independent of remote monitoring. Read only indexed active intents, in a
// bounded fair batch; check progress and stop ownership in the same transaction.
// Caller serializes with worker startup and cancels returned workers before
// releasing those gates. Money/card/merchant/OAI facts remain untouched.
func (s *Store) SweepProRestockWatchdog(now time.Time) ([]ProRestockWatchdogResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT t.payload,coalesce(w.payload,'{}') FROM pro_restock_tasks t LEFT JOIN pro_restock_progress w ON w.task_id=t.id WHERE t.status NOT IN ('ready','failed','cancelled') AND json_extract(t.payload,'$.managed_externally') IS NOT 1 AND json_extract(t.payload,'$.stopped_at') IS NULL AND json_extract(t.payload,'$.fulfilled_at') IS NULL ORDER BY coalesce(w.checked_at,0),t.created_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	type entry struct {
		t        restock.Task
		progress restockProgress
	}
	entries := []entry{}
	for rows.Next() {
		var raw, progress string
		var e entry
		if err = rows.Scan(&raw, &progress); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &e.t); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(progress), &e.progress); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []ProRestockWatchdogResult{}
	for _, e := range entries {
		t := e.t
		if !t.Active() || t.StoppedAt != nil || t.ManagedExternally {
			continue
		}
		p, readErr := replacementProfile(tx, t.Email)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return nil, readErr
		}
		owned := readErr == nil && p.ManagementScope == "pro" && p.ProRestockTaskID == t.ID
		if !owned {
			p = model.MailAccountProfile{}
		}
		progress := observeRestockProgress(t, p, e.progress, now)
		completed := owned && restockFinalCompleted(p)
		timedOut := !now.Before(progress.At.Add(ProRestockStallTimeout))
		if !completed && !timedOut {
			_, err = tx.Exec(`INSERT INTO pro_restock_progress(task_id,payload,checked_at) VALUES(?,?,?) ON CONFLICT(task_id) DO UPDATE SET payload=excluded.payload,checked_at=excluded.checked_at`, t.ID, mustJSON(progress), now.UnixMilli())
			if err != nil {
				return nil, err
			}
			continue
		}
		t.StoppedAt, t.UpdatedAt = &now, now
		t.Status, t.ETA, t.NextCardCheckAt = "cancelled", nil, nil
		t.StoppedFailure = !completed
		t.Message = "连续 10 分钟无实质进展，已强制结束本次补货并释放在途；原订单、付款和卡片记录保留"
		if completed {
			t.Message = "全自动最后一步已完成，已自动结束补货占位；收尾及回款独立继续，不计作新增可用容量"
		}
		item := ProRestockWatchdogResult{Task: t}
		if !completed && owned && !(p.PushStatus == "completed" && p.ProAuto.Steps["push"] == "completed") {
			p.ProAuto.StopVersion++
			p.ProAuto.StopRequested = true
			p.ProAuto.Status = "stopped"
			p.ProAuto.NextCheckAt = nil
			p.ProAuto.RetryAt = nil
			p.ProAuto.Error = t.Message
			p.ProAuto.UpdatedAt = now
			if p.ProAuto.Replacement != nil {
				p.ProAuto.Replacement.Pending = false
				p.ProAuto.Replacement.Queued = false
			}
			if p.MergeV2RestartPending() {
				p.ProMergeV2Restart.Status = "cancelled"
			}
			if err = saveReplacementProfile(tx, p); err != nil {
				return nil, err
			}
			item.CancelEmail = t.Email
		}
		if _, err = tx.Exec(`UPDATE pro_restock_tasks SET status=?,payload=? WHERE id=?`, t.Status, mustJSON(t), t.ID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`DELETE FROM pro_restock_progress WHERE task_id=?`, t.ID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, tx.Commit()
}
