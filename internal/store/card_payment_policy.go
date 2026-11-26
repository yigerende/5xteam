package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

// Local accounting metadata is stored separately from issuer refreshes and Pro
// workflows. A policy never supplies refund evidence or changes actual balances.
type CardPaymentPolicy struct {
	Provider  string    `json:"provider"`
	CardID    string    `json:"card_id"`
	Mode      string    `json:"mode"`
	AmountUSD string    `json:"amount_usd"`
	Note      string    `json:"note"`
	Revision  int       `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

var ErrCardPaymentPolicyConflict = errors.New("回款方式已被修改，请刷新后重试")

// Only a completed Pro removal releases a payment-only order. Merely changing
// the accounting classification cannot release an active account or payment.
const paymentOnlyReleasedSQL = `EXISTS(SELECT 1 FROM pro_card_order_releases pr WHERE pr.order_id=u.order_id)
 AND EXISTS(SELECT 1 FROM card_payment_modes pp WHERE pp.mode='payment_only' AND (
 (pp.provider='nexovcc' AND EXISTS(SELECT 1 FROM nexovcc_cards pc WHERE pc.card_key=u.card_key AND pc.id=pp.card_id))
 OR (pp.provider='ejh' AND EXISTS(SELECT 1 FROM ejh_card_links pc WHERE pc.card_key=u.card_key AND pc.card_id=pp.card_id))))`

func (s *Store) CardPaymentOnly(provider, id string) (bool, error) {
	var p CardPaymentPolicy
	err := s.EJHGet("card_payment_policy", provider+":"+id, &p)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return p.Mode == "payment_only", err
}

func (s *Store) CardPaymentPolicies(provider string) ([]CardPaymentPolicy, error) {
	rows, err := EJHRecords[CardPaymentPolicy](s, "card_payment_policy")
	out := []CardPaymentPolicy{}
	for _, row := range rows {
		if row.Provider == provider {
			out = append(out, row)
		}
	}
	return out, err
}

func (s *Store) SaveCardPaymentPolicy(in CardPaymentPolicy, moveExternal string) (CardPaymentPolicy, error) {
	in.Note, in.AmountUSD, moveExternal = strings.TrimSpace(in.Note), strings.TrimSpace(in.AmountUSD), strings.TrimSpace(moveExternal)
	if (in.Provider != "ejh" && in.Provider != "nexovcc") || in.CardID == "" || len(in.CardID) > 200 || in.Revision < 0 || (in.Mode != "normal" && in.Mode != "payment_only") {
		return in, fmt.Errorf("请选择有效卡片及回款方式")
	}
	if len([]rune(in.Note)) > 500 || (in.Mode == "payment_only" && in.Note == "") {
		return in, fmt.Errorf("请填写用途说明，最多500字")
	}
	if in.AmountUSD != "" {
		if !ejhLedgerAmount.MatchString(in.AmountUSD) {
			return in, fmt.Errorf("付款本金须为非负 USD 金额，最多两位小数")
		}
		v, _ := ejh.Money(in.AmountUSD)
		in.AmountUSD = v.FloatString(2)
	}
	if moveExternal == "" {
		moveExternal = "0"
	}
	if !ejhLedgerAmount.MatchString(moveExternal) {
		return in, fmt.Errorf("原平台外转出金额格式无效")
	}
	moved, _ := ejh.Money(moveExternal)
	if moved.Sign() > 0 && (in.Provider != "ejh" || in.Mode != "payment_only") {
		return in, fmt.Errorf("只有 EJH 只付款分类可转入原平台外转出金额")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	var exists int
	if in.Provider == "ejh" {
		err = tx.QueryRow(`SELECT count(*) FROM ejh_records WHERE kind='card' AND id=?`, in.CardID).Scan(&exists)
	} else {
		err = tx.QueryRow(`SELECT count(*) FROM nexovcc_cards WHERE id=?`, in.CardID).Scan(&exists)
	}
	if err != nil {
		return in, err
	}
	if exists == 0 {
		return in, sql.ErrNoRows
	}
	key := in.Provider + ":" + in.CardID
	var old CardPaymentPolicy
	err = s.ejhGetTx(tx, "card_payment_policy", key, &old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return in, err
	}
	// Check revision before any transfer: retrying a saved form cannot deduct twice.
	if old.Revision != in.Revision {
		return in, ErrCardPaymentPolicyConflict
	}
	if moved.Sign() > 0 && old.Mode == "payment_only" {
		return in, fmt.Errorf("已设置只付款的卡片不能再次转入同笔平台外支出")
	}
	now := time.Now()
	if moved.Sign() > 0 {
		var ledger EJHRechargeLedger
		if err = s.ejhGetTx(tx, "recharge_ledger", "manual-usd", &ledger); err != nil {
			return in, err
		}
		before := ledger.ExternalTransferAmount
		if before == "" {
			before = "0.00"
		}
		amount, e := ejh.Money(before)
		if e != nil {
			return in, e
		}
		if amount.Cmp(moved) < 0 {
			return in, fmt.Errorf("转入金额超过现有平台外转出累计")
		}
		after := new(big.Rat).Sub(amount, moved).FloatString(2)
		event := EJHExternalTransferEvent{EJHRechargeEvent: EJHRechargeEvent{ID: EJHID("payment-only-transfer", key+now.String()), At: now, Before: before, After: after, Delta: new(big.Rat).Neg(moved).FloatString(2)}, Note: "重新分类为只付款支出：" + in.CardID, Kind: "payment_only_reclassification", CardID: in.CardID}
		ledger.ExternalTransferAmount, ledger.ExternalTransferUpdatedAt = after, &now
		if err = s.ejhPutTx(tx, "recharge_ledger", "manual-usd", ledger); err != nil {
			return in, err
		}
		if err = s.ejhPutTx(tx, "finance_external_transfer", event.ID, event); err != nil {
			return in, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO card_payment_modes(provider,card_id,mode) VALUES(?,?,?) ON CONFLICT(provider,card_id) DO UPDATE SET mode=excluded.mode`, in.Provider, in.CardID, in.Mode); err != nil {
		return in, err
	}
	in.Revision = old.Revision + 1
	in.UpdatedAt = now
	if err = s.ejhPutTx(tx, "card_payment_policy", key, in); err != nil {
		return in, err
	}
	if err = s.ejhPutTx(tx, "card_payment_policy_audit", fmt.Sprintf("%s:%d", key, in.Revision), map[string]any{"before": old, "after": in, "external_reclassified_usd": moved.FloatString(2), "at": now}); err != nil {
		return in, err
	}
	return in, tx.Commit()
}
