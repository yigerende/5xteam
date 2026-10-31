package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

var errXUPayRateLimited = errors.New("序付接口限流，保留原任务等待或核实，不消耗换号次数")

func replacementProfile(tx *sql.Tx, email string) (p model.MailAccountProfile, err error) {
	var raw string
	if err = tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, email).Scan(&raw); err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	return
}

func saveReplacementProfile(tx *sql.Tx, p model.MailAccountProfile) error {
	p.UpdatedAt = time.Now()
	_, err := tx.Exec(`UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?`, mustJSON(p), formatTime(p.UpdatedAt), p.Email)
	return err
}

// An uncertain order is retained, including its card occupancy, while another
// account may be tried. A confirmed success must never create a successor.
func replacementPayment(tx *sql.Tx, p model.MailAccountProfile) (order gptpay.Order, err error) {
	id := p.ProAuto.OrderID
	if id == "" {
		id = p.ProAuto.PaymentOrderID()
	}
	var raw string
	err = tx.QueryRow(`SELECT profile FROM gptpay_orders WHERE id=? AND email=?`, id, p.Email).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) && p.ProAuto.OrderID == "" {
		return order, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &order)
	}
	if err == nil && (order.Status == "success" || order.SucceededAt != nil) {
		err = errors.New("原订单已开通成功，不再换号")
	}
	if err == nil && order.Provider == gptpay.XUPay && order.RateLimited {
		err = errXUPayRateLimited
	}
	return
}

