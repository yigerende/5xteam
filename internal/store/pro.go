package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func normalizeProSettings(v *model.ProSettings) {
	v.RechargeFailoverCount = max(0, min(10, v.RechargeFailoverCount))
	v.RechargeReplacementCount = max(0, min(10, v.RechargeReplacementCount))
	v.OAIInquiry = v.OAIInquiry.Normalized()
	if v.OAIConfirmationConcurrency < 1 {
		v.OAIConfirmationConcurrency = 2
	}
	if v.OAIConfirmationConcurrency > 100 {
		v.OAIConfirmationConcurrency = 100
	}
	v.OAIConfirmation = v.OAIConfirmation.Normalized()
	if strings.TrimSpace(v.MerchantAPIURL) == "" {
		v.MerchantAPIURL = refundmerchant.DefaultSubmitURL
	}
	if v.MerchantProvider == "" {
		v.MerchantProvider = refundmerchant.ProviderOriginal
	}
	if strings.TrimSpace(v.MerchantStarBridgeAPIURL) == "" {
		v.MerchantStarBridgeAPIURL = refundmerchant.StarBridgeSubmitURL
	}
	groupNames := make(map[int64]string)
	for index, id := range v.PostMergeGroupIDs {
		if index < len(v.PostMergeGroupNames) && groupNames[id] == "" {
			groupNames[id] = strings.TrimSpace(v.PostMergeGroupNames[index])
		}
	}
	v.PostMergeGroupIDs = uniquePositiveInt64(v.PostMergeGroupIDs)
	v.PostMergeGroupNames = make([]string, len(v.PostMergeGroupIDs))
	for index, id := range v.PostMergeGroupIDs {
		v.PostMergeGroupNames[index] = groupNames[id]
	}
	if v.MaxUnmerged < 1 {
		v.MaxUnmerged = 5
	}
	if v.ScheduleIntervalSeconds < 10 {
		v.ScheduleIntervalSeconds = 120
	}
	v.Provider = strings.ToLower(strings.TrimSpace(v.Provider))
	if v.Provider != "cpa" && v.Provider != "vm2api" {
		v.Provider = "sub2"
	}
	if v.QuotaCheckIntervalSeconds < 10 {
		v.QuotaCheckIntervalSeconds = 120
	}
	v.DeletionCheckIntervalSeconds = int(v.DeletionCheckInterval() / time.Second)
	if v.QuotaUsedThreshold <= 0 || v.QuotaUsedThreshold > 100 || math.IsNaN(v.QuotaUsedThreshold) || math.IsInf(v.QuotaUsedThreshold, 0) {
		v.QuotaUsedThreshold = 100
	}
	if v.TargetSeatType != "prolite" {
		v.TargetSeatType = "default"
	}
	if v.RetryCount < 0 {
		v.RetryCount = 0
	}
	if v.RetryCount > 10 {
		v.RetryCount = 10
	}
	if v.RetryIntervalSeconds < 1 {
		v.RetryIntervalSeconds = 3
	}
	if v.Concurrency < 1 {
		v.Concurrency = 2
	}
	if v.Concurrency > 20 {
		v.Concurrency = 20
	}
	if v.MergeConcurrency < 1 {
		v.MergeConcurrency = 2
	}
	if v.MergeConcurrency > 20 {
		v.MergeConcurrency = 20
	}
	normalizeSub2Settings(&v.Sub2)
	v.Sub2.Provider = v.Provider
	if v.CPA.StatusCheckIntervalSeconds < 10 {
		v.CPA.StatusCheckIntervalSeconds = 120
	}
	if v.CPA.QuotaCheckIntervalSeconds < 10 {
		v.CPA.QuotaCheckIntervalSeconds = 120
	}
}

