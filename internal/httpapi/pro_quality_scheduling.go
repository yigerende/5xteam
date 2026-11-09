package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/model"
)

var errProQualitySchedulingPause = errors.New("降智处理前暂停 Sub2 调度失败")

// Pausing is an idempotent prerequisite, not a disposition attempt. Read back
// the switch even after a failed write: the upstream may have applied it.
func (s *Server) pauseProQualityScheduling(ctx context.Context, p model.MailAccountProfile, q model.ProQualitySettings, c model.ProSettings, password string, state model.ProQualityState) error {
	if !q.PauseBeforeAction || state.ActionStarted {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !s.proQualityPolicyCurrent(q, c) {
			return errors.New("降智配置或 Sub2 连接已变化")
		}
		latest, _, err := s.store.MailAccountCredential(p.Email)
		if err != nil {
			return err
		}
		if request, _ := ctx.Value(proQualityMergeKey{}).(*proQualityMergeRequest); request != nil && request.Identity == state.Identity && request.Revision == q.Revision {
			latest.ProWorkflowRunning = false // The queued disposition owns this lock.
		}
		if !proQualityEligible(latest, c) || !proQualityCanAct(latest) || proQualityIdentity(latest, c) != state.Identity {
			return errors.New("账号状态或 Sub2 绑定已变化")
		}
		if !state.Degraded || state.ValidUntil == nil || !time.Now().Before(*state.ValidUntil) {
			return errors.New("降智结果未确认或已过期")
		}
		return nil
	}
	pause := func() error {
		if err := validate(); err != nil {
			return err
		}
		account, err := s.sub2.FallbackAccount(ctx, c.Sub2, password, p.Sub2AccountID)
		if err != nil {
			return err
		}
		if err = validate(); err != nil {
			return err
		}
		if *account.Schedulable {
			writeErr := s.sub2.SetSchedulable(ctx, c.Sub2, password, p.Sub2AccountID, false)
			account, err = s.sub2.FallbackAccount(ctx, c.Sub2, password, p.Sub2AccountID)
			if err != nil {
				return errors.Join(writeErr, err)
			}
			if *account.Schedulable {
				return errors.Join(writeErr, errors.New("Sub2 复查仍为开启调度，等待下轮检测重试"))
			}
			s.auditProEvent(p, "quality_scheduling", "completed", "sub2", "已确认暂停该账号的 Sub2 调度", map[string]any{"account_id": p.Sub2AccountID})
		}
		return validate()
	}
	if err := pause(); err != nil {
		return fmt.Errorf("%w：%s", errProQualitySchedulingPause, redactSensitiveText(err.Error()))
	}
	return nil
}

func (s *Server) recordProQualityPauseFailure(p model.MailAccountProfile, state model.ProQualityState, err error) {
	_, _ = s.store.UpdateProAccount(p.Email, func(latest *model.MailAccountProfile) {
		if latest.ProQuality.Identity == state.Identity && latest.ProQuality.Revision == state.Revision {
			latest.ProQuality.ActionStatus, latest.ProQuality.ActionError = "failed", err.Error()
		}
	})
	s.auditProEvent(p, "quality_scheduling", "failed", "sub2", err.Error(), map[string]any{"account_id": p.Sub2AccountID})
}
