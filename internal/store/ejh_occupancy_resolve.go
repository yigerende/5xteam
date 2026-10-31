package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

type EJHOccupancyResolutionInput struct {
	CardID         string `json:"card_id"`
	Email          string `json:"email"`
	ExpectedStatus string `json:"expected_status"`
	Outcome        string `json:"outcome"`
	Evidence       string `json:"evidence"`
	Confirm        bool   `json:"confirm"`
}

// Reconcile one legacy deleted-account order, never all orders of an email.
// The admission lock makes release atomic with card allocation across stores.
func (s *Store) ResolveEJHOccupancy(id string, in EJHOccupancyResolutionInput) error {
	return s.resolveCardOccupancy("ejh", id, in)
}

func (s *Store) ResolveNexovccOccupancy(id string, in EJHOccupancyResolutionInput) error {
	return s.resolveCardOccupancy("nexovcc", id, in)
}

func (s *Store) resolveCardOccupancy(provider, id string, in EJHOccupancyResolutionInput) error {
	in.Evidence = strings.TrimSpace(in.Evidence)
	if !in.Confirm || len([]rune(in.Evidence)) < 5 || len(in.Evidence) > 2000 {
		return fmt.Errorf("请确认已核实原订单，并填写至少 5 个字的核实依据（最多 2000 字节）")
	}
	if in.Outcome != "success_removed" && in.Outcome != "not_activated" {
		return fmt.Errorf("请选择已核实的开通和移出结果，结果不明不能解除占用")
	}
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
	var raw, key, email, status string
	if err = tx.QueryRow(`SELECT o.profile,u.card_key,u.email,u.status FROM gptpay_orders o JOIN pro_card_usage u ON u.order_id=o.id WHERE o.id=?`, id).Scan(&raw, &key, &email, &status); err != nil {
		return fmt.Errorf("原开通订单或占用记录不存在")
	}
	var order gptpay.Order
	if err = json.Unmarshal([]byte(raw), &order); err != nil {
		return err
	}
	var sealed string
	if err = tx.QueryRow(`SELECT encrypted_secret FROM gptpay_cards WHERE id=?`, in.CardID).Scan(&sealed); err != nil {
		return fmt.Errorf("卡片不存在，请刷新后核对")
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return err
	}
	var secret gptpay.CardSecret
	if err = json.Unmarshal([]byte(plain), &secret); err != nil {
		return err
	}
	kind, err := proCardType(tx, key)
	if err != nil {
		return err
	}
	if kind != provider || proCardKey(secret.Number) != key || !strings.EqualFold(email, strings.TrimSpace(in.Email)) || !strings.EqualFold(order.Email, email) {
		return fmt.Errorf("卡片、账号与原订单不匹配，请刷新后核对")
	}
	if order.OccupancyResolution != nil {
		if order.OccupancyResolution.Outcome != in.Outcome {
			return fmt.Errorf("该订单已有不同核实结果，不能覆盖")
		}
		return nil // A retried response must not release a newer order or reservation.
	}
	var exists bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM mail_accounts WHERE email=? COLLATE NOCASE)`, email).Scan(&exists); err != nil {
		return err
	}
	if exists && in.Outcome != "not_activated" {
		return fmt.Errorf("账号仍在管理列表或已重新导入，请通过账号的正常开通 / 合并流程处理")
	}
	if busy, e := caseAccountWorking(tx, email); e != nil {
		return e
	} else if busy {
		return fmt.Errorf("账号仍在执行，请等本轮执行结束后核实")
	}
	if order.Status != in.ExpectedStatus || status != order.Status {
		return fmt.Errorf("订单状态已变化，请重新打开并核实后提交")
	}
	if order.Remote.AutomaticRunning || order.CheckPending || (in.Outcome == "not_activated" && (order.SucceededAt != nil || order.Remote.PaymentState == "succeeded")) {
		return errCaseStillRunning
	}
	switch order.Status {
	case "success", "failed", "manual_review", "submission_unknown":
	default:
		return fmt.Errorf("订单仍在处理中，请先查询供应商结果，不能直接解除")
	}
	target := "failed"
	if in.Outcome == "success_removed" {
		target = "success"
	}
	if (order.Status == "success" || order.Status == "failed") && order.Status != target {
		return fmt.Errorf("核实结果与订单最终状态冲突，不能解除")
	}
	if (order.Remote.Status == "success" || order.Remote.Status == "failed") && order.Remote.Status != target {
		return fmt.Errorf("核实结果与已保存的供应商结果冲突，请先查询原订单")
	}
	before := order
	now := time.Now().UTC()
	order.Status, order.UpdatedAt = target, now
	if target == "failed" {
		order.NextStatusCheckAt = nil
		order.RateLimited = false
		order.CheckPending = false
	}
	order.OccupancyResolution = &gptpay.OccupancyResolution{Outcome: in.Outcome, Evidence: in.Evidence, At: now}
	// Preserve supplier responses, errors, snapshots and credits as original evidence.
	if _, err = tx.Exec(`UPDATE gptpay_orders SET profile=?,active=0 WHERE id=?`, mustJSON(order), id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE pro_card_usage SET status=? WHERE order_id=?`, target, id); err != nil {
		return err
	}
	if provider == "nexovcc" {
		// Mark only this verified order, never all orders belonging to the email.
		if target == "success" {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO pro_card_order_releases(order_id,released_at) VALUES(?,?)`, id, formatTime(now)); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`INSERT INTO nexovcc_audit(profile) VALUES(?)`, mustJSON(map[string]any{"action": "deleted_account_occupancy", "before": before, "after": order, "reason": in.Evidence, "at": now})); err != nil {
			return err
		}
	} else {
		if err = ejhSaveCaseTx(tx, order); err != nil {
			return err
		}
		if target == "success" {
			if err = ejhMarkRequestedTx(tx, email, id, "manual", now); err != nil {
				return err
			}
		}
		if err = s.ejhPutTx(tx, "match_audit", EJHID("occupancy", id), map[string]any{"action": "deleted_account_occupancy", "before": before, "after": order, "reason": in.Evidence, "at": now}); err != nil {
			return err
		}
	}

	return tx.Commit()
}
