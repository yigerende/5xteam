package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

func ejhCardID(order string) string { return store.EJHID("card", order) }

func (s *Server) forcedMergeReason(p model.MailAccountProfile, c model.ProSettings, now time.Time) string {
	if c.AutoDeletePaused {
		return ""
	}
	if p.DispositionStarted() {
		return ""
	}
	if p.DailyMergeMatches() && p.ManagementScope == "pro" && p.PushStatus == "completed" && !p.ProWorkflowRunning && !p.ProAuto.StopRequested && p.ProAuto.Status != "stopped" && (p.ProMigration == nil || !p.ProMigration.Paused) && !proTransferNeedsConfirmation(p) && !(p.TransferStatus == "completed" && p.RemoveStatus == "completed") {
		r := p.ProDailyMerge
		cfg, err := s.store.ProRestockConfig()
		if err == nil && cfg.DailyEJHMergeEnabled && !now.Before(r.At) && r.Attempts <= c.RetryCount && (r.NextAt == nil || !now.Before(*r.NextAt)) && s.store.DailyEJHMergeCurrent(p) {
			return "daily_ejh"
		}
	}
	if forceMergeDue(p, c, now) {
		return "push_timeout"
	}
	return ""
}

func recordForcedMergeAttempt(p *model.MailAccountProfile, reason string, c model.ProSettings) {
	p.ProMergeTrigger = reason
	next := time.Now().Add(time.Duration(max(30, c.RetryIntervalSeconds)) * time.Second)
	if reason == "daily_ejh" {
		p.ProDailyMerge.Attempts++
		p.ProDailyMerge.NextAt = &next
	} else {
		p.ProForceMergeAttempts++
		p.ProForceMergeNextAt = &next
	}
}

func (s *Server) forcedMergeRetry(p model.MailAccountProfile, c model.ProSettings) bool {
	if p.ProMergeTrigger == "daily_ejh" {
		cfg, err := s.store.ProRestockConfig()
		return err == nil && cfg.DailyEJHMergeEnabled && p.DailyMergeMatches() && p.ProDailyMerge.Attempts <= c.RetryCount && s.store.DailyEJHMergeCurrent(p)
	}
	return p.ProMergeTrigger == "push_timeout" && p.ProPushDeadline() != nil && p.ProForceMergeAttempts <= c.RetryCount
}

func forceMergeDue(p model.MailAccountProfile, c model.ProSettings, now time.Time) bool {
	if c.AutoDeletePaused {
		return false
	}
	if p.ProAuto.StopRequested || p.DispositionStarted() {
		return false
	}
	if p.ProPushDeadline() == nil || p.ManagementScope != "pro" || p.ProPushStartedAt == nil || p.PushStatus != "completed" || p.ProWorkflowRunning || p.ProAuto.Status == "stopped" || (p.ProMigration != nil && p.ProMigration.Paused) || proTransferNeedsConfirmation(p) {
		return false
	}
	if p.TransferStatus == "completed" && p.RemoveStatus == "completed" {
		return false
	}
	if p.ProForceMergeAttempts > c.RetryCount {
		return false
	}
	if p.ProForceMergeNextAt != nil && now.Before(*p.ProForceMergeNextAt) {
		return false
	}
	return !now.Before(*p.ProPushDeadline())
}
func (s *Server) tickProForceMerge(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	cfg, _, _, e := s.store.ProSettings()
	if e != nil {
		return
	}
	now := time.Now()
	if run, err := s.store.CaptureDailyEJHMerge(now); err != nil {
		slog.Error("每日 EJH 定点合并名单保存失败", "error", err)
	} else if run != nil {
		slog.Info("每日 EJH 定点合并已登记", "day", run.Day, "count", run.Count)
	}
	profiles, err := s.store.ProForceMergeCandidates(ctx, now)
	if err != nil {
		return
	}
	for _, p := range profiles {
		reason := s.forcedMergeReason(p, cfg, now)
		if reason == "" {
			continue
		}
		if p.ProAuto.ID != "" && (reason != "daily_ejh" || p.ProAuto.Status == "waiting_quota" || p.ProAuto.Status == "running") {
			if p.ProAuto.Status != "waiting_quota" {
				continue
			}
			_, e = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { next := time.Now(); p.ProAuto.NextCheckAt = &next })
			if e == nil {
				s.queueDueProAutomation(p.Email)
			}
			continue
		}
		v, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if !lock.TryLock() {
			continue
		}
		latest, _, e := s.store.MailAccountCredential(p.Email)
		if e != nil {
			lock.Unlock()
			continue
		}
		reason = s.forcedMergeReason(latest, cfg, time.Now())
		if reason == "" {
			lock.Unlock()
			continue
		}
		_, e = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
			recordForcedMergeAttempt(p, reason, cfg)
		})
		if e != nil {
			lock.Unlock()
			continue
		}
		if _, e = s.startProDispositionJob(p.Email, lock.Unlock, nil); e != nil {
			if errors.Is(e, errProAutoFinalStepPaused) {
				_ = s.waitProAutoFinalStepResume(p.Email)
			}
			lock.Unlock()
		} else {
			message := "推送等待超时，跳过额度等待并执行" + cfg.DispositionLabel()
			if reason == "daily_ejh" {
				message = "每日 EJH 定点处理，执行已登记账号的" + cfg.DispositionLabel()
			}
			s.auditProEvent(latest, "space_merge", "pending", cfg.Provider, message, map[string]any{"trigger": reason})
		}
	}
}
