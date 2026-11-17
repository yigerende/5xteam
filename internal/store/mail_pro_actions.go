package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Only the authenticated mailbox action handler calls this admission method.
// A normal account import cannot opt itself in or change the pinned final mode.
func (s *Store) EnableMailProActions(email, mode string) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.MailAccountProfile
	email = strings.ToLower(strings.TrimSpace(email))
	if mode != "" && mode != "merge" && mode != "deactivate" && mode != "merchant" && mode != "merge_v2" {
		return p, errors.New("收尾流程无效")
	}
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	// Match mailbox listing/credential reads and persist the canonical scope
	// when a legacy mailbox explicitly opts in to background actions.
	p.ManagementScope = normalizeMailManagementScope(p.ManagementScope)
	if p.ManagementScope != "mail" {
		return p, errors.New("账号不在邮件管理中，请刷新列表")
	}
	if p.ProWorkflowRunning || p.ProAuto.Status == "running" {
		return p, errors.New("账号正在执行操作")
	}
	if p.MailProActions == nil {
		p.MailProActions = &model.MailProActions{EnabledAt: time.Now()}
	}
	if mode != "" {
		if p.MailProActions.FinalMode != "" && p.MailProActions.FinalMode != mode {
			return p, errors.New("已有收尾流程，请续跑原流程")
		}
		p.MailProActions.FinalMode = mode
	}
	p.UpdatedAt = time.Now()
	_, err := s.db.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", mustJSON(p), formatTime(p.UpdatedAt), email)
	return p, err
}

func mailProActionPending(p model.MailAccountProfile) bool {
	if p.ProWorkflowRunning || p.ProAuto.Status == "running" {
		return true
	}
	for _, v := range p.ProOAIConfirmations {
		if v != nil && (v.Status == "running" || v.Status == "waiting" || v.NextAttemptAt != nil) {
			return true
		}
	}
	if p.ProAuto.StopRequested {
		return false
	}
	if p.MerchantNeedsContinuation() || p.DeletionNeedsContinuation() || p.MergeV2Started() && !p.MergeV2Completed() {
		return true
	}
	return false
}
