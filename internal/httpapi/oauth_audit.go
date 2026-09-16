package httpapi

import (
	"context"
	"time"

	"chatgpt-space-merge/internal/model"
)

const (
	oauthQualityAttempts = 4
	oauthRoundAttempts   = 3
)

func (s *Server) newAccountOAuthJob(ctx context.Context, profile model.FreeAccountProfile, trigger string) map[string]any {
	trace, _ := ctx.Value(autoRotationTraceContextKey{}).(autoRotationTraceContext)
	source := "manual_single"
	if trigger == "relogin" {
		source = "relogin"
	}
	if trace.TaskID != "" {
		source = "auto_rotation"
	}
	job := map[string]any{
		"job_id": randomRegistrationID(), "account_id": profile.ID, "email": profile.Email,
		"cycle_id": profile.CycleID, "run_id": trace.RunID, "task_id": trace.TaskID,
		"outer_attempt": trace.Attempt, "outer_max_attempts": trace.MaxAttempts,
		"trigger": trigger, "source": source, "status": "queued", "state": "queued",
		"created_at": time.Now(), "logs": []any{}, "error": "", "result": nil,
	}
	s.oauthMu.Lock()
	s.oauthJobs[job["job_id"].(string)] = job
	s.oauthMu.Unlock()
	details := map[string]any{
		"job_id": job["job_id"], "trigger": trigger, "outer_attempt": trace.Attempt,
		"outer_max_attempts": trace.MaxAttempts, "round_max_attempts": oauthRoundAttempts,
		"quality_max_attempts": oauthQualityAttempts, "configured_login_mode": s.store.AutoRotationSettings().OAuthLoginMode,
	}
	s.auditAccountEvent(s.oauthJobContext(job["job_id"].(string)), profile.ID, "oauth", "job_created", source, "", "OAuth 任务已创建", details)
	return job
}

func (s *Server) oauthJobContext(jobID string) context.Context {
	s.oauthMu.RLock()
	job := s.oauthJobs[jobID]
	cycle, _ := job["cycle_id"].(string)
	run, _ := job["run_id"].(string)
	task, _ := job["task_id"].(string)
	attempt, _ := job["outer_attempt"].(int)
	maxAttempts, _ := job["outer_max_attempts"].(int)
	s.oauthMu.RUnlock()
	return context.WithValue(context.Background(), autoRotationTraceContextKey{}, autoRotationTraceContext{
		CycleID: cycle, RunID: run, TaskID: task, Attempt: attempt, MaxAttempts: maxAttempts,
	})
}

func oauthResultDetails(result map[string]any, runErr error) map[string]any {
	details := map[string]any{"oauth_success": runErr == nil && result["success"] == true}
	for _, key := range []string{"status", "error_code", "stage", "http_status", "retryable", "dead", "hint"} {
		if value, ok := result[key]; ok {
			details[key] = value
		}
	}
	if message := oauthErrorText(result, runErr); message != "" {
		details["error"] = message
	}
	return details
}

func oauthRoundDecision(round int, result map[string]any, runErr error) protocolOAuthDiagnostic {
	retryable := isRetryableOAuthNetworkResult(result, runErr)
	event, message := "round_stopped", "OAuth 本次任务停止重试"
	reason := "non_retryable_result"
	if retryable {
		event, message, reason = "round_retry", "OAuth 代理/网络瞬时错误，释放出口并重新开始完整 OAuth", "retryable_result"
		if round >= oauthRoundAttempts {
			event, message, reason = "round_exhausted", "OAuth 完整流程重试次数已用尽", "round_limit_reached"
		}
	}
	details := oauthResultDetails(result, runErr)
	details["classified_retryable"] = retryable
	details["will_retry"], details["stop_reason"] = retryable && round < oauthRoundAttempts, reason
	details["retry_scope"] = "oauth_job"
	return protocolOAuthDiagnostic{SchemaVersion: 1, Stage: "oauth", Event: event, Message: message,
		Attempt: round, Level: "warning", Details: details}
}