// Persist the intent before the worker releases its account lock. No error
// text allowlist: every failed step-two execution uses the same bounded rule.
func (s *Store) QueueProRechargeReplacement(email, flowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := replacementProfile(tx, email)
	if err != nil {
		return err
	}
	a, r := &p.ProAuto, p.ProAuto.Replacement
	if a.ID != flowID || a.StopRequested || a.Stage != "recharge" || a.Status != "failed" || a.Steps["recharge"] == "completed" || a.Steps["oauth"] == "completed" || r == nil || r.NextEmail != "" || r.Pending {
		return nil
	}
	order, err := replacementPayment(tx, p)
	if errors.Is(err, errXUPayRateLimited) {
		r.Pending = false
		r.Message = err.Error()
		if err = saveReplacementProfile(tx, p); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	a.PaymentReviewRequired = order.PaymentReviewRequired()
	if !r.CanContinue() {
		r.Message = "第二步失败，主供应商换号次数已用尽，未启用备用尝试或未配置备用供应商"
		if r.ProviderSwitched {
			r.Message = fmt.Sprintf("切换供应商后账号尝试次数已用尽（%d/%d），整条任务结束", r.Attempts-r.Limit, r.FailoverLimit)
		}
	} else {
		r.Pending = true
		r.Message = fmt.Sprintf("第二步失败，等待领取新账号从登录开始（下次 %d/%d）", r.Attempts+1, r.Limit)
		if r.Attempts >= r.Limit {
			r.Message = fmt.Sprintf("主供应商本轮次数已用尽，等待切换或继续备用供应商账号（%d/%d）", r.Attempts-r.Limit+1, r.FailoverLimit)
		}
	}
	if err = saveReplacementProfile(tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

// Claim the next unused account, both sides of the link, and (when applicable)
// a new budgeted restock task atomically. Replays return the existing successor.
func (s *Store) ClaimProRechargeReplacement(email string) (next model.MailAccountProfile, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return next, err
	}
	defer tx.Rollback()
	p, err := replacementProfile(tx, email)
	if err != nil {
		return next, err
	}
	r := p.ProAuto.Replacement
	if r == nil {
		return next, errors.New("没有换号任务")
	}
	if r.NextEmail != "" {
		return replacementProfile(tx, r.NextEmail)
	}
	root, err := replacementProfile(tx, r.RootEmail)
	if err != nil {
		return next, err
	}
	if !r.Pending || !r.CanContinue() || p.ProAuto.StopRequested || root.ProAuto.StopRequested {
		return next, errors.New("换号任务已停止或次数已用尽")
	}
	if _, err = replacementPayment(tx, p); err != nil {
		return next, err
	}
	var task *restock.Task
	if p.ProRestockTaskID != "" {
		var raw string
		if err = tx.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, p.ProRestockTaskID).Scan(&raw); err != nil {
			return next, err
		}
		task = &restock.Task{}
		if err = json.Unmarshal([]byte(raw), task); err != nil {
			return next, err
		}
		c, e := readRestockConfig(tx)
		if e != nil {
			return next, e
		}
		if !c.Enabled || c.Mode != "auto" || c.Version != task.Version || task.StoppedAt != nil {
			return next, errors.New("补货配置已变化或任务停止，等待处理")
		}
		if reason := c.NoRestockReason(time.Now()); reason != "" {
			return next, errors.New(reason)
		}
		if err = takeoverPurchaseGate(tx, task.TakeoverToken, time.Now()); err != nil {
			return next, err
		}
		l, e := restockLimits(tx, time.Now())
		if e != nil {
			return next, e
		}
		var active int
		if err = tx.QueryRow(`SELECT count(*) FROM pro_restock_tasks WHERE id<>? AND status NOT IN ('ready','failed','cancelled')`, task.ID).Scan(&active); err != nil {
			return next, err
		}
		if active >= c.MaxInflight || l.HourCount >= c.HourLimit || l.DayCount >= c.DayLimit || c.HourBudget > 0 && l.HourCost+c.UnitCost > c.HourBudget || c.DayBudget > 0 && l.DayCost+c.UnitCost > c.DayBudget {
			return next, errors.New("等待补货名额或预算释放后换号")
		}
		task.Cost = c.UnitCost
	}
	var nextEmail string
	if err = tx.QueryRow(proScheduleCandidateSQL, 1).Scan(&nextEmail); errors.Is(err, sql.ErrNoRows) {
		return next, errors.New("没有未使用的新账号，等待补充账号后继续")
	} else if err != nil {
		return next, err
	}
	if next, err = replacementProfile(tx, nextEmail); err != nil {
		return next, err
	}
	nr := *r
	if nr.Attempts >= nr.Limit && !nr.ProviderSwitched {
		if err = s.switchRechargeProvider(tx, &nr); err != nil {
			return next, err
		}
	}
	nr.PreviousEmail, nr.NextEmail, nr.Pending, nr.Queued = email, "", false, true
	nr.Attempts++
	nr.Message = fmt.Sprintf("第二步失败换号 %d/%d：从登录开始", nr.Attempts, nr.Limit)
	if nr.ProviderSwitched {
		nr.Message = fmt.Sprintf("已切换至 %s，账号尝试 %d/%d：从登录开始", nr.Payment.Provider, nr.Attempts-nr.Limit, nr.FailoverLimit)
	}
	now := time.Now()
	next.ProAuto = model.ProAutoState{Status: "replacement_queued", Stage: "login", StartedAt: now, UpdatedAt: now, Steps: map[string]string{"login": "pending"}, Replacement: &nr, RestockBinding: p.ProAuto.RestockBinding, Sub2PushGroupIDs: append([]int64(nil), p.ProAuto.Sub2PushGroupIDs...)}
	next.ProRestockTargetID = p.ProRestockTargetID
	next.TargetAdminID, next.TargetSeatType = p.TargetAdminID, p.TargetSeatType
	if task != nil {
		oldID := task.ID
		t := restock.Task{ID: "recharge:" + p.ProAuto.ID, DecisionID: task.DecisionID, Email: next.Email, Version: task.Version, GroupID: task.GroupID, Binding: task.Binding, Plan: task.Plan, Status: "reserved", Stage: "queued", Cost: task.Cost, CreatedAt: now, UpdatedAt: now, Stages: map[string]time.Time{"reserved": now}, CardProvider: task.CardProvider, StrictCardProvider: task.StrictCardProvider, PurchaseRoute: task.PurchaseRoute, TakeoverToken: task.TakeoverToken, Capacity: task.Capacity, ForceMergeTracked: task.ForceMergeTracked, ForceMergeAssigned: task.ForceMergeAssigned, ForceMergeSource: task.ForceMergeSource, EmptyGroupStartup: task.EmptyGroupStartup, Message: nr.Message}
		if nr.Payment != nil {
			t.Plan = fmt.Sprintf("%s:%s:%s:%d", nr.Payment.Provider, nr.Payment.PlanCode, nr.Payment.Country, nr.Payment.CreditQuantity)
			t.PurchaseRoute = nr.PurchaseRoute
		}
		next.ProRestockTaskID = t.ID
		if _, err = tx.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, t.ID, t.Email, t.Status, formatTime(t.CreatedAt), t.Cost, mustJSON(t)); err != nil {
			return next, err
		}
		if _, err = tx.Exec(`UPDATE pro_restock_replacements SET task_id=? WHERE task_id=?`, t.ID, oldID); err != nil {
			return next, err
		}
	}
	r.NextEmail, r.Pending = next.Email, false
	r.Message = fmt.Sprintf("已衔接至 %s：%s", next.Email, nr.Message)
	if err = saveReplacementProfile(tx, p); err != nil {
		return next, err
	}
	if err = saveReplacementProfile(tx, next); err != nil {
		return next, err
	}
	return next, tx.Commit()
}

