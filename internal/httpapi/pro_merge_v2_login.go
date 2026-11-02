package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"chatgpt-space-merge/internal/model"
)

var mergeV2DiagnosticURL = regexp.MustCompile(`https?://[^\s<>"']+`)

type mergeV2RetryError struct{ cause error }

func (e *mergeV2RetryError) Error() string { return e.cause.Error() }
func (e *mergeV2RetryError) Unwrap() error { return e.cause }

// One login per durable job. The scheduler owns all delays and budgets, so
// neither the mother mutex nor the worker slot is held while waiting.
func (s *Server) loginProMergeV2(ctx context.Context, p model.MailAccountProfile, provider string, _ func(context.Context, time.Duration) error) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	latest, _, err := s.store.MailAccountCredential(p.Email)
	if err != nil {
		return nil, err
	}
	if latest.ProAuto.StopRequested {
		return nil, context.Canceled
	}
	if latest.IsProDead() {
		return nil, errProAccountDead
	}
	if latest.ProMergeV2 == nil || latest.ProMergeV2.TeamID != p.ProMergeV2.TeamID {
		return nil, errors.New("合并空间2.0目标空间已变化")
	}
	if latest.MergeV2Resolved() {
		return map[string]any{"success": true}, nil
	}
	if latest.ProMergeV2.SubmittedAt != nil || latest.ProMergeV2.VerificationOnly {
		return nil, errMergeV2Unknown
	}
	executionID := randomRegistrationID()
	attempt := 1
	if latest.ProMergeV2.Recovery != nil {
		attempt = latest.ProMergeV2.Recovery.Attempts
	}
	diagnostic := func(d protocolOAuthDiagnostic) { s.auditMergeV2Login(p, provider, executionID, attempt, d) }
	diagnostic(protocolOAuthDiagnostic{Stage: "merge_v2_login", Event: "team_login_start", Message: "开始团队会话登录"})
	var result map[string]any
	if s.proMergeV2Login != nil {
		result, err = s.proMergeV2Login(ctx, p.Email)
	} else {
		result, err = s.executeOpenAILoginContext(ctx, p.Email, "chatgpt_at", func(message string) { s.auditProEvent(p, "merge_v2_login", "running", provider, message, nil) }, diagnostic)
	}
	diagnostic(protocolOAuthDiagnostic{Stage: "merge_v2_login", Event: "team_login_result", Message: "团队登录尝试结束", Details: oauthResultDetails(result, err)})
	latest, _, readErr := s.store.MailAccountCredential(p.Email)
	if readErr != nil {
		return nil, readErr
	}
	if latest.MergeV2Resolved() {
		return map[string]any{"success": true}, nil
	}
	if latest.ProMergeV2 != nil && (latest.ProMergeV2.SubmittedAt != nil || latest.ProMergeV2.VerificationOnly) {
		return result, errMergeV2Unknown
	}
	if latest.ProAuto.StopRequested || ctx.Err() != nil {
		return result, context.Canceled
	}
	if latest.ProMergeV2 == nil || latest.ProMergeV2.TeamID != p.ProMergeV2.TeamID {
		return nil, errors.New("合并空间2.0目标空间已变化")
	}
	if result["success"] == true || result["dead"] == true {
		return result, err
	}
	retry := result["retryable"] == true || err != nil
	switch result["error_code"] {
	case "password_login_restart_required", "team_session_mismatch", "team_invitation_accepted", "email_code_source_missing", "email_code_unavailable":
		retry = true
	}
	if retry {
		return result, &mergeV2RetryError{cause: fmt.Errorf("合并空间2.0：%s", oauthErrorText(result, err))}
	}
	return result, err
}

// Reuse the nonblocking audit queue. HTTP evidence is already structured by
// the protocol runner; discard credential containers and raw bodies entirely.
func (s *Server) auditMergeV2Login(p model.MailAccountProfile, provider, executionID string, attempt int, d protocolOAuthDiagnostic) {
	details := mergeV2DiagnosticFields(d.Details, "plan", "request_id", "error", "error_type", "error_code", "stage", "http_status", "retryable", "dead", "hint", "oauth_success", "round", "round_max_attempts", "quality_attempt", "quality_max_attempts", "stop_reason", "will_retry", "delay_seconds", "next_attempt", "max_attempts", "login_method", "login_auth_status", "at_team_present", "at_team_matches", "session_team_present", "session_team_matches", "email_matches", "workspace_selected", "workspace_page", "target_member", "invitation_accepted")
	details["execution_id"], details["team_login_attempt"], details["protocol_event"] = executionID, attempt, d.Event
	if code, ok := d.Details["curl_code"]; ok {
		switch code.(type) {
		case int, int64, float64:
			details["curl_code"] = code
		}
	}
	if first, ok := d.Details["first_failed_request"].(map[string]any); ok {
		details["first_failed_request"] = mergeV2DiagnosticFields(first, "request_id", "stage", "http_status", "url", "error", "error_code", "error_message", "body_kind")
	}
	response := mergeV2DiagnosticFields(d.Response, "http_status", "url", "location", "continue_url", "page_type", "error_code", "error_type", "error_message", "body_kind", "body_bytes", "ok", "auth_status", "exit_ip", "ip_check_status")
	if headers, ok := d.Response["headers"].(map[string]any); ok {
		response["headers"] = mergeV2DiagnosticFields(headers, "content-type", "cf-ray", "cf-mitigated", "x-request-id", "x-openai-request-id", "retry-after")
	}
	request := mergeV2DiagnosticFields(d.Request, "request_id", "method", "url", "referer", "timeout_seconds", "impersonate", "allow_redirects")
	if proxy, ok := d.Request["proxy"].(map[string]any); ok {
		request["proxy"] = mergeV2DiagnosticFields(proxy, "configured", "name", "endpoint")
	}
	s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: p.ID, Email: p.Email, AdminAccountID: p.ProMergeV2.AdminID,
		Type: "pro_merge_v2_protocol", Source: "pro_management", Provider: provider, Operation: "merge_v2_login", Stage: d.Stage,
		Message: mergeV2DiagnosticText(d.Message), Level: d.Level, HTTPStatus: d.HTTPStatus, DurationMS: d.DurationMS, Attempt: attempt,
		Request: request, Response: response, Details: details})
}

func mergeV2DiagnosticText(value string) string {
	value = mergeV2DiagnosticURL.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[url]"
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	})
	value = redactSensitiveText(value)
	if len(value) > 1200 {
		value = value[:1200]
	}
	return value
}

func mergeV2DiagnosticFields(input map[string]any, keys ...string) map[string]any {
	out := make(map[string]any)
	for _, key := range keys {
		switch value := input[key].(type) {
		case string:
			out[key] = mergeV2DiagnosticText(value)
		case bool, int, int64, float64:
			out[key] = value
		}
	}
	return out
}
