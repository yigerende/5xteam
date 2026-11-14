package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type proRestockRecoveryKey struct{}
type proPaidRecoveryKey struct{}

func isProPaidRecovery(ctx context.Context) bool {
	return ctx.Value(proPaidRecoveryKey{}) != nil || ctx.Value(proRestockRecoveryKey{}) != nil
}

// Recovery can establish a new login session, but it has no payment permission.
func (s *Server) confirmRestockPaidSession(ctx context.Context, p model.MailAccountProfile, taskID string, args map[string]any) (any, error) {
	if p.ProRestockTaskID != taskID || p.ProAuto.OrderID == "" {
		return nil, errors.New("补货恢复任务与原订单不匹配")
	}
	return s.confirmProPaidSession(p, args)
}

func (s *Server) confirmProPaidSession(p model.MailAccountProfile, args map[string]any) (any, error) {
	order, paid, err := s.store.GPTPayOrder(p.ProAuto.OrderID)
	if err != nil || order.Status != "success" || order.Email != p.Email || paid.Input.Session.User.Email != p.Email || paid.Input.Session.Account.ID == "" {
		return nil, errors.New("原订单未确认成功，停止恢复授权")
	}
	session, _ := args["session"].(map[string]any)
	at, _ := session["access_token"].(string)
	info, err := workflow.DecodeUserInfo(at)
	if err != nil || info.Email != p.Email || info.AccountID != paid.Input.Session.Account.ID {
		return nil, errors.New("恢复登录账号或空间与已付款订单不一致")
	}
	if err = s.autoProUpdate(p.Email, "oauth", "running", "恢复登录身份与原成功订单一致，仅继续获取 RT/AT，不重新付款"); err != nil {
		return nil, err
	}
	return map[string]any{"status": "success"}, nil
}
func (s *Server) queueRestockPaidRecovery(p model.MailAccountProfile, settings model.ProSettings) {
	// The consent-specific policy owns this recovery budget and final disposition.
	if v := p.ProAuto.SIWCConsentFailure; v != nil && (v.Exhausted || v.Retries >= settings.RetryCount) {
		return
	}
	if p.ProAuto.StopRequested || p.ProRestockTaskID == "" || p.ProAuto.RestockRecoveries >= settings.RetryCount+1 || (p.ProAuto.RestockRecoveryAt != nil && time.Since(*p.ProAuto.RestockRecoveryAt) < time.Duration(max(30, settings.RetryIntervalSeconds))*time.Second) {
		return
	}
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	if s.proAutoClosed || s.proAutoJobs[p.Email] != nil || s.proLoginAlreadyRunning(p.Email) || len(s.proAutoJobs) >= settings.Concurrency {
		return
	}
	value, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	latest, _, err := s.store.MailAccountCredential(p.Email)
	if err != nil || latest.ProAuto.OrderID != p.ProAuto.OrderID || latest.ProAuto.StopRequested || latest.ProAuto.Status == "stopped" {
		lock.Unlock()
		return
	}
	order, _, err := s.store.GPTPayOrder(p.ProAuto.OrderID)
	if err != nil || order.Status != "success" {
		lock.Unlock()
		return
	}
	cfg, _, err := s.store.GPTPaySettings()
	if err != nil {
		lock.Unlock()
		return
	}
	_, err = s.store.UpdateProAccount(p.Email, func(v *model.MailAccountProfile) {
		now := time.Now()
		v.ProAuto.RestockRecoveries++
		v.ProAuto.RestockRecoveryAt = &now
		v.ProAuto.Status = "running"
		v.ProAuto.Stage = "oauth"
		if v.ProAuto.Steps == nil {
			v.ProAuto.Steps = map[string]string{}
		}
		v.ProAuto.Steps["login"], v.ProAuto.Steps["recharge"], v.ProAuto.Steps["oauth"] = "completed", "completed", "pending"
		v.ProAuto.RetryAt = nil
		for _, stage := range []string{"login", "recharge", "oauth"} {
			delete(v.ProManualStages, stage)
			delete(v.ProStageOverrides, stage)
		}
		v.ProAuto.Error = fmt.Sprintf("恢复已付款订单的授权，第 %d 次", v.ProAuto.RestockRecoveries)
	})
	if err != nil {
		lock.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), proRestockRecoveryKey{}, p.ProRestockTaskID))
	if s.proAutoJobs == nil {
		s.proAutoJobs = map[string]context.CancelFunc{}
	}
	s.proAutoJobs[p.Email] = cancel
	s.proAutoWG.Add(1)
	go s.runProAutomation(ctx, p.Email, lock.Unlock, cancel, false, cfg, "", gptpay.Card{}, gptpay.CardSecret{})
}