func (s *Store) ProSettings() (model.ProSettings, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := model.DefaultProSettings()
	var raw, encryptedSub2, encryptedCPA string
	if err := s.db.QueryRow("SELECT profile, encrypted_sub2_password, encrypted_cpa_key FROM pro_settings WHERE id=1").Scan(&raw, &encryptedSub2, &encryptedCPA); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return v, "", "", nil
		}
		return v, "", "", err
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, "", "", err
	}
	normalizeProSettings(&v)
	subPassword, err := s.decryptOptional(encryptedSub2)
	if err != nil {
		return v, "", "", err
	}
	cpaKey, err := s.decryptOptional(encryptedCPA)
	if err != nil {
		return v, "", "", err
	}
	v.Sub2.PasswordPresent, v.CPA.KeyPresent = encryptedSub2 != "", encryptedCPA != ""
	var vmKey string
	if err := s.db.QueryRow("SELECT encrypted_key FROM pro_vm2api_secret WHERE id=1").Scan(&vmKey); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, "", "", err
	}
	v.VM2API.KeyPresent = vmKey != ""
	var merchantKey string
	if err := s.db.QueryRow("SELECT encrypted_key FROM pro_merchant_secret WHERE id=1").Scan(&merchantKey); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, "", "", err
	}
	v.MerchantKeyPresent = merchantKey != ""
	var starBridgeKey string
	if err := s.db.QueryRow("SELECT encrypted_key FROM pro_starbridge_merchant_secret WHERE id=1").Scan(&starBridgeKey); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, "", "", err
	}
	v.MerchantStarBridgeKeyPresent = starBridgeKey != ""
	return v, subPassword, cpaKey, nil
}

func (s *Store) SaveProSettings(v model.ProSettings, subPassword, cpaKey string, vmKeys ...string) (model.ProSettings, error) {
	if err := v.Sub2.ValidateProxyRetention(); err != nil {
		return v, err
	}
	if err := v.ValidateDisposition(); err != nil {
		return v, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	normalizeProSettings(&v)
	var oldSub, oldCPA, previousRaw string
	_ = s.db.QueryRow("SELECT encrypted_sub2_password, encrypted_cpa_key, profile FROM pro_settings WHERE id=1").Scan(&oldSub, &oldCPA, &previousRaw)
	var previous model.ProSettings
	_ = json.Unmarshal([]byte(previousRaw), &previous)
	// The independent runtime control cannot be overwritten by a stale form.
	v.AutoDeletePaused = previous.AutoDeletePaused
	previousSecret := oldSub
	if previous.Sub2.AuthMode != v.Sub2.AuthMode {
		oldSub = ""
		_ = s.db.QueryRow("SELECT encrypted_password FROM pro_sub2_variants WHERE auth_mode=?", v.Sub2.AuthMode).Scan(&oldSub)
	}
	var err error
	if strings.TrimSpace(subPassword) != "" {
		oldSub, err = s.encrypt(strings.TrimSpace(subPassword))
		if err != nil {
			return v, err
		}
	}
	if strings.TrimSpace(cpaKey) != "" {
		oldCPA, err = s.encrypt(strings.TrimSpace(cpaKey))
		if err != nil {
			return v, err
		}
	}
	var vmKey string
	if err = s.db.QueryRow("SELECT encrypted_key FROM pro_vm2api_secret WHERE id=1").Scan(&vmKey); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if len(vmKeys) > 0 && strings.TrimSpace(vmKeys[0]) != "" {
		vmKey, err = s.encrypt(strings.TrimSpace(vmKeys[0]))
		if err != nil {
			return v, err
		}
	}
	v.VM2API.KeyPresent = vmKey != ""
	v.Sub2.PasswordPresent, v.CPA.KeyPresent = oldSub != "", oldCPA != ""
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if previousRaw != "" {
		if _, err = tx.Exec("INSERT INTO pro_sub2_variants(auth_mode,payload,encrypted_password) VALUES(?,?,?) ON CONFLICT(auth_mode) DO UPDATE SET payload=excluded.payload,encrypted_password=excluded.encrypted_password", previous.Sub2.AuthMode, mustJSON(previous.Sub2), previousSecret); err != nil {
			return v, err
		}
	}
	if _, err = tx.Exec("INSERT INTO pro_sub2_variants(auth_mode,payload,encrypted_password) VALUES(?,?,?) ON CONFLICT(auth_mode) DO UPDATE SET payload=excluded.payload,encrypted_password=excluded.encrypted_password", v.Sub2.AuthMode, mustJSON(v.Sub2), oldSub); err != nil {
		return v, err
	}
	if err = syncProPushTimeoutTx(tx, v); err != nil {
		return v, err
	}
	if _, err = tx.Exec("INSERT INTO pro_vm2api_secret(id,encrypted_key) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET encrypted_key=excluded.encrypted_key", vmKey); err != nil {
		return v, err
	}
	_, err = tx.Exec(`INSERT INTO pro_settings(id,profile,encrypted_sub2_password,encrypted_cpa_key) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_sub2_password=excluded.encrypted_sub2_password,encrypted_cpa_key=excluded.encrypted_cpa_key`, string(raw), oldSub, oldCPA)
	if err != nil {
		return v, err
	}
	if len(vmKeys) > 0 && strings.TrimSpace(vmKeys[0]) != "" {
		if err = s.rotateProVMSecretTx(tx, v.VM2API.URL, strings.TrimSpace(vmKeys[0])); err != nil {
			return v, err
		}
	}
	return v, tx.Commit()
}

func (s *Store) SaveProOAuthSession(session model.ProOAuthSession, verifier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, err := s.encrypt(verifier)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO pro_oauth_sessions(id,profile,encrypted_verifier,expires_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_verifier=excluded.encrypted_verifier,expires_at=excluded.expires_at", session.ID, string(raw), sealed, formatTime(session.ExpiresAt))
	return err
}

func (s *Store) ProOAuthSession(id string) (model.ProOAuthSession, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec("DELETE FROM pro_oauth_sessions WHERE expires_at < ?", formatTime(time.Now()))
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile,encrypted_verifier FROM pro_oauth_sessions WHERE id=?", strings.TrimSpace(id)).Scan(&raw, &encrypted); err != nil {
		return model.ProOAuthSession{}, "", errors.New("OAuth 会话不存在或已过期")
	}
	var session model.ProOAuthSession
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return session, "", err
	}
	verifier, err := s.decrypt(encrypted)
	return session, verifier, err
}

