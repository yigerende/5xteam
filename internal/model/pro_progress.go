package model

import "time"

// Manual progress projects individual operations into the UI. An existing
// paid automation can resume only after its persisted outcomes are reconciled.
type ProStageProgress struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProStageDisplay struct {
	Steps  map[string]string `json:"steps"`
	Errors map[string]string `json:"errors,omitempty"`
	Manual map[string]bool   `json:"manual,omitempty"`
}

// Operator annotations are separate from execution checkpoints and payment facts.
type ProStageOverride struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (p MailAccountProfile) ApplyProStageOverrides(v *ProStageDisplay) {
	for stage, edit := range p.ProStageOverrides {
		if edit.UpdatedAt.Before(p.ProAuto.StartedAt) {
			continue
		}
		// A later deletion check or manual confirmation supersedes the display
		// annotation; it must never mask a fresh execution result.
		if stage == "deactivate" && p.ProDeactivation != nil && p.ProDeactivation.UpdatedAt.After(edit.UpdatedAt) {
			continue
		}
		if v.Manual == nil {
			v.Manual = map[string]bool{}
		}
		v.Steps[stage], v.Manual[stage] = edit.Status, true
		delete(v.Errors, stage)
	}
}

// Only an explicit operator action may resume stopped work. Use persisted
// OAuth/push results, never editable display overrides, to skip provisioning.
func (p MailAccountProfile) CanResumeProQuota() bool {
	a := p.ProAuto
	if p.ManagementScope != "pro" || p.DispositionStarted() || a.ID == "" || a.OrderID == "" || p.ProWorkflowRunning || p.SpaceMergedOnce ||
		(p.ProMigration != nil && p.ProMigration.Paused) ||
		p.TransferStatus != "" && p.TransferStatus != "not_started" || p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed" ||
		p.ChatGPTStatus == "dead" || p.OAuthStatus != "completed" || !p.AccessTokenPresent || !p.RefreshTokenPresent ||
		p.OAuthAuthorizedAt == nil || p.OAuthAuthorizedAt.Before(a.StartedAt) || p.PushStatus != "completed" {
		return false
	}
	switch a.Status {
	case "stopped", "failed", "interrupted", "awaiting_push":
	default:
		return false
	}
	for _, stage := range p.ProManualStages {
		if stage.Status == "running" {
			return false
		}
	}
	if !(p.PushProvider == "sub2" && p.Sub2AccountID > 0 || p.PushProvider == "vm2api" && p.VM2 != nil && p.VM2.ReadyAt != nil && p.VM2.DeletedAt == nil) {
		return false
	}
	pushAt := a.StageTimes["push:completed"]
	if a.Steps["push"] != "completed" {
		pushAt = time.Time{}
	}
	if manual := p.ProManualStages["push"]; manual.Status == "completed" && !manual.StartedAt.Before(a.StartedAt) && manual.UpdatedAt.After(pushAt) {
		pushAt = manual.UpdatedAt
	}
	return !pushAt.IsZero() && !pushAt.Before(*p.OAuthAuthorizedAt) && !pushAt.Before(a.StartedAt)
}

// ReconcileProAutoManual repairs an existing paid workflow after successful
// manual OAuth/push. Display state alone is insufficient: saved credentials,
// authorization time and the downstream result must agree. Never create an
// automation, replay payment, resume stopped/import-paused work or repeat merge.
func (p *MailAccountProfile) ReconcileProAutoManual(now time.Time) bool {
	a := &p.ProAuto
	if p.ManagementScope != "pro" || p.DispositionStarted() || a.ID == "" || a.StopRequested || p.ProWorkflowRunning || p.SpaceMergedOnce || p.TransferStatus == "completed" ||
		(p.ProMigration != nil && p.ProMigration.Paused) || (a.Status != "failed" && a.Status != "interrupted" && a.Status != "awaiting_push") ||
		(a.Stage != "oauth" && a.Stage != "push") || a.Steps["recharge"] != "completed" ||
		p.ChatGPTStatus == "dead" || p.OAuthStatus != "completed" || !p.AccessTokenPresent || !p.RefreshTokenPresent ||
		p.OAuthAuthorizedAt == nil || p.OAuthAuthorizedAt.Before(a.StartedAt) {
		return false
	}
	oauth := p.ProManualStages["oauth"]
	newOAuth := oauth.Status == "completed" && !oauth.StartedAt.Before(a.StartedAt) &&
		oauth.UpdatedAt.After(a.UpdatedAt) && !p.OAuthAuthorizedAt.Before(oauth.StartedAt)
	if a.Steps["oauth"] != "completed" && !newOAuth {
		return false
	}
	push := p.ProManualStages["push"]
	newPush := push.Status == "completed" && !push.StartedAt.Before(a.StartedAt) && push.UpdatedAt.After(a.UpdatedAt) &&
		!push.UpdatedAt.Before(*p.OAuthAuthorizedAt) && p.PushStatus == "completed" && ((p.PushProvider == "sub2" && p.Sub2AccountID > 0) || (p.PushProvider == "vm2api" && p.VM2 != nil && p.VM2.ReadyAt != nil && p.VM2.DeletedAt == nil))
	if !newOAuth && !newPush {
		return false
	}
	a.Steps["oauth"] = "completed"
	a.Error, a.NextCheckAt = "", nil
	a.Status, a.Stage, a.Steps["push"] = "awaiting_push", "push", "pending"
	if newPush {
		a.Status, a.Stage = "waiting_quota", "quota"
		a.Steps["push"], a.Steps["quota"] = "completed", "waiting"
		next := now.UTC()
		a.NextCheckAt = &next
	}
	a.UpdatedAt = now
	return true
}

