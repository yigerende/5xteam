package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// A failed payment ends the order's occupancy, but not its running workflow.
// Convert that occupancy back to a reservation in the same transaction. This
// closes the gap before retry preparation, including the configured retry wait.
// Worker cleanup (or process-start recovery) releases the reservation on exit.
func holdFailedProAutoCard(tx *sql.Tx, previous, order gptpay.Order) error {
	if order.Status != "failed" || !previous.Active() || order.SucceededAt != nil {
		return nil
	}
	var raw string
	err := tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, order.Email).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var p model.MailAccountProfile
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	a := p.ProAuto
	if p.IsProDead() || p.ManagementScope != "pro" || a.Status != "running" || a.ID == "" || a.CardID != order.CardID || a.PaymentOrderID() != order.ID || (a.OrderID != "" && a.OrderID != order.ID) {
		return nil
	}
	_, err = tx.Exec(`INSERT INTO pro_card_reservations(email,card_key)
	 SELECT email,card_key FROM pro_card_usage WHERE order_id=?
	 ON CONFLICT(email) DO NOTHING`, order.ID)
	return err
}

// The caller keeps the original account lock and worker slot throughout the
// wait. Verify payment history and advance the attempt in one transaction.
func (s *Store) PrepareProAutoRetry(email string, expected model.ProAutoState, limit int, at time.Time, message string) (model.MailAccountProfile, error) {
	return s.prepareProAutoRetry(email, expected, limit, at, message, proLoginRetry)
}

func (s *Store) PrepareProAutoProxyRetry(email string, expected model.ProAutoState, limit int, at time.Time, message string) (model.MailAccountProfile, error) {
	return s.prepareProAutoRetry(email, expected, limit, at, message, proProxyRetry)
}

func (s *Store) PrepareProSessionRecovery(email string, expected model.ProAutoState, message string) (model.MailAccountProfile, error) {
	return s.prepareProAutoRetry(email, expected, 1, time.Time{}, message, proSessionRecovery)
}

type proRetryKind int

const (
	proLoginRetry proRetryKind = iota
	proProxyRetry
	proSessionRecovery
)

func (s *Store) prepareProAutoRetry(email string, expected model.ProAutoState, limit int, at time.Time, message string, kind proRetryKind) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.MailAccountProfile
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, email).Scan(&raw); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	a := p.ProAuto
	attempts := a.RetryAttempts
	switch kind {
	case proSessionRecovery:
		if a.SessionRecoveryOrderID != "" || a.Stage != "recharge" || a.OrderID == "" || a.OrderID != a.PaymentOrderID() || a.PaymentReviewRequired {
			return p, errors.New("序付订单无法重新登录，或本次机会已使用")
		}
		attempts = 0
	case proProxyRetry:
		attempts = a.ProxyRetryAttempts
		if a.Stage != "login" || a.OrderID != "" || a.ProxyRetryAttempts != expected.ProxyRetryAttempts {
			return p, errors.New("代理重试仅允许尚未付款的登录阶段")
		}
	}
	if p.IsProDead() || p.ManagementScope != "pro" || a.ID == "" || a.ID != expected.ID || a.OrderID != expected.OrderID || a.StopVersion != expected.StopVersion || a.RetryAttempts != expected.RetryAttempts || a.SessionRecoveryOrderID != expected.SessionRecoveryOrderID || a.StopRequested || a.Status != "running" || p.ProWorkflowRunning || p.SpaceMergedOnce {
		return p, errors.New("账号流程已变化或停止")
	}
	if attempts >= limit || (a.Stage != "login" && a.Stage != "recharge") || a.Steps["recharge"] == "completed" || a.Steps["oauth"] == "completed" || p.PushStatus == "completed" {
		return p, errors.New("重试次数已用尽或已进入开通后的流程")
	}
	rows, err := tx.Query(`SELECT profile FROM gptpay_orders WHERE email=? ORDER BY created_at DESC,id DESC`, email)
	if err != nil {
		return p, err
	}
	linked, first := a.OrderID == "", true
	for rows.Next() {
		var order gptpay.Order
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &order)
		}
		if err != nil {
			break
		}
		if kind == proSessionRecovery && first && (order.ID != a.OrderID || order.Email != email || order.CardID != a.CardID || order.PlanCode != a.PlanCode || !order.CanReloginXUPaySession()) {
			err = errors.New("缺少序付原单未付款且 Session 已失效的确认")
			break
		}
		if order.Status != "failed" || order.SucceededAt != nil {
			err = errors.New("已有成功或待确认订单，禁止重新付款")
			break
		}
		if first && order.PaymentReviewRequired() {
			err = errors.New(gptpay.PaymentReviewMessage)
			break
		}
		if first && order.RetryBlocked() {
			err = errors.New("供应商禁止重试，请先核查原订单")
			break
		}
		first = false
		linked = linked || order.ID == a.OrderID
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return p, err
	}
	if !linked {
		return p, errors.New("原订单无法核实，禁止重新付款")
	}
	// Keep the account, card, batch, original start time and policy snapshots.
	switch kind {
	case proSessionRecovery:
		p.ProAuto.SessionRecoveryOrderID = a.OrderID
	case proProxyRetry:
		p.ProAuto.ProxyRetryAttempts++
	default:
		p.ProAuto.RetryAttempts++
	}
	if kind != proSessionRecovery {
		p.ProAuto.RetryAt = &at
	}
	p.ProAuto.OrderID, p.ProAuto.ExitIP = "", ""
	p.ProAuto.Stage, p.ProAuto.Error = "login", message
	p.ProAuto.Steps = map[string]string{"login": "pending"}
	p.ProAuto.StageTimes = map[string]time.Time{}
	p.ProAuto.UpdatedAt, p.UpdatedAt = time.Now(), time.Now()
	p.ProManualStages, p.ProStageOverrides = nil, nil
	if _, err = tx.Exec(`UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?`, mustJSON(p), formatTime(p.UpdatedAt), email); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
