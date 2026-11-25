package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Claim the next retry in the same transaction as paid-order verification.
// Repeated ticks, concurrent callers and service restarts cannot reset it.
func (s *Store) PrepareProOAuthRecovery(email string, expected model.ProAutoState, source string, downstream model.ProDownstreamRef, limit int, next time.Time) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.MailAccountProfile
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	a := p.ProAuto
	if a.ID == "" || a.ID != expected.ID || a.OrderID == "" || a.OrderID != expected.OrderID || a.StopVersion != expected.StopVersion || a.StopRequested || a.Status != "running" || a.Steps["oauth"] == "completed" || p.PushStatus == "completed" || p.Sub2AccountID != 0 || p.IsProDead() || p.DispositionStarted() || p.ProWorkflowRunning || p.SpaceMergedOnce {
		return p, errors.New("账号流程已变化或停止，未恢复 SIWC 授权")
	}
	if err = tx.QueryRow("SELECT profile FROM gptpay_orders WHERE id=? AND email=?", a.OrderID, email).Scan(&raw); err != nil {
		return p, err
	}
	var order gptpay.Order
	if json.Unmarshal([]byte(raw), &order) != nil || order.Status != "success" {
		return p, errors.New("原订单未确认付款成功，不能恢复 SIWC 授权")
	}
	attempts := 0
	if r := a.OAuthRecovery; r != nil {
		if r.OrderID != a.OrderID || r.Source != source || !r.Downstream.Matches(downstream) {
			return p, errors.New("SIWC 恢复授权目标已变化")
		}
		attempts = r.Attempts
	}
	expectedAttempts := 0
	if expected.OAuthRecovery != nil {
		expectedAttempts = expected.OAuthRecovery.Attempts
	}
	if attempts != expectedAttempts {
		return p, errors.New("SIWC 恢复已由其他执行领取")
	}
	attempts = max(attempts, a.RestockRecoveries)
	if previous := a.SIWCConsentFailure; previous != nil && previous.OrderID == a.OrderID {
		attempts = max(attempts, previous.Retries)
	}
	r := &model.ProOAuthRecovery{OrderID: a.OrderID, Source: source, Downstream: downstream, Attempts: attempts, Exhausted: attempts >= limit}
	message := fmt.Sprintf("第三步授权重试次数已耗尽（%d/%d），请手动处理；不重复付款、不自动收尾", attempts, limit)
	if !r.Exhausted {
		r.Attempts++
		r.NextAt = &next
		message = fmt.Sprintf("正在重试第三步授权：第 %d/%d 次，等待 %d 秒；原订单已付款，不重新付款", r.Attempts, limit, max(0, int(time.Until(next).Seconds()+0.5)))
	}
	p.ProAuto.OAuthRecovery = r
	// A challenge recovery must never inherit consent-failure final disposition.
	p.ProAuto.SIWCConsentFailure = nil
	p.ProAuto.Stage, p.ProAuto.Error = "oauth", message
	p.ProAuto.RetryAt = r.NextAt
	p.ProAuto.UpdatedAt, p.UpdatedAt = time.Now(), time.Now()
	if _, err = tx.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", mustJSON(p), formatTime(p.UpdatedAt), email); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
