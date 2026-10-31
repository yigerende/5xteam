package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

var errCaseStillRunning = errors.New("原订单仍在执行或已有付款成功证据，本次未删除；请等待执行结束后处理")

func caseAccountWorking(tx *sql.Tx, email string) (bool, error) {
	var raw string
	err := tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=? COLLATE NOCASE`, email).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		var p model.MailAccountProfile
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return false, err
		}
		if p.ProAuto.Status == "running" || p.ProWorkflowRunning {
			return true, nil
		}
		for _, stage := range p.ProManualStages {
			if stage.Status == "running" {
				return true, nil
			}
		}
	}
	var working bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pro_restock_tasks WHERE email=? COLLATE NOCASE AND status NOT IN ('ready','failed','cancelled'))`, email).Scan(&working)
	return working, err
}

// Deleting an unopened return record is the operator's explicit reconciliation.
// Release exactly its order; preserve card history, other orders and credentials.
func (s *Store) releaseUnopenedCaseTx(tx *sql.Tx, provider, id string, now time.Time) error {
	var raw string
	if err := tx.QueryRow(`SELECT profile FROM gptpay_orders WHERE id=?`, id).Scan(&raw); err != nil {
		return err
	}
	var order gptpay.Order
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		return err
	}
	if order.OccupancyResolution != nil || order.Status == "success" {
		return nil
	}
	if order.SucceededAt != nil || order.Remote.PaymentState == "succeeded" || order.Remote.Status == "success" || order.Remote.AutomaticRunning || order.CheckPending {
		return errCaseStillRunning
	}
	switch order.Status {
	case "failed", "manual_review", "submission_unknown":
	default:
		return errCaseStillRunning
	}
	working, err := caseAccountWorking(tx, order.Email)
	if err != nil {
		return err
	}
	if working {
		return errCaseStillRunning
	}
	before := order
	order.Status = "failed"
	order.UpdatedAt = now
	order.NextStatusCheckAt = nil
	order.CheckPending = false
	order.RateLimited = false
	order.OccupancyResolution = &gptpay.OccupancyResolution{Outcome: "not_activated", Evidence: "操作员在账号回款中确认删除未开通记录，已核实未开通并解除该订单占用", At: now}
	if _, err = tx.Exec(`UPDATE gptpay_orders SET profile=?,active=0 WHERE id=?`, mustJSON(order), id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE pro_card_usage SET status='failed' WHERE order_id=?`, id); err != nil {
		return err
	}
	if err = ejhSaveCaseTx(tx, order); err != nil {
		return err
	}
	audit := map[string]any{"action": "delete_unopened_release", "order_id": id, "before": before, "after": order, "at": now}
	if provider == "ejh" {
		return s.ejhPutTx(tx, "match_audit", EJHID("case-release", id), audit)
	}
	if provider != "nexovcc" {
		return fmt.Errorf("卡台类型无效")
	}
	_, err = tx.Exec(`INSERT INTO nexovcc_audit(profile) VALUES(?)`, mustJSON(audit))
	return err
}

// Repair only existing explicit return-list deletion markers. Merely deleting
// an account is not a reconciliation instruction. Live/paid records are skipped.
func (s *Store) reconcileDeletedUnopenedCases() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockProCardAdmission(tx); err != nil {
		return err
	}
	cases, err := s.ejhCasesQuery(tx, nil)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range cases {
		if c.CardType != "ejh" || c.State != "unopened" || c.RemovedAt == nil || c.Charge != nil || c.OpenedAt != nil || len(c.Returns) > 0 {
			continue
		}
		if err = s.releaseUnopenedCaseTx(tx, "ejh", c.ID, now); err != nil && !errors.Is(err, errCaseStillRunning) {
			return err
		}
	}
	nex, err := s.nexovccCasesQuery(tx)
	if err != nil {
		return err
	}
	for _, c := range nex {
		if !c.canRemoveUnopened() || c.RemovedAt == "" {
			continue
		}
		if err = s.releaseUnopenedCaseTx(tx, "nexovcc", c.ID, now); err != nil && !errors.Is(err, errCaseStillRunning) {
			return err
		}
	}
	return tx.Commit()
}
