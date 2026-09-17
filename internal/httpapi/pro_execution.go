package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

func proStageStatus(p model.MailAccountProfile, stage string) string {
	switch stage {
	case "invite":
		return p.InviteStatus
	case "accept":
		return p.AcceptStatus
	case "transfer":
		return p.TransferStatus
	case "remove":
		return p.RemoveStatus
	}
	return ""
}

func setProStage(p *model.MailAccountProfile, stage, status string) {
	switch stage {
	case "invite":
		p.InviteStatus = status
	case "accept":
		p.AcceptStatus = status
	case "transfer":
		p.TransferStatus = status
	case "remove":
		p.RemoveStatus = status
	}
	if stage == "transfer" && status == "completed" {
		p.SpaceMergedOnce = true
		if p.SpaceMergedAt == nil {
			now := time.Now()
			p.SpaceMergedAt = &now
		}
	}
}

func proStageLabel(stage string) string {
	return map[string]string{"invite": "邀请空间", "accept": "进入空间", "transfer": "合并空间", "remove": "母号移出"}[stage]
}

func (s *Server) updateProAccountStage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Stage          string  `json:"stage"`
		Status         string  `json:"status"`
		ExpectedStatus *string `json:"expected_status"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if proStageLabel(input.Stage) == "" || (input.Status != "not_started" && input.Status != "pending" && input.Status != "completed" && input.Status != "failed") {
		writeAPI(w, 400, nil, "无效的 Pro 阶段或状态")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在执行操作，请完成后再修正状态")
		return
	}
	defer lock.Unlock()
	before, _, err := s.store.MailAccountCredential(email)
	if err != nil || before.ManagementScope != "pro" {
		writeAPI(w, 404, nil, "Pro 账号不存在")
		return
	}
	if input.ExpectedStatus != nil && *input.ExpectedStatus != proStageStatus(before, input.Stage) {
		writeAPI(w, 409, nil, "步骤状态已变化，请刷新后重新确认")
		return
	}
	profile, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		setProStage(p, input.Stage, input.Status)
		p.ProWorkflowRunning = false
		p.ProLastError = ""
		if input.Status == "failed" {
			p.ProLastError = "手动标记" + proStageLabel(input.Stage) + "失败"
		}
	})
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: profile.ID, Email: profile.Email, AdminAccountID: profile.TargetAdminID,
		Type: "manual_stage", Source: "pro_management", Operation: "stage", Stage: input.Stage,
		FromStatus: proStageStatus(before, input.Stage), ToStatus: input.Status, Message: "手动修正 Pro " + proStageLabel(input.Stage) + "状态",
		Details: map[string]any{"before": proMergeState(before), "after": proMergeState(profile), "remote_request_sent": false}})
	writeAPI(w, 200, profile, "")
}

func proMergeState(p model.MailAccountProfile) map[string]any {
	return map[string]any{"invite": p.InviteStatus, "accept": p.AcceptStatus, "transfer": p.TransferStatus, "remove": p.RemoveStatus,
		"target_admin_id": p.TargetAdminID, "target_team_id": p.TargetTeamID, "seat_type": p.TargetSeatType, "space_merged_once": p.SpaceMergedOnce}
}

func (s *Server) proLogProfile(w http.ResponseWriter, r *http.Request) (model.MailAccountProfile, bool) {
	p, _, err := s.store.MailAccountCredential(r.PathValue("email"))
	if err != nil || p.ManagementScope != "pro" {
		writeAPI(w, 404, nil, "Pro 账号不存在")
		return p, false
	}
	return p, true
}

func (s *Server) listProAccountEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.proLogProfile(w, r)
	if !ok {
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeAPI(w, 400, nil, "日志条数必须在 1 到 100 之间")
			return
		}
		limit = parsed
	}
	page, err := s.store.AccountEventsPage(p.ID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		status := 500
		if errors.Is(err, store.ErrInvalidEventCursor) {
			status = 400
		}
		writeAPI(w, status, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"account": p, "events": redactExecutionEvents(page.Events), "has_more": page.HasMore, "next_cursor": page.NextCursor}, "")
}

func (s *Server) exportProAccountEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.proLogProfile(w, r)
	if !ok {
		return
	}
	payload, err := s.executionLogSnapshot(r.Context(), p.ID, "", "")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	payload.ProAccount = &p
	writeExecutionLogExport(w, "pro-account-"+p.Email+"-logs-"+beijingNow().Format("20060102-150405"), payload)
}

// The callback shares the existing nonblocking queue, redaction and retention.
func (s *Server) proRequestContext(ctx context.Context, p model.MailAccountProfile, executionID, stage string, attempt int, proxy map[string]any) context.Context {
	return workflow.WithRequestObserver(ctx, func(o workflow.RequestObservation) {
		status, level, message := "running", "info", "开始请求"
		response := map[string]any{"http_status": o.StatusCode}
		if o.Phase == "end" {
			status, message = "completed", "请求成功"
			if o.Err != nil {
				status, level, message = "failed", "error", "请求失败"
				response["error"] = o.Err.Error()
			}
			var body any
			if len(o.Body) > 0 {
				if json.Unmarshal(o.Body, &body) != nil {
					body = string(o.Body)
				}
				response["body"] = body
			}
		}
		s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: p.ID, Email: p.Email, AdminAccountID: p.TargetAdminID,
			Type: "pro_request", Source: "pro_management", Operation: "space_merge", Stage: stage, ToStatus: status, Level: level,
			Attempt: attempt, HTTPStatus: o.StatusCode, DurationMS: o.Duration.Milliseconds(), Message: proStageLabel(stage) + "：" + message,
			Request: map[string]any{"method": o.Method, "url": o.URL, "headers": o.Headers, "body": o.Payload}, Response: response,
			Details: map[string]any{"execution_id": executionID, "phase": o.Phase, "proxy": proxy, "transport": o.Transport, "target_team_id": p.TargetTeamID}})
	})
}

func (s *Server) runProMergeStep(ctx context.Context, p model.MailAccountProfile, settings model.ProSettings, executionID, stage string, proxy map[string]any, operation func(context.Context) (workflow.Response, error)) error {
	if proStageStatus(p, stage) == "completed" || (stage == "remove" && p.RemoveStatus == "team_removed") {
		s.auditProEvent(p, stage, "completed", settings.Provider, proStageLabel(stage)+"已完成，续跑跳过", map[string]any{"execution_id": executionID, "skipped": true})
		return nil
	}
	if _, err := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { setProStage(p, stage, "running") }); err != nil {
		return err
	}
	attempt := 0
	err := retryProStep(ctx, settings.RetryCount, settings.RetryIntervalSeconds, func() error {
		attempt++
		s.auditProEvent(p, stage, "running", settings.Provider, fmt.Sprintf("%s：第 %d/%d 次执行", proStageLabel(stage), attempt, settings.RetryCount+1), map[string]any{"execution_id": executionID, "attempt": attempt, "max_attempts": settings.RetryCount + 1, "proxy": proxy})
		_, err := operation(s.proRequestContext(ctx, p, executionID, stage, attempt, proxy))
		if err != nil {
			s.auditProEvent(p, stage, "failed", settings.Provider, proStageLabel(stage)+"本次失败", map[string]any{"execution_id": executionID, "attempt": attempt, "error": err.Error(), "will_retry": attempt <= settings.RetryCount && ctx.Err() == nil, "retry_interval_seconds": settings.RetryIntervalSeconds})
		}
		return err
	})
	status := "completed"
	if err != nil {
		status = "failed"
	}
	_, saveErr := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		setProStage(p, stage, status)
		if err != nil {
			p.ProLastError = err.Error()
		}
	})
	if saveErr != nil {
		return errors.Join(err, saveErr)
	}
	return err
}
