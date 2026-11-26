package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

type EJHRechargeLedger struct {
	HistoryMonthRecordedAt    *time.Time `json:"history_month_recorded_at,omitempty"`
	HistoryMonth              string     `json:"history_month,omitempty"`
	Amount                    string     `json:"amount"`
	UpdatedAt                 *time.Time `json:"updated_at,omitempty"`
	ExternalTransferAmount    string     `json:"external_transfer_amount,omitempty"`
	ExternalTransferNote      string     `json:"external_transfer_note,omitempty"`
	ExternalTransferUpdatedAt *time.Time `json:"external_transfer_updated_at,omitempty"`
}

var ejhLedgerAmount = regexp.MustCompile(`^\d{1,12}(\.\d{1,2})?$`)

func (s *Store) EJHRechargeLedger() (EJHRechargeLedger, error) {
	var out EJHRechargeLedger
	err := s.EJHGet("recharge_ledger", "manual-usd", &out)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, err
}

func (s *Store) SaveEJHRechargeLedger(amount string) (EJHRechargeLedger, error) {
	var out EJHRechargeLedger
	amount = strings.TrimSpace(amount)
	if amount != "" {
		if !ejhLedgerAmount.MatchString(amount) {
			return out, fmt.Errorf("充值总账须为非负 USD 金额，最多 12 位整数和 2 位小数")
		}
		value, err := ejh.Money(amount)
		if err != nil {
			return out, err
		}
		amount = value.FloatString(2)
	}
	now := time.Now()
	out = EJHRechargeLedger{Amount: amount, UpdatedAt: &now}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var previous EJHRechargeLedger
	if err = s.ejhGetTx(tx, "recharge_ledger", "manual-usd", &previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if previous.Amount == amount {
		return previous, nil
	}
	out = previous
	out.Amount, out.UpdatedAt = amount, &now
	if err = s.ejhPutTx(tx, "recharge_ledger", "manual-usd", out); err != nil {
		return out, err
	}
	before, after := new(big.Rat), new(big.Rat)
	if previous.Amount != "" {
		before.SetString(previous.Amount)
	}
	if amount != "" {
		after.SetString(amount)
	}
	event := EJHRechargeEvent{ID: EJHID("recharge", now.String()), At: now, Before: previous.Amount, After: amount, Delta: new(big.Rat).Sub(after, before).FloatString(2), Baseline: previous.Amount == ""}
	if err = s.ejhPutTx(tx, "finance_recharge", event.ID, event); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type EJHExternalTransferEvent struct {
	EJHRechargeEvent
	Note   string `json:"note,omitempty"`
	Kind   string `json:"kind,omitempty"`
	CardID string `json:"card_id,omitempty"`
}

// Record an accounting exclusion only. No money is moved at the issuer, and
// the recharge principal, recharge fees and original trades remain unchanged.
func (s *Store) SaveEJHExternalTransfer(amount, note string) (EJHRechargeLedger, error) {
	amount, note = strings.TrimSpace(amount), strings.TrimSpace(note)
	if amount == "" {
		amount = "0"
	}
	if !ejhLedgerAmount.MatchString(amount) {
		return EJHRechargeLedger{}, fmt.Errorf("平台外转出须为非负 USD 金额，最多 12 位整数和 2 位小数")
	}
	if len([]rune(note)) > 500 {
		return EJHRechargeLedger{}, fmt.Errorf("平台外转出备注最多 500 字")
	}
	value, err := ejh.Money(amount)
	if err != nil {
		return EJHRechargeLedger{}, err
	}
	amount = value.FloatString(2)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return EJHRechargeLedger{}, err
	}
	defer tx.Rollback()
	var out EJHRechargeLedger
	if err = s.ejhGetTx(tx, "recharge_ledger", "manual-usd", &out); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	before := out.ExternalTransferAmount
	if before == "" {
		before = "0.00"
	}
	if before == amount && out.ExternalTransferNote == note {
		return out, nil
	}
	now := time.Now()
	previous, err := ejh.Money(before)
	if err != nil {
		return out, err
	}
	event := EJHExternalTransferEvent{EJHRechargeEvent: EJHRechargeEvent{
		ID: EJHID("external-transfer", now.String()), At: now, Before: before, After: amount,
		Delta: new(big.Rat).Sub(value, previous).FloatString(2), Baseline: out.ExternalTransferUpdatedAt == nil,
	}, Note: note}
	out.ExternalTransferAmount, out.ExternalTransferNote, out.ExternalTransferUpdatedAt = amount, note, &now
	if err = s.ejhPutTx(tx, "recharge_ledger", "manual-usd", out); err != nil {
		return out, err
	}
	if err = s.ejhPutTx(tx, "finance_external_transfer", event.ID, event); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// Assign only the undated historical recharge to a known month. The balance,
// dated recharge events and their audit timestamps are never rewritten.
func (s *Store) SaveEJHRechargeHistoryMonth(month string) (EJHRechargeLedger, error) {
	month = strings.TrimSpace(month)
	if month != "" {
		parsed, err := time.Parse("2006-01", month)
		if err != nil || parsed.Year() < 2000 || parsed.Year() > 2100 {
			return EJHRechargeLedger{}, fmt.Errorf("请输入有效的充值归属月份")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return EJHRechargeLedger{}, err
	}
	defer tx.Rollback()
	var out EJHRechargeLedger
	if err = s.ejhGetTx(tx, "recharge_ledger", "manual-usd", &out); err != nil {
		return out, err
	}
	out.HistoryMonth = month
	now := time.Now()
	out.HistoryMonthRecordedAt = &now
	if err = s.ejhPutTx(tx, "recharge_ledger", "manual-usd", out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