func (s *Store) ProRechargeReplacementWork() ([]model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT profile FROM mail_accounts WHERE coalesce(json_extract(profile,'$.pro_auto.stop_requested'),0)=0 AND (json_extract(profile,'$.pro_auto.replacement.pending')=1 OR json_extract(profile,'$.pro_auto.replacement.queued')=1 OR (json_type(profile,'$.pro_auto.replacement')='object' AND json_extract(profile,'$.pro_auto.status')='failed' AND json_extract(profile,'$.pro_auto.stage')='recharge' AND coalesce(json_extract(profile,'$.pro_auto.replacement.next_email'),'')='' AND (json_extract(profile,'$.pro_auto.replacement.attempts')<json_extract(profile,'$.pro_auto.replacement.limit') OR (json_type(profile,'$.pro_auto.replacement.payment')='object' AND coalesce(json_extract(profile,'$.pro_auto.replacement.backup_provider'),'')<>'' AND json_extract(profile,'$.pro_auto.replacement.attempts')<json_extract(profile,'$.pro_auto.replacement.limit')+coalesce(json_extract(profile,'$.pro_auto.replacement.failover_limit'),0))))) ORDER BY updated_at,email`)
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

// Called under the automation registry lock so a concurrent start either is
// cancelled by the caller or observes the durable stop before submitting work.
func (s *Store) StopProRechargeReplacementChain(email string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := replacementProfile(tx, email)
	if err != nil {
		return nil, err
	}
	if p.ProAuto.Replacement == nil {
		return nil, nil
	}
	rows, err := tx.Query(`SELECT profile FROM mail_accounts WHERE json_extract(profile,'$.pro_auto.replacement.root_email')=?`, p.ProAuto.Replacement.RootEmail)
	if err != nil {
		return nil, err
	}
	var profiles []model.MailAccountProfile
	for rows.Next() {
		var raw string
		var member model.MailAccountProfile
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &member); err != nil {
			break
		}
		profiles = append(profiles, member)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	var emails []string
	for _, member := range profiles {
		member.ProAuto.StopRequested = true
		member.ProAuto.StopVersion++
		member.ProAuto.Replacement.Pending = false
		member.ProAuto.Replacement.Queued = false
		member.ProAuto.Replacement.Message = "用户已停止整条换号流程"
		member.ProAuto.NextCheckAt = nil
		if member.ProAuto.Replacement.NextEmail == "" {
			member.ProAuto.Status = "stopped"
		}
		if err = saveReplacementProfile(tx, member); err != nil {
			return nil, err
		}
		emails = append(emails, member.Email)
	}
	return emails, tx.Commit()
}