func (s *Store) DeleteProOAuthSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM pro_oauth_sessions WHERE id=?", strings.TrimSpace(id))
	return err
}

func (s *Store) UpdateProAccount(email string, mutate func(*model.MailAccountProfile)) (model.MailAccountProfile, error) {
	return s.updateProAccount(email, time.Time{}, mutate)
}

// Check the UI version under the same lock as the business-state transaction.
func (s *Store) UpdateProAccountIfUnchanged(email string, expected time.Time, mutate func(*model.MailAccountProfile)) (model.MailAccountProfile, error) {
	return s.updateProAccount(email, expected, mutate)
}

func (s *Store) updateProAccount(email string, expected time.Time, mutate func(*model.MailAccountProfile)) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		return model.MailAccountProfile{}, errors.New("Pro 账号不存在")
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	if !p.ProActionsAllowed() {
		return p, errors.New("账号不在 Pro 管理中")
	}
	if !expected.IsZero() && !p.UpdatedAt.Equal(expected) {
		return p, ErrProStageConflict
	}
	// Decode a separate copy because nested workflow maps/pointers are mutated.
	var previous model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &previous); err != nil {
		return p, err
	}
	wasConfirmed := p.TransferStatus == "completed" && p.ProMergeConfirmedSource != ""
	wasRemoved := p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed"
	wasDeleted := p.DeletionAccepted()
	wasMergeV2Completed, wasMergeV2Accepted := p.MergeV2Completed(), p.MergeV2Accepted()
	wasMerchantCompleted := p.MerchantCompleted()
	wasMerchantAccepted := p.MerchantAccepted()
	previousProvider, previousSub2ID := p.PushProvider, p.Sub2AccountID
	previousPushStatus := p.PushStatus
	// Preserve a historical end before a manual retry replaces its stage data.
	if p.ProPushStartedAt != nil && p.ProPushEndedAt == nil {
		p.ProPushEndedAt = p.ProPushEndTime()
	}
	wasEnding := p.ProWorkflowRunning || p.SpaceMergedOnce || p.DispositionStarted() ||
		(p.ProAuto.Status == "running" && (p.ProAuto.Stage == "merge" || p.ProAuto.Stage == "deactivate"))
	if mutate != nil {
		mutate(&p)
	}
	if previous.ProDead != nil {
		p.ProDead = previous.ProDead
	}
	if p.IsProDead() {
		p.ChatGPTStatus = "dead"
		p.ProAuto.Status = "dead"
		p.ProAuto.NextCheckAt = nil
		p.ProAuto.RetryAt = nil
		p.ProLastError = p.ProDead.Reason
	}
	if previousPushStatus != "completed" && p.PushStatus == "completed" && p.ProPushStartedAt == nil {
		now := time.Now()
		p.ProPushStartedAt = &now
	}
	if p.PushProvider != previousProvider || p.Sub2AccountID != previousSub2ID || p.VM2 != nil {
		// A committed SIWC transfer explicitly binds the carried total to its
		// newly created destination. Other reassignment still clears old costs.
		if p.ProSIWC == nil || p.ProSIWC.AccountID != p.Sub2AccountID || p.ProSIWC.Stage != "warm" || p.CostDownstreamIdentity != fmt.Sprint(p.Sub2AccountID) {
			p.ClearProCostSnapshot()
		}
	}
	p.UpdatedAt = time.Now()
	if p.MergeV2Started() {
		p.ReconcileProMergeV2(p.UpdatedAt)
	} else if p.MerchantStarted() {
		p.ReconcileProMerchant(p.UpdatedAt)
	} else if p.DeletionSubmitted() {
		p.ReconcileProAutoDeletion(p.UpdatedAt)
	} else {
		p.ReconcileProAutoMerge(p.UpdatedAt)
	}
	p.ReconcileProQualityMerge()
	p.ReconcileProStop()
	if p.IsProDead() {
		p.ProAuto.Status = "dead"
		p.ProAuto.NextCheckAt = nil
		p.ProAuto.RetryAt = nil
	}
	if p.ProPushEndTime() != nil || !wasEnding {
		p.FreezeProPushDuration(p.UpdatedAt)
	}
	for _, mode := range model.ProDispositionModes {
		if !previous.OAIConfirmationPrerequisite(mode) && p.OAIConfirmationPrerequisite(mode) {
			settings := model.DefaultProSettings()
			var settingsRaw string
			if err := s.db.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&settingsRaw); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return p, err
			}
			if settingsRaw != "" {
				if err := json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
					return p, err
				}
			}
			p.PrepareOAIConfirmation(previous, settings.OAIConfirmation, p.UpdatedAt)
			break
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", string(encoded), formatTime(p.UpdatedAt), email); err != nil {
		return p, err
	}
	// Archive the old round in the same transaction that accepts/resets a
	// manual restart. Never include credentials or the whole account profile.
	if restart := p.ProMergeV2Restart; restart != nil {
		requested := previous.ProMergeV2Restart == nil || previous.ProMergeV2Restart.ID != restart.ID
		reset := previous.ProMergeV2Restart != nil && previous.ProMergeV2Restart.Status == "pending" && restart.Status == "starting"
		if requested || reset {
			id := mergeV2RestartEventID(p.ID, restart.ID)
			message := "已接收手动重新执行，等待旧任务退出"
			if reset {
				id += ":archive"
				message = "已归档上一轮合并2.0，从邀请重新执行"
			}
			event := model.AutoRotationEvent{ID: id, AccountID: p.ID, Email: p.Email, Type: "pro_merge_v2_restart", Source: "pro_management", Operation: "merge_v2", Stage: "restart", Message: message, CreatedAt: p.UpdatedAt, Details: map[string]any{"request_id": restart.ID, "previous_merge_v2": previous.ProMergeV2}}
			if _, err = tx.Exec("INSERT INTO auto_rotation_events(id,run_id,task_id,account_id,payload,created_at) VALUES(?,?,?,?,?,?)", id, "", "", p.ID, mustJSON(event), formatTime(p.UpdatedAt)); err != nil {
				return p, err
			}
		}
	}
	// Keep confirmation history available to card return records after the
	// mailbox is removed locally. It never changes financial status or amounts.
	if len(p.ProOAIConfirmations) > 0 && mustJSON(previous.ProOAIConfirmations) != mustJSON(p.ProOAIConfirmations) {
		if _, err = tx.Exec(`INSERT INTO pro_oai_confirmation_accounts(email,profile) VALUES(?,?) ON CONFLICT(email) DO UPDATE SET profile=excluded.profile`, email, mustJSON(p.ProOAIConfirmations)); err != nil {
			return p, err
		}
	}
	if err = recordProDeadTx(tx, p, previous); err != nil {
		return p, err
	}
	if err = s.recordEJHConsumptionTx(tx, p, p.UpdatedAt); err != nil {
		return p, err
	}
	if err = recordProMerchantFeeTx(tx, p); err != nil {
		return p, err
	}
	if err = s.recordNexovccRevenueTx(tx, p); err != nil {
		return p, err
	}
	if (!wasRemoved && (p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed")) || (!wasDeleted && p.DeletionAccepted()) || (!wasMerchantCompleted && p.MerchantCompleted()) || (!wasMergeV2Completed && p.MergeV2Completed()) {
		if err = releaseProCardOrdersTx(tx, email, formatTime(p.UpdatedAt)); err != nil {
			return p, err
		}
	}
	if !wasConfirmed && p.TransferStatus == "completed" && p.ProMergeConfirmedSource != "" {
		at := p.UpdatedAt
		if p.SpaceMergedAt != nil {
			at = *p.SpaceMergedAt
		}
		if err = ejhMarkRequestedTx(tx, p.Email, p.ProAuto.OrderID, p.ProMergeConfirmedSource, at); err != nil {
			return p, err
		}
	}
	if !wasMergeV2Accepted && p.MergeV2Accepted() {
		if err = ejhMarkRequestedTx(tx, p.Email, p.ProAuto.OrderID, "merge_v2_accepted", *p.ProMergeV2.AcceptedAt); err != nil {
			return p, err
		}
	}
	if !previous.MergeV2Resolved() && p.ProMergeV2 != nil && p.ProMergeV2.CompletionBasis == "plan_free" && p.ProMergeV2.SubmittedAt != nil {
		if err = ejhMarkRequestedTx(tx, p.Email, p.ProAuto.OrderID, "merge_v2_plan_free", *p.ProMergeV2.SubmittedAt); err != nil {
			return p, err
		}
	}
	if !wasMerchantAccepted && p.MerchantAccepted() {
		if err = ejhMarkRequestedTx(tx, p.Email, p.ProAuto.OrderID, "api_merchant", *p.ProMerchant.AcceptedAt); err != nil {
			return p, err
		}
	}
	if !wasDeleted && p.DeletionAccepted() {
		if err = ejhMarkRequestedTx(tx, p.Email, p.ProAuto.OrderID, "account_deactivation", *p.ProDeactivation.AcceptedAt); err != nil {
			return p, err
		}
	}
	return p, tx.Commit()
}

