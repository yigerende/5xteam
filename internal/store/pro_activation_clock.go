package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Record the clock in the payment transaction, before OAuth or push can fail.
// This is one primary-key account lookup, never a history scan or remote call.
func captureProActivationClockTx(tx *sql.Tx, order gptpay.Order) error {
	if order.Status != "success" || order.SucceededAt == nil || order.SucceededAt.IsZero() {
		return nil
	}
	var raw string
	if err := tx.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", order.Email).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	if p.ManagementScope != "pro" || !proClockOrderMatches(p, order.ID) || !p.StartProPushClock(*order.SucceededAt, "activation") {
		return nil
	}
	now := formatTime(time.Now())
	_, err := tx.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_push_started_at',?,'$.pro_push_started_source','activation','$.updated_at',?),updated_at=? WHERE email=?`, formatTime(*p.ProPushStartedAt), now, now, order.Email)
	return err
}

func proClockOrderMatches(p model.MailAccountProfile, orderID string) bool {
	if p.ProAuto.OrderID != "" {
		return p.ProAuto.OrderID == orderID
	}
	return p.ProAuto.ID == "" || p.ProAuto.PaymentOrderID() == orderID
}

// Recover a missing clock when an existing paid workflow is continued. Already
// recorded clocks (including historical first-push clocks) remain untouched.
func (s *Store) recoverProActivationClockLocked(p *model.MailAccountProfile) error {
	var raw string
	orderID := p.ProAuto.OrderID
	if orderID == "" {
		orderID = p.ProAuto.PaymentOrderID()
	}
	query := `SELECT profile FROM gptpay_orders WHERE email=? AND json_extract(profile,'$.status')='success'`
	args := []any{p.Email}
	if orderID != "" {
		query += " AND id=?"
		args = append(args, orderID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT 1"
	err := s.db.QueryRow(query, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var order gptpay.Order
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		return err
	}
	// Use the saved completion timestamp for legacy orders lacking succeeded_at.
	at := p.ProAuto.StageTimes["recharge:completed"]
	if order.SucceededAt != nil && !order.SucceededAt.IsZero() {
		at = *order.SucceededAt
	} else if at.IsZero() {
		at = order.UpdatedAt
	}
	p.StartProPushClock(at, "activation")
	return nil
}