// Older records stored an ordinary threshold wait in the error field.
// Hide only that known informational message, never an actual query failure.
func (s ProAutoState) DisplayError() string {
	if s.Status == "waiting_quota" && s.Stage == "quota" && s.Error == "额度尚未达到阈值，等待下次检测" {
		return ""
	}
	return s.Error
}

// An actual (or explicitly confirmed) merge ends quota waiting even when it
// was performed manually. Display-only manual progress cannot satisfy this.
func (p *MailAccountProfile) ReconcileProAutoMerge(now time.Time) bool {
	if p.ProAuto.StopRequested {
		changed := p.ProAuto.Status != "stopped" || p.ProAuto.NextCheckAt != nil || p.ProAuto.Error != "用户已停止全自动流程"
		p.ProAuto.Status, p.ProAuto.Error = "stopped", "用户已停止全自动流程"
		p.ProAuto.NextCheckAt = nil
		if changed {
			p.ProAuto.UpdatedAt = now
		}
		return changed
	}
	if p.MergeV2Started() {
		return p.ReconcileProMergeV2(now)
	}
	if p.MerchantStarted() {
		return p.ReconcileProMerchant(now)
	}
	if p.DeletionSubmitted() {
		return p.ReconcileProAutoDeletion(now)
	}
	if p.ProAuto.ID == "" || p.TransferStatus != "completed" {
		return false
	}
	if p.ProAuto.Steps == nil {
		p.ProAuto.Steps = map[string]string{}
	}
	quota := p.ProAuto.Steps["quota"]
	if quota != "completed" && quota != "skipped" {
		quota = "skipped"
	}
	merge := p.ProMergeProgress()
	status, message := merge, ""
	switch merge {
	case "failed", "unknown":
		status, message = "failed", p.ProLastError
	case "pending":
		status, message = "interrupted", "空间已合并，请继续完成剩余四步流程"
		if p.ProVM2CleanupPending() {
			message = "空间已移出，请继续清理 vm2api 槽位及运行环境"
		}
		if p.ProPostMergeGroupsPending() && p.RemoveStatus == "completed" {
			message = "空间已移出，请继续调整 Sub2 分组"
		}
	}
	changed := p.ProAuto.Status != status || p.ProAuto.Stage != "merge" || p.ProAuto.Error != message ||
		p.ProAuto.NextCheckAt != nil || p.ProAuto.Steps["quota"] != quota || p.ProAuto.Steps["merge"] != merge
	if changed {
		p.ProAuto.Status, p.ProAuto.Stage, p.ProAuto.Error = status, "merge", message
		p.ProAuto.Steps["quota"], p.ProAuto.Steps["merge"] = quota, merge
		p.ProAuto.NextCheckAt = nil
		p.ProAuto.UpdatedAt = now
	}
	return changed
}