func (s *Store) RecoverProWorkflows() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := model.DefaultProSettings()
	var settingsRaw string
	if err := s.db.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&settingsRaw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if settingsRaw != "" {
		if err := json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
			return err
		}
	}
	rows, err := s.db.Query("SELECT email,profile FROM mail_accounts")
	if err != nil {
		return err
	}
	type item struct {
		email   string
		profile model.MailAccountProfile
	}
	var updates []item
	for rows.Next() {
		var email, raw string
		if rows.Scan(&email, &raw) != nil {
			continue
		}
		var p model.MailAccountProfile
		if json.Unmarshal([]byte(raw), &p) != nil || !p.ProActionsAllowed() {
			continue
		}
		legacyCancelled := p.TransferStatus == "failed" && (strings.Contains(p.ProLastError, "context canceled") || strings.Contains(p.ProLastError, "context deadline exceeded"))
		groupsRunning := p.ProPostMergeGroups != nil && p.ProPostMergeGroups.Status == "running"
		// Normalize clocks saved by older versions without reviving completed
		// deletion workflows, even when downstream cleanup remains unfinished.
		deletionReconciled := false
		if p.DeletionConfirmed() {
			deletionReconciled = p.ReconcileProAutoDeletion(time.Now())
		}
		deletionNeedsRecovery := p.ProDeactivation != nil && (deletionReconciled || p.ProDeactivation.Status == "running" || p.DeletionNeedsContinuation() && p.ProDeactivation.NextCheckAt == nil)
		merchantReconciled := p.ReconcileProMerchant(time.Now())
		if p.MergeV2Started() {
			merchantReconciled = p.ReconcileProMergeV2(time.Now()) || merchantReconciled
		}
		if p.MerchantNeedsContinuation() && p.ProMerchant.NextCheckAt == nil && !p.ProAuto.StopRequested {
			next := time.Now().Add(15 * time.Second)
			p.ProMerchant.NextCheckAt = &next
			merchantReconciled = true
		}

		if m := p.ProMergeV2; m != nil && m.CompletedAt == nil && m.Recovery != nil && !m.Recovery.Exhausted && m.Recovery.NextAt == nil {
			r := m.Recovery
			// The coordinator decides exhaustion/cleanup after restart. Do not
			// strand a pre-submit job at its budget limit before cleaning its seat.
			next := time.Now().Add(time.Duration(r.Delay(settings)) * time.Second)
			r.NextAt = &next
			merchantReconciled = true
		}
		stopReconciled := p.ReconcileProStop()
		if p.IsProDead() {
			p.ProAuto.Status = "dead"
			p.ProAuto.NextCheckAt = nil
			p.ProAuto.RetryAt = nil
		}
		if !p.ProWorkflowRunning && !legacyCancelled && !groupsRunning && !deletionNeedsRecovery && !merchantReconciled && !stopReconciled {
			continue
		}
		if legacyCancelled {
			p.TransferStatus = "unknown"
		}
		p.ProWorkflowRunning = false
		if m := p.ProMergeV2; m != nil && m.Status == "running" {
			m.Status = "interrupted"
			if m.SubmittedAt != nil && m.AcceptedAt == nil {
				m.Status = "unknown"
			}
			m.Steps[m.Stage] = m.Status
			m.Error = "服务重启，请从保存进度续跑；已受理请求不会重复提交"
			m.UpdatedAt = time.Now()
		}
		if d := p.ProDeactivation; d != nil && d.Status == "running" {
			d.Status = "interrupted"
			if d.SubmittedAt != nil {
				d.Status = "unknown"
			}
			if d.AcceptedAt != nil {
				d.Status = "queued"
			}
			d.Error = "服务重启，可续跑核验；已提交的删除不会重复发送"
			d.UpdatedAt = time.Now()
		}
		if d := p.ProDeactivation; d != nil && p.DeletionNeedsContinuation() && !p.ProAuto.StopRequested {
			next := time.Now().Add(settings.DeletionCheckInterval())
			d.NextCheckAt = &next
		}
		if groupsRunning {
			p.ProPostMergeGroups.Status = "pending"
			p.ProPostMergeGroups.UpdatedAt = time.Now()
		}
		for _, status := range []*string{&p.InviteStatus, &p.AcceptStatus, &p.TransferStatus, &p.RemoveStatus} {
			if *status == "running" {
				if status == &p.TransferStatus {
					*status = "unknown"
				} else {
					*status = "pending"
				}
			}
		}
		p.ProLastError = "服务重启，流程将在下次检测或手动执行时续跑"
		if p.TransferStatus == "unknown" {
			p.ProLastError = "服务在合并途中重启，远端结果待确认；请核实合并结果后手动修正状态再续跑"
		}
		if p.DeletionConfirmed() {
			p.ProDeactivation.Status, p.ProDeactivation.Stage = "completed", "confirm"
			p.ProLastError = p.ProDeactivation.Error
		}
		p.ReconcileProMerchant(time.Now())
		p.ReconcileProMergeV2(time.Now())
		p.ReconcileProStop()
		if p.IsProDead() {
			p.ProAuto.Status = "dead"
			p.ProAuto.NextCheckAt = nil
			p.ProAuto.RetryAt = nil
		}
		p.UpdatedAt = time.Now()
		updates = append(updates, item{email: email, profile: p})
	}
	_ = rows.Close()
	for _, update := range updates {
		raw, _ := json.Marshal(update.profile)
		if _, err = s.db.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", string(raw), formatTime(update.profile.UpdatedAt), update.email); err != nil {
			return err
		}
	}
	return nil
}
