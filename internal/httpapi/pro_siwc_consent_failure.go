package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Constructed only from the protocol's consent/grant-specific error code.
// Authentication errors from s2, quota queries or token exchange never qualify.
type siwcConsentDeniedError struct {
	Source     string
	Downstream model.ProDownstreamRef
}

func (*siwcConsentDeniedError) Error() string {
	return "SIWC 确认授权失败：HTTP 401 / not_authorized"
}

var errSIWCConsentExhausted = errors.New("SIWC 确认授权重试耗尽，按配置执行最后一步")

func siwcAuthorizationSource(v model.Sub2Settings) string {
	if v.SIWCAuthorizationSource == "sub2api" {
		return "siwc_sub2api"
	}
	return "siwc_local"
}

func (s *Server) siwcConsentPolicy(p model.MailAccountProfile, denied *siwcConsentDeniedError) (model.ProSettings, error) {
	v, _, _, err := s.proNewPushSettings(p)
	if err != nil {
		return v, err
	}
	if v.Provider != "sub2" || v.Sub2.AuthMode != "siwc" || !v.Sub2.SIWCConsentDeniedFinal ||
		denied.Source != siwcAuthorizationSource(v.Sub2) || !denied.Downstream.Matches(model.ProDownstream(v)) ||
		p.ProAuto.StopRequested || p.ProAuto.ID == "" || p.ProAuto.OrderID == "" || p.IsProDead() ||
		p.ProAuto.Steps["oauth"] == "completed" || p.ProAuto.Steps["push"] == "completed" ||
		p.PushStatus == "completed" || p.Sub2AccountID != 0 || p.ProSIWC != nil || p.VM2 != nil || p.CPAAuthFileName != "" {
		return v, errors.New("SIWC 授权失败处置条件或推送配置已变化")
	}
	if pending, e := s.proHasPushAttempt(p, v); e != nil || pending {
		return v, errors.New("存在待确认的下游导入，不能跳过推送核实")
	}
	order, _, err := s.store.GPTPayOrder(p.ProAuto.OrderID)
	if err != nil || order.Status != "success" || order.Email != p.Email {
		return v, errors.New("原订单未确认付款成功，不能执行授权失败处置")
	}
	return v, nil
}

// Retry under the existing account lock/worker limit. Every iteration invokes
// the normal fresh-session login with payment-free recovery permission only.
func (s *Server) prepareSIWCConsentRetry(ctx context.Context, email string, cause error) (bool, error) {
	var denied *siwcConsentDeniedError
	if !errors.As(cause, &denied) {
		return false, cause
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return false, err
	}
	v, err := s.siwcConsentPolicy(p, denied)
	if err != nil {
		return false, cause
	}
	retries := 0
	previous := p.ProAuto.SIWCConsentFailure
	if previous != nil && previous.OrderID == p.ProAuto.OrderID && previous.StopVersion == p.ProAuto.StopVersion &&
		previous.Source == denied.Source && previous.Downstream.Matches(denied.Downstream) {
		retries = previous.Retries
	}
	// Already-used restock recovery attempts count towards the same budget.
	retries = max(retries, p.ProAuto.RestockRecoveries)
	exhausted := retries >= v.RetryCount
	next := time.Now().Add(time.Duration(v.RetryIntervalSeconds) * time.Second)
	message := fmt.Sprintf("SIWC 确认授权被拒绝，%d 秒后重新登录授权 %d/%d；原订单已付款，不重新付款", v.RetryIntervalSeconds, retries+1, v.RetryCount)
	if exhausted {
		message = "SIWC 确认授权仍为 401 / not_authorized，重试耗尽，跳过推送和额度并执行配置的最后一步"
	}
	updated, err := s.store.UpdateProAccount(email, func(current *model.MailAccountProfile) {
		if current.ProAuto.ID != p.ProAuto.ID || current.ProAuto.StopVersion != p.ProAuto.StopVersion || current.ProAuto.StopRequested {
			return
		}
		if !exhausted {
			retries++
		}
		current.ProAuto.SIWCConsentFailure = &model.ProSIWCConsentFailure{OrderID: p.ProAuto.OrderID, Source: denied.Source, Downstream: denied.Downstream, StopVersion: p.ProAuto.StopVersion, Retries: retries, Exhausted: exhausted}
		current.ProAuto.Stage = "oauth"
		if current.ProAuto.Steps == nil {
			current.ProAuto.Steps = map[string]string{}
		}
		current.ProAuto.Steps["oauth"] = "failed"
		current.ProAuto.Error = message
		current.ProAuto.UpdatedAt = time.Now()
		current.ProAuto.RetryAt = &next
		if exhausted {
			current.ProAuto.RetryAt = nil
			current.ProAuto.Steps["push"], current.ProAuto.Steps["quota"] = "skipped", "skipped"
			for _, stage := range []string{"oauth", "push", "quota"} {
				delete(current.ProManualStages, stage)
				delete(current.ProStageOverrides, stage)
			}
		}
	})
	if err != nil {
		return false, err
	}
	if updated.ProAuto.StopRequested || updated.ProAuto.StopVersion != p.ProAuto.StopVersion {
		return false, context.Canceled
	}
	s.auditProEvent(updated, "oauth", "failed", "pro_auto", message, map[string]any{"error_code": "siwc_consent_not_authorized", "retry_count": retries, "retry_limit": v.RetryCount, "exhausted": exhausted})
	if exhausted {
		return true, errSIWCConsentExhausted
	}
	if err = waitProAuto(ctx, time.Until(next)); err != nil {
		return false, err
	}
	// Re-read after the wait: disabling the option/changing target must win.
	latest, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return false, err
	}
	if latest.ProAuto.ID != p.ProAuto.ID || latest.ProAuto.StopVersion != p.ProAuto.StopVersion {
		return false, context.Canceled
	}
	if _, err = s.siwcConsentPolicy(latest, denied); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) validateSIWCConsentFinal(p model.MailAccountProfile) error {
	if !p.ProAuto.SIWCConsentFinal() {
		return errors.New("SIWC 授权失败处置记录已失效")
	}
	v := p.ProAuto.SIWCConsentFailure
	_, err := s.siwcConsentPolicy(p, &siwcConsentDeniedError{Source: v.Source, Downstream: v.Downstream})
	return err
}
