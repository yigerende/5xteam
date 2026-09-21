package model

import "time"

// Manual progress is display-only. It must never satisfy the resumable
// automation's post-purchase OAuth milestone or create a scheduled task.
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
		return "completed"
	}
	return "pending"
}
