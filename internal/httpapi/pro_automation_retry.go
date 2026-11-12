package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Retry the pre-payment chain while retaining the account lock, worker slot
// and restock claim. Paid recovery deliberately remains payment-free.
func (s *Server) runProProvisioning(ctx context.Context, email string, cfg gptpay.Settings, key string, card gptpay.Card, secret gptpay.CardSecret) error {
	var err error
	paidProxyRetries := 0
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		stage, message := "login", "开始 Pro 全自动流程，建立连续登录会话"
		if isProPaidRecovery(ctx) {
			stage, message = "oauth", "原订单已开通，仅重新登录原账号恢复 RT/AT 授权，不重新开通或付款"
		}
		if err = s.autoProUpdate(email, stage, "running", message); err != nil {
			return err
		}
		sessionCtx, stop := context.WithTimeout(ctx, time.Duration(cfg.SessionTimeoutMinutes)*time.Minute)
		rpc := func(method string, args map[string]any) (any, error) {
			return s.proAutoRPC(sessionCtx, email, cfg, key, card, secret, method, args)
		}
		if s.proAutoHooks != nil && s.proAutoHooks.Session != nil {
			err = s.proAutoHooks.Session(sessionCtx, email, rpc)
		} else {
			err = s.runProContinuousSession(sessionCtx, email, rpc)
		}
		stop()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var unavailable *globalProxyWaiting
		if errors.As(err, &unavailable) {
			at := unavailable.Until
			if at.After(time.Now().Add(30 * time.Second)) {
				at = time.Now().Add(30 * time.Second)
			}
			_, saveErr := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto.RetryAt = &at
				p.ProAuto.Error = unavailable.Error()
				p.ProAuto.UpdatedAt = time.Now()
			})
			if saveErr != nil {
				return saveErr
			}
			if e := waitProAuto(ctx, time.Until(at)); e != nil {
				return e
			}
			continue
		}
		if isProPaidRecovery(ctx) {
			var cooled *cooledLoginProxyError
			if errors.As(err, &cooled) && paidProxyRetries < 3 {
				paidProxyRetries++
				if p, _, e := s.store.MailAccountCredential(email); e == nil {
					s.auditProEvent(p, "oauth", "pending", "pro_auto", "原代理已冷却，立即切换备用代理重新登录恢复授权；原单已付款，不重新付款", map[string]any{"proxy_retry_attempt": paidProxyRetries})
				}
				continue
			}
			return err
		}
		p, _, readErr := s.store.MailAccountCredential(email)
		if readErr != nil {
			return err
		}
		if p.IsProDead() {
			return errProAccountDead
		}
		if p.ProAuto.OrderID != "" {
			if order, _, e := s.store.GPTPayOrder(p.ProAuto.OrderID); e == nil && order.PaymentReviewRequired() {
				_, saveErr := s.store.UpdateProAccount(email, func(current *model.MailAccountProfile) {
					if current.ProAuto.ID == p.ProAuto.ID && current.ProAuto.OrderID == order.ID {
						current.ProAuto.PaymentReviewRequired = true
					}
				})
				if saveErr != nil {
					return fmt.Errorf("%s；保存待核实状态失败：%v：%w", gptpay.PaymentReviewMessage, saveErr, err)
				}
				return fmt.Errorf("%s：%w", gptpay.PaymentReviewMessage, err)
			}
		}
		// Step two never retries this account; the outer worker queues a successor.
		if p.ProAuto.Stage == "recharge" {
			return err
		}
		// Read the current policy when scheduling each retry, not just when the
		// workflow starts. A wait already scheduled keeps its published deadline.
		settings, _, _, readErr := s.store.ProSettings()
		if readErr != nil {
			return fmt.Errorf("%w；无法读取重试配置：%v", err, readErr)
		}
		delay := time.Duration(settings.RetryIntervalSeconds) * time.Second
		if isLoginProxyNetworkFailure(err) && p.ProAuto.Stage == "login" && p.ProAuto.OrderID == "" && p.ProAuto.Steps["recharge"] != "completed" {
			const limit = 3
			if p.ProAuto.ProxyRetryAttempts >= limit {
				return fmt.Errorf("代理隧道连续不可用，已更换会话重试 %d 次，尚未付款：%w", limit, err)
			}

			var cooled *cooledLoginProxyError
			if errors.As(err, &cooled) {
				delay = 0
			}
			at := time.Now().Add(delay)
			message := fmt.Sprintf("代理隧道失败，%d 秒后更换代理会话重试 %d/%d；保留原账号和原卡，不占账号业务重试次数", int(delay.Seconds()), p.ProAuto.ProxyRetryAttempts+1, limit)
			if cooled != nil {
				message = fmt.Sprintf("原代理已冷却，立即选择可用备用代理从头登录 %d/%d；保留原账号和原卡", p.ProAuto.ProxyRetryAttempts+1, limit)
			}
			retried, e := s.store.PrepareProAutoProxyRetry(email, p.ProAuto, limit, at, message)
			if e != nil {
				return fmt.Errorf("%w；代理重试未执行：%v", err, e)
			}
			s.auditProEvent(retried, "login", "pending", "pro_auto", message, map[string]any{
				"proxy_error": redactSensitiveText(err.Error()), "automation_id": p.ProAuto.ID,
				"proxy_retry_attempt": retried.ProAuto.ProxyRetryAttempts, "retry_limit": limit,
				"retry_interval_seconds": settings.RetryIntervalSeconds, "retry_at": at,
			})
			if e = waitProAuto(ctx, time.Until(at)); e != nil {
				return e
			}
			continue
		}
		if p.ProAuto.RetryAttempts >= settings.RetryCount {
			if p.ProAuto.RetryAttempts > 0 {
				return fmt.Errorf("原账号重试 %d 次后仍失败：%w", p.ProAuto.RetryAttempts, err)
			}
			return err
		}
		// Completed payment/OAuth continues through the existing recovery path.
		if p.ProAuto.Stage != "login" && p.ProAuto.Stage != "recharge" || p.ProAuto.Steps["recharge"] == "completed" {
			return err
		}
		at := time.Now().Add(delay)
		message = fmt.Sprintf("原账号重试 %d/%d，%d 秒后从登录继续；上次失败：%s", p.ProAuto.RetryAttempts+1, settings.RetryCount, settings.RetryIntervalSeconds, err)
		retried, retryErr := s.store.PrepareProAutoRetry(email, p.ProAuto, settings.RetryCount, at, message)
		if retryErr != nil {
			return fmt.Errorf("%w；未自动重试：%v", err, retryErr)
		}
		s.auditProEvent(retried, "login", "pending", "pro_auto", message, map[string]any{
			"automation_id": p.ProAuto.ID, "previous_order_id": p.ProAuto.OrderID,
			"retry_attempt": retried.ProAuto.RetryAttempts, "retry_limit": settings.RetryCount, "retry_at": at,
		})
		if err = waitProAuto(ctx, time.Until(at)); err != nil {
			return err
		}
	}
}

// Only connection establishment failures qualify; never replay a payment RPC.
func isProProxyTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "connect tunnel failed") || strings.Contains(m, "proxy connect aborted") || strings.Contains(m, "could not resolve proxy") || strings.Contains(m, "failed to connect to proxy")
}