// ProStages projects the latest execution of each step into the common UI.
func (p MailAccountProfile) ProStages() ProStageDisplay {
	v := ProStageDisplay{Steps: map[string]string{}, Errors: map[string]string{}}
	for step, status := range p.ProAuto.Steps {
		v.Steps[step] = status
	}
	if f := p.ProAuto.SIWCConsentFailure; f != nil && f.Exhausted && f.OrderID == p.ProAuto.OrderID && v.Steps["oauth"] == "failed" {
		v.Errors["oauth"] = "SIWC 确认授权 401 / not_authorized，重试耗尽；已按配置跳过推送与额度，执行最后一步"
	}
	if message := p.ProAuto.DisplayError(); message != "" {
		v.Errors[p.ProAuto.Stage] = message
	}
	if p.ProAuto.ID == "" {
		// Existing persisted outcomes also remain visible after upgrading.
		v.Steps["push"], v.Steps["quota"] = p.PushStatus, p.QuotaStatus
		if p.OAuthAuthorizedAt != nil && p.RefreshTokenPresent && p.AccessTokenPresent {
			v.Steps["oauth"] = "completed"
		}
		if p.ChatGPTSessionPresent && p.AccessTokenPresent {
			v.Steps["login"] = "completed"
		}
		v.Steps["merge"] = p.ProMergeProgress()
	}
	for step, progress := range p.ProManualStages {
		if p.ProAuto.ID != "" && progress.StartedAt.Before(p.ProAuto.StartedAt) {
			continue
		}
		// A successful quota request does not mean the automatic threshold
		// was reached. Keep that checkpoint, including after a manual refresh.
		if step == "quota" && p.ProAuto.ID != "" && progress.Status == "completed" && p.ProAuto.Steps[step] != "" {
			delete(v.Errors, step)
			continue
		}
		v.Steps[step] = progress.Status
		delete(v.Errors, step)
		if progress.Error != "" {
			v.Errors[step] = progress.Error
		}
	}
	// The saved grouping result is authoritative even if an earlier manual
	// attempt failed and the workflow was subsequently resumed automatically.
	if (p.ProPostMergeGroups != nil || p.VM2 != nil) && p.TransferStatus == "completed" && (p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed") {
		v.Steps["merge"] = p.ProMergeProgress()
		delete(v.Errors, "merge")
		if p.ProPostMergeGroups != nil && p.ProPostMergeGroups.Error != "" {
			v.Errors["merge"] = p.ProPostMergeGroups.Error
		}
		if p.ProVM2CleanupPending() && p.VM2.Error != "" {
			v.Errors["merge"] = p.VM2.Error
		}
	}
	if d := p.ProDeactivation; d != nil {
		status := d.Status
		if d.SubmittedAt != nil && d.ConfirmedAt == nil {
			status = "waiting"
		}
		delete(v.Errors, "deactivate")
		if d.ConfirmedAt != nil {
			status = "completed"
		} else if d.Error != "" {
			status = "failed"
		}
		v.Steps["deactivate"] = status
		delete(v.Manual, "deactivate")
		if d.ConfirmationSource == "manual" && d.ConfirmedAt != nil {
			if v.Manual == nil {
				v.Manual = map[string]bool{}
			}
			v.Manual["deactivate"] = true
		}
		if d.Error != "" {
			v.Errors["deactivate"] = d.Error
		}
	}
	p.ApplyProStageOverrides(&v)
	if m := p.ProMerchant; m != nil {
		status := "waiting"
		if m.Error != "" || m.Status == "failed" {
			status = "failed"
		}
		if m.CompletedAt != nil {
			status = "completed"
		}
		v.Steps["merchant"] = status
		delete(v.Manual, "merchant")
		delete(v.Errors, "merchant")
		if m.Error != "" {
			v.Errors["merchant"] = m.Error
		}
	}
	if m := p.ProMergeV2; m != nil {
		v.Steps["merge_v2"] = m.Status
		delete(v.Errors, "merge_v2")
		if m.Error != "" {
			v.Errors["merge_v2"] = m.Error
		}
	}
	return v
}

func (p MailAccountProfile) ProMergeProgress() string {
	if p.ProWorkflowRunning {
		return "running"
	}
	for _, state := range []string{p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus} {
		if state == "running" {
			return "running"
		}
		if state == "unknown" {
			return "unknown"
		}
		if state == "failed" {
			return "failed"
		}
	}
	if p.InviteStatus == "completed" && p.AcceptStatus == "completed" && p.TransferStatus == "completed" && (p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed") {
		if p.ProVM2CleanupPending() {
			if p.VM2.Stage == "delete_failed" {
				return "failed"
			}
			return "pending"
		}
		if p.ProPostMergeGroupsPending() {
			switch p.ProPostMergeGroups.Status {
			case "failed":
				return "failed"
			case "running":
				return "running"
			default:
				return "pending"
			}
		}
		return "completed"
	}
	return "pending"
}
