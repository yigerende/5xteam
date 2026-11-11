package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// ReissueProMerchant is called under the account execution lock after querying
// the original merchant job. Archive and rotate the key in one transaction so
// concurrent clicks and retries of a lost HTTP response cannot create two jobs.
func (s *Store) ReissueProMerchant(email, expectedRequestID string, expectedStopVersion uint64) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	var p model.MailAccountProfile
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	if !p.ProFinalStepAllowed("merchant") || p.IsProDead() || p.ProWorkflowRunning || p.MergeV2RestartPending() || (p.MergeV2Started() && !p.MergeV2Completed()) {
		return p, errors.New("该账号当前不能重新提交商户，请先刷新进度")
	}
	if expectedRequestID == "" || !p.MerchantCanReissue() || p.ProMerchant.RequestID != expectedRequestID {
		return p, errors.New("原申请不是已确认失败的订单，或已重新提交，请刷新进度")
	}
	if p.ProAuto.StopVersion != expectedStopVersion {
		return p, errors.New("重试期间流程已被停止，请刷新后再操作")
	}
	var requestID, encrypted string
	if err := s.db.QueryRow("SELECT request_id,encrypted_submission FROM pro_merchant_requests WHERE email=?", email).Scan(&requestID, &encrypted); err != nil {
		return p, err
	}
	if requestID != expectedRequestID || encrypted == "" {
		return p, errors.New("商户申请快照不一致，未重新提交")
	}
	var charged int
	if err := s.db.QueryRow("SELECT count(*) FROM pro_merchant_fees WHERE request_id=?", requestID).Scan(&charged); err != nil {
		return p, err
	}
	if charged != 0 {
		return p, errors.New("原申请已有成功费用记录，不能重新发单")
	}
	previous := *p.ProMerchant
	now := time.Now().UTC()
	// Retain the original supplier, credentials, cost binding and completed
	// downstream cleanup. Only this merchant attempt receives a new identity.
	p.ProMerchant = &model.ProMerchant{
		Provider: previous.Provider, SuccessFeeUSD: previous.SuccessFeeUSD,
		FeeOrderID: previous.FeeOrderID, FeeCardID: previous.FeeCardID, FeeIssuer: previous.FeeIssuer,
		RetiredAt: previous.RetiredAt, RequestID: "space-" + rand.Text(), Status: "pending",
		Message: "已确认原单失败，等待重新提交商户", StartedAt: now, UpdatedAt: now, NextCheckAt: &now,
	}
	p.ProAuto.StopRequested = false
	if p.ProMigration != nil {
		p.ProMigration.Paused = false
	}
	p.ProLastError = ""
	p.UpdatedAt = now
	p.ReconcileProMerchant(now)
	event := model.AutoRotationEvent{
		ID: "merchant-reissue:" + p.ID + ":" + requestID, AccountID: p.ID, Email: p.Email,
		Type: "pro_merchant_retry", Source: "pro_management", Operation: "merchant", Stage: "retry",
		Message: "手动重新发单，已归档上一笔商户申请", CreatedAt: now,
		Details: map[string]any{"previous_merchant": previous, "request_id": p.ProMerchant.RequestID},
	}
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO auto_rotation_events(id,run_id,task_id,account_id,payload,created_at) VALUES(?,?,?,?,?,?)", event.ID, "", "", p.ID, mustJSON(event), formatTime(now)); err != nil {
		return p, err
	}
	if _, err = tx.Exec("UPDATE pro_merchant_requests SET request_id=? WHERE email=?", p.ProMerchant.RequestID, email); err != nil {
		return p, err
	}
	if _, err = tx.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", mustJSON(p), formatTime(now), email); err != nil {
		return p, err
	}
	return p, tx.Commit()
}
