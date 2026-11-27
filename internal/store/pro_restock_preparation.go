package store

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Caller excludes live preparation and account workers. Recheck the durable
// intent and all possible issuance/payment evidence under the admission lock.
func (s *Store) FinishUnsubmittedRestockPreparation(expected *restock.Task) (bool, error) {
	return s.settleUnsubmittedRestockPreparation(expected, nil)
}

// A resource wait retains the account, batch and budget, and does not create
// another failure or consume another replacement. The same evidence checks as
// failed preparation apply before returning the task to the dispatch queue.
func (s *Store) WaitUnsubmittedRestockPreparation(expected *restock.Task, next time.Time) (bool, error) {
	return s.settleUnsubmittedRestockPreparation(expected, &next)
}

func (s *Store) settleUnsubmittedRestockPreparation(expected *restock.Task, next *time.Time) (bool, error) {
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
	var current restock.Task
	var raw string
	err = tx.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, expected.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = json.Unmarshal([]byte(raw), &current); err != nil {
		return false, err
	}
	if current.UpdatedAt != expected.UpdatedAt && !current.UpdatedAt.Equal(expected.UpdatedAt) {
		return false, nil
	}
	if !current.Active() || current.Status == "reserved" || current.StoppedAt != nil || (current.CardProvider != "nexovcc" && current.CardProvider != "ejh") || current.OrderID != "" || current.CardID != "" {
		return false, nil
	}
	if err = tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, current.Email).Scan(&raw); err != nil {
		return false, err
	}
	var p model.MailAccountProfile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return false, err
	}
	if p.ProAuto.ID != "" || p.ProRestockTaskID != current.ID || p.PushStatus == "completed" || p.DispositionStarted() || p.SpaceMergedOnce {
		return false, nil
	}
	var unsafe bool
	if next != nil {
		// Only brand-new preparation may be queued again. A pre-existing order,
		// even one edited locally to failed, must keep its own recovery path.
		if p.ProAuto.StopRequested {
			return false, nil
		}
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM gptpay_orders WHERE email=?)`, current.Email).Scan(&unsafe); err != nil || unsafe {
			return false, err
		}
	}
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM gptpay_orders WHERE email=? AND (json_extract(profile,'$.status')<>'failed' OR json_extract(profile,'$.succeeded_at') IS NOT NULL)) OR EXISTS(SELECT 1 FROM nexovcc_operations WHERE json_extract(profile,'$.restock_task_id')=? AND (status<>'failed' OR coalesce(json_extract(profile,'$.serial_no'),'')<>'' OR coalesce(json_extract(profile,'$.card_id'),'')<>'')) OR EXISTS(SELECT 1 FROM pro_card_reservations WHERE email=?)`, current.Email, current.ID, current.Email).Scan(&unsafe)
	if err != nil || unsafe {
		return false, err
	}
	// Include both issuers: a configured balance fallback may have already
	// issued a card on the other platform. Legacy EJH operations lack a task
	// link, so conservatively retain any possible automatic issuance since
	// reservation rather than treating an unknown charge as a safe failure.
	rows, err := tx.Query(`SELECT r.sealed FROM ejh_records r WHERE r.kind='operation' AND r.id IN (
SELECT id FROM ejh_open_index WHERE task_id=?
UNION SELECT id FROM ejh_open_index WHERE task_id='' AND (created_at>=? OR created_at=?))`, current.ID, formatTime(current.CreatedAt), formatTime(time.Time{}))
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var sealed string
		if err = rows.Scan(&sealed); err != nil {
			break
		}
		var clear string
		clear, err = s.decrypt(sealed)
		if err != nil {
			break
		}
		var op ejh.Operation
		if err = json.Unmarshal([]byte(clear), &op); err != nil {
			break
		}
		if op.Method != "open" {
			continue
		}
		matching := op.RestockTaskID == current.ID || (op.RestockTaskID == "" && (op.AccountEmail == "" || op.AccountEmail == current.Email) && op.Automatic && (op.CreatedAt.IsZero() || !op.CreatedAt.Before(current.CreatedAt)))
		if matching && (op.Status != "failed" || op.ErrorCode == "" || op.CardID != "" || op.OrderNo != "" || op.Result != nil) {
			unsafe = true
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || unsafe {
		return false, err
	}
	current.Stage, current.ETA, current.UpdatedAt = "card", nil, time.Now()
	if next != nil {
		current.Status, current.SubmittedAt, current.NextCardCheckAt = "reserved", nil, next
		current.Message = expected.Message
		if current.Stages == nil {
			current.Stages = map[string]time.Time{}
		}
		if _, recorded := current.Stages["card:waiting"]; !recorded {
			current.Stages["card:waiting"] = current.UpdatedAt
		}
	} else {
		current.Status, current.Cost = "failed", 0
		current.Message = "开卡准备失败，已核实无开卡或付款提交，释放本次在途：" + current.Message
		if r := p.ProAuto.Replacement; r != nil && r.Queued {
			r.Queued, r.Message = false, current.Message
			p.ProAuto.Status, p.ProAuto.Stage, p.ProAuto.Error = "failed", "card", current.Message
			p.ProAuto.RetryAt = nil
			if err = saveReplacementProfile(tx, p); err != nil {
				return false, err
			}
		}
	}
	if _, err = tx.Exec(`UPDATE pro_restock_tasks SET status=?,cost=?,payload=? WHERE id=?`, current.Status, current.Cost, mustJSON(current), current.ID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	*expected = current
	return true, nil
}
