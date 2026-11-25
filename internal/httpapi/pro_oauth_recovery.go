package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/model"
)

// An unusable authorization session never grants payment permission and is
// separate from the opt-in consent/401 final policy.
type siwcSessionExpiredError struct {
	Source     string
	Downstream model.ProDownstreamRef
}

func (*siwcSessionExpiredError) Error() string {
	return "SIWC 身份验证会话无法继续，需要恢复已付款授权"
}

var errProOAuthReloginReady = errors.New("正在恢复已付款授权")
var errProOAuthRetriesExhausted = errors.New("第三步授权重试已耗尽")

func proOAuthSource(v model.ProSettings) string {
	if v.Provider == "sub2" && v.Sub2.AuthMode == "siwc" {
		return siwcAuthorizationSource(v.Sub2)
	}
	return "codex"
}

// Called while Python still owns the live login client. A retry keeps it unless
// the protocol explicitly reports an unusable authentication session.
func (s *Server) proOAuthRetryRPC(ctx context.Context, email string, args map[string]any) (any, error) {
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return nil, err
	}
	if p.ProAuto.StopRequested {
		return nil, context.Canceled
	}
	if p.ProAuto.Steps["oauth"] == "completed" {
		return map[string]any{"completed": true}, nil
	}
	v, _, _, err := s.proNewPushSettings(p)
	if err != nil {
		return nil, err
	}
	source := firstNonEmpty(p.ProAuto.OAuthSource, proOAuthSource(v))
	if target, ok := args["expected_downstream"].(model.ProDownstreamRef); ok && !target.Matches(model.ProDownstream(v)) {
		return nil, errors.New("推送目标已变化，停止旧授权会话重试")
	}
	s.auditProEvent(p, "oauth", "failed", "pro_auto", "第三步授权失败，按后台配置检查重试次数", map[string]any{"error": redactSensitiveText(fmt.Sprint(args["error"])), "error_code": args["error_code"]})
	_, err = s.prepareOAuthRecovery(ctx, email, &siwcSessionExpiredError{Source: source, Downstream: model.ProDownstream(v)})
	if err != nil {
		latest, _, readErr := s.store.MailAccountCredential(email)
		if ctx.Err() == nil && readErr == nil && latest.ProAuto.OAuthRecovery != nil && latest.ProAuto.OAuthRecovery.Exhausted {
			return map[string]any{"retry": false, "message": err.Error()}, nil
		}
		return nil, err
	}
	return map[string]any{"retry": true, "relogin": args["error_code"] == "siwc_session_expired" || args["error_code"] == "oauth_session_expired"}, nil
}

func (s *Server) prepareOAuthRecovery(ctx context.Context, email string, cause error) (bool, error) {
	var expired *siwcSessionExpiredError
	if !errors.As(cause, &expired) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return true, err
	}
	v, _, _, err := s.proNewPushSettings(p)
	if err != nil {
		return true, err
	}
	if expired.Source != proOAuthSource(v) || !expired.Downstream.Matches(model.ProDownstream(v)) ||
		p.ProAuto.ID == "" || p.ProAuto.OrderID == "" || p.ProAuto.StopRequested || p.IsProDead() || p.DispositionStarted() || p.ProWorkflowRunning ||
		p.ProAuto.Steps["oauth"] == "completed" || p.PushStatus == "completed" || p.Sub2AccountID != 0 {
		return true, fmt.Errorf("%w；账号或推送配置已变化，停止自动恢复", cause)
	}
	next := time.Now().Add(time.Duration(v.RetryIntervalSeconds) * time.Second)
	updated, err := s.store.PrepareProOAuthRecovery(email, p.ProAuto, expired.Source, expired.Downstream, v.RetryCount, next)
	if err != nil {
		return true, err
	}
	r := updated.ProAuto.OAuthRecovery
	s.auditProEvent(updated, "oauth", "pending", "pro_auto", updated.ProAuto.Error, map[string]any{"recovery_attempt": r.Attempts, "recovery_limit": v.RetryCount, "exhausted": r.Exhausted})
	if r.Exhausted {
		return true, errors.New(updated.ProAuto.Error)
	}
	if err = waitProAuto(ctx, time.Until(next)); err != nil {
		return true, err
	}
	latest, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return true, err
	}
	if latest.ProAuto.ID != p.ProAuto.ID || latest.ProAuto.OrderID != p.ProAuto.OrderID || latest.ProAuto.StopVersion != p.ProAuto.StopVersion || latest.ProAuto.StopRequested {
		return true, context.Canceled
	}
	current, _, _, err := s.proNewPushSettings(latest)
	if err != nil {
		return true, err
	}
	if expired.Source != proOAuthSource(current) || !expired.Downstream.Matches(model.ProDownstream(current)) {
		return true, errors.New("推送授权配置已变化，停止已付款授权恢复")
	}
	return true, nil
}
