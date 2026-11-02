package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *Server) updateProMergeV2Stage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Stage              string    `json:"stage"`
		Status             string    `json:"status"`
		ExpectedStatus     *string   `json:"expected_status"`
		ExpectedUpdatedAt  time.Time `json:"expected_updated_at"`
		ConfirmNotAccepted bool      `json:"confirm_not_accepted"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	labels := map[string]string{"invite": "邀请", "login": "团队登录", "submit": "提交停用", "remove": "移出团队", "groups": "后续分组"}
	if labels[input.Stage] == "" || input.ExpectedStatus == nil || input.ExpectedUpdatedAt.IsZero() {
		writeAPI(w, 400, nil, "步骤或版本无效，请刷新")
		return
	}
	switch input.Status {
	case "pending", "not_started", "completed", "failed", "unknown", "interrupted":
	default:
		writeAPI(w, 400, nil, "状态无效")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "流程执行中，请先停止")
		return
	}
	defer lock.Unlock()
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil || p.ManagementScope != "pro" || p.ProMergeV2 == nil {
		writeAPI(w, 404, nil, "该账号尚未启动合并空间2.0")
		return
	}
	m := p.ProMergeV2
	if p.ProWorkflowRunning || s.proAutomationRunning(email) || s.proLoginAlreadyRunning(email) {
		writeAPI(w, 409, nil, "流程执行中，请先停止")
		return
	}
	if m.Steps[input.Stage] != *input.ExpectedStatus {
		writeAPI(w, 409, nil, "步骤状态已变化，请刷新")
		return
	}
	if m.CompletedAt != nil || (p.MergeV2Resolved() && (input.Stage == "submit" || input.Stage == "invite" || input.Stage == "login")) {
		writeAPI(w, 409, nil, "已受理或已完成的事实不可重置；不会重复提交")
		return
	}
	if m.VerificationOnly && input.Stage == "submit" && input.Status != "completed" {
		writeAPI(w, 409, nil, "该原单已进入仅核验流程，不能清除提交记录或重复提交")
		return
	}
	if input.Stage == "submit" && m.SubmittedAt != nil && input.Status != "completed" && !input.ConfirmNotAccepted {
		writeAPI(w, 400, nil, "请先确认上游确实未受理，才能重置提交步骤")
		return
	}
	if input.Status == "completed" {
		previous := map[string]string{"login": "invite", "submit": "login", "remove": "submit", "groups": "remove"}[input.Stage]
		if previous != "" && m.Steps[previous] != "completed" {
			writeAPI(w, 400, nil, "请先确认前置步骤完成")
			return
		}
		if (input.Stage == "login" || input.Stage == "submit") && m.UserID == "" {
			writeAPI(w, 400, nil, "缺少经过验证的团队登录，请先续跑登录")
			return
		}
	}
	saved, err := s.store.UpdateProAccountIfUnchanged(email, input.ExpectedUpdatedAt, func(p *model.MailAccountProfile) {
		m := p.ProMergeV2
		now := time.Now()
		if m.Manual == nil {
			m.Manual = map[string]model.ProStageOverride{}
		}
		m.Manual[input.Stage] = model.ProStageOverride{Status: input.Status, UpdatedAt: now}
		m.Steps[input.Stage] = input.Status
		m.Stage = input.Stage
		m.Status = "interrupted"
		m.Error = ""
		p.ProLastError = ""
		m.UpdatedAt = now
		m.Message = "手动修正" + labels[input.Stage] + "；续跑仅执行剩余步骤"
		if p.MergeV2Resolved() && (input.Stage == "remove" || input.Stage == "groups") && m.Recovery != nil {
			m.Recovery.SetPhase("tail")
			m.Recovery.Attempts, m.Recovery.Exhausted, m.Recovery.NextAt = 0, false, nil
		}
		if input.Stage == "submit" {
			if input.Status == "completed" {
				if m.SubmittedAt == nil {
					m.SubmittedAt = &now
				}
				m.AcceptedAt = &now
				if m.Recovery != nil {
					m.Recovery.Phase = "tail"
					m.Recovery.Attempts = 0
					m.Recovery.NextAt = nil
					m.Recovery.Exhausted = false
				}
			} else {
				m.SubmittedAt = nil
				// An explicit reviewed rejection starts a new manual budget.
				// Earlier evidence remains in the append-only account audit.
				m.Recovery = nil
			}
		}
		if input.Stage == "groups" && input.Status == "completed" {
			m.CompletedAt = &now
			m.Status = "completed"
			if p.DailyMergeMatches() {
				p.ProDailyMerge.Completed = true
			}
		}
	})
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	s.auditProEvent(saved, "merge_v2_manual", "completed", "", "手动修正"+labels[input.Stage]+"状态", map[string]any{"status": input.Status, "remote_request_sent": false})
	writeAPI(w, 200, saved, "")
}
