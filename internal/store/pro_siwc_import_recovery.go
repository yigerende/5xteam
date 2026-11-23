package store

import (
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Claim once and remove only the confirmed unsuccessful request and its unused
// proxy reservation atomically. A restart cannot grant a second recovery.
func (s *Store) PrepareProSIWCImportRecovery(email string, expected model.ProAutoState, id, pending, key string) (model.MailAccountProfile, error) {
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
	if a.ID == "" || a.ID != expected.ID || a.OrderID == "" || a.OrderID != expected.OrderID || a.StopVersion != expected.StopVersion || a.StopRequested || a.Status != "running" || a.Stage != "oauth" || a.Steps["recharge"] != "completed" || a.Steps["oauth"] == "completed" || p.Sub2AccountID > 0 || p.PushStatus == "completed" || p.ManagementScope != "pro" || p.IsProDead() || p.DispositionStarted() || p.ProWorkflowRunning || p.SpaceMergedOnce || p.ProMigration != nil && p.ProMigration.Paused {
		return p, errors.New("账号流程已变化或停止，未清理 SIWC 请求")
	}
	if a.SIWCImportRecoveries >= 1 {
		return p, errors.New("SIWC 重新授权一次后仍失败，请核对下游结果后手动处理")
	}
	if err = tx.QueryRow("SELECT profile FROM gptpay_orders WHERE id=? AND email=?", a.OrderID, email).Scan(&raw); err != nil {
		return p, err
	}
	var order gptpay.Order
	if json.Unmarshal([]byte(raw), &order) != nil || order.Status != "success" {
		return p, errors.New("原订单未确认开通成功，不能恢复 SIWC 授权")
	}
	if err = tx.QueryRow("SELECT encrypted_payload FROM sub2_push_attempts WHERE id=?", id).Scan(&raw); err != nil {
		return p, err
	}
	plain, err := s.decrypt(raw)
	if err != nil {
		return p, err
	}
	if plain != pending || key == "" {
		return p, errors.New("SIWC 待确认请求已变化")
	}
	var completed int
	if err = tx.QueryRow("SELECT count(*) FROM sub2_proxy_binding_events WHERE attempt_key=? AND (counted!=0 OR remote_id>0)", key).Scan(&completed); err != nil {
		return p, err
	}
	if completed != 0 {
		return p, errors.New("SIWC 已有代理绑定结果，不能重新导入")
	}
	if _, err = tx.Exec("DELETE FROM sub2_proxy_binding_events WHERE attempt_key=? AND counted=0", key); err != nil {
		return p, err
	}
	if _, err = tx.Exec("DELETE FROM sub2_push_attempts WHERE id=?", id); err != nil {
		return p, err
	}
	p.ProAuto.SIWCImportRecoveries++
	p.ProAuto.Error = ""
	p.ProAuto.UpdatedAt, p.UpdatedAt = time.Now(), time.Now()
	if _, err = tx.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", mustJSON(p), formatTime(p.UpdatedAt), email); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
