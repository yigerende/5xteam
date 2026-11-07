package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type mergeV2PlanKey struct{}
type mergeV2PlanOptions struct{ TeamID, UserID, PersonalID string }

// claimMergeV2Stage persists the first attempt before I/O. Subsequent attempts
// are claimed by the scheduler; entering a new stage starts its own budget.
func (s *Server) claimMergeV2Stage(email, phase string) (model.MailAccountProfile, error) {
	return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		m := p.ProMergeV2
		if m.Recovery == nil {
			m.Recovery = &model.ProMergeV2Recovery{Cycles: 1}
		}
		if m.Recovery.Phase != phase {
			m.Recovery.SetPhase(phase)
			m.Recovery.Attempts = 1
		}
		now := time.Now()
		m.Recovery.CheckedAt = &now
	})
}

func (s *Server) performProMergeV2(ctx context.Context, email string) (out model.MailAccountProfile, runErr error) {
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	cfg, _, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return p, err
	}
	rule := cfg.MergeV2RecoveryConfig()
	if p.ProMergeV2 != nil && p.ProMergeV2.ManualRestartID != "" {
		rule.Enabled = true
	}
	if p.MergeV2Completed() {
		return p, nil
	}
	if p.ProAuto.StopRequested && !p.MergeV2DeadCleanupAllowed() || ctx.Err() != nil {
		return p, context.Canceled
	}
	attempted := false
	defer func() {
		if !attempted {
			return
		}
		latest, _, e := s.store.MailAccountCredential(email)
		if e != nil {
			runErr = errors.Join(runErr, e)
			return
		}
		m := latest.ProMergeV2
		if m == nil || m.CompletedAt != nil {
			return
		}
		out, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			m := p.ProMergeV2
			if m.Recovery == nil {
				m.Recovery = &model.ProMergeV2Recovery{Cycles: 1}
			}
			r := m.Recovery
			if r.Exhausted {
				return
			}
			if runErr != nil {
				r.Error = mergeV2DiagnosticText(runErr.Error())
				if r.OriginalError == "" {
					r.OriginalError = r.Error
				}
			}
			switch {
			case p.MergeV2Resolved():
				r.SetPhase("tail")
			case m.SubmittedAt != nil:
				if r.Phase != "checking" && r.Phase != "reinvite" {
					r.SetPhase("removing")
				}
			case r.Phase != "cleanup":
				var retry *mergeV2RetryError
				canRetry := errors.As(runErr, &retry)
				r.NetworkFailure = canRetry && workflow.IsRetryableConnectionError(retry.cause)
				if !canRetry || r.Attempts >= r.Limit(cfg) {
					if errors.Is(runErr, context.Canceled) || errors.Is(context.Cause(ctx), errProAutoFinalStepPaused) {
						return
					}
					r.Failure = r.Error
					r.SetPhase("cleanup")
					if errors.Is(runErr, errProAccountDead) && p.IsProDead() {
						version := p.ProAuto.StopVersion
						r.CleanupStopVersion = &version
					}
				}
			}
			if r.NextAt != nil {
				return
			}
			if r.Attempts >= r.Limit(cfg) {
				r.FinishFailure(m)
				return
			}
			if !rule.Enabled || p.ProAuto.StopRequested && !p.MergeV2DeadCleanupAllowed() || errors.Is(context.Cause(ctx), errProAutoFinalStepPaused) {
				return
			}
			delay := r.Delay(cfg)
			next := time.Now().Add(time.Duration(delay) * time.Second)
			r.NextAt = &next
			m.Message = fmt.Sprintf("%s；%d秒后继续（已执行%d/%d次）", mergeV2RecoveryLabel(r.Phase), delay, r.Attempts, r.Limit(cfg))
			if r.NetworkFailure {
				m.Message = "网络故障，" + m.Message
			}
			if m.SubmittedAt == nil || p.MergeV2Resolved() {
				m.Status = "interrupted"
			}
		})
		runErr = errors.Join(runErr, e)
		if e == nil && out.ProMergeV2.Recovery != nil {
			r := out.ProMergeV2.Recovery
			status := "waiting"
			if r.Exhausted {
				status = "failed"
			}
			s.auditProEvent(out, "merge_v2_recovery", status, "", out.ProMergeV2.Message, map[string]any{"phase": r.Phase, "attempts": r.Attempts, "exhausted": r.Exhausted, "error": r.Error, "next_at": r.NextAt, "verification_only": out.ProMergeV2.VerificationOnly})
		}
	}()
	m := p.ProMergeV2
	if m != nil && m.Recovery != nil {
		r := m.Recovery
		if r.Exhausted {
			return p, errors.New("合并2.0本轮已结束，请查看执行记录")
		}
		if r.NextAt != nil && time.Now().Before(*r.NextAt) {
			return p, errMergeV2Unknown
		}
		if !rule.Enabled && !p.MergeV2Resolved() {
			return p, errors.New("合并2.0自动恢复已关闭")
		}
		attempted = true
		// A verified fact or a durable verification-only policy always takes priority
		// over a stale phase left by an interrupted process or an old release.
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			m := p.ProMergeV2
			r := m.Recovery
			if p.MergeV2Resolved() {
				r.SetPhase("tail")
			} else if m.VerificationOnly && r.Phase == "reinvite" {
				r.SetPhase("checking")
			}
			if r.Attempts >= r.Limit(cfg) || r.Phase == "reinvite" && r.Cycles >= rule.MaxCycles {
				if r.PreSubmit() {
					r.Failure = "邀请或登录重试次数已用尽"
					r.SetPhase("cleanup")
				} else {
					r.FinishFailure(m)
					return
				}
			}
			r.NextAt = nil
			r.Attempts++
			now := time.Now()
			r.CheckedAt = &now
		})
		if err != nil {
			return p, err
		}
		r = p.ProMergeV2.Recovery
		if r.Exhausted {
			return p, errors.New(p.ProMergeV2.Message)
		}
		switch r.Phase {
		case "removing", "checking":
			return s.recoverProMergeV2Plan(ctx, p, rule)
		case "cleanup":
			return s.cleanupFailedProMergeV2(ctx, p)
		case "reinvite":
			p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				m := p.ProMergeV2
				r := m.Recovery
				if m.SubmittedAt != nil {
					r.Submissions = append(r.Submissions, *m.SubmittedAt)
				}
				m.SubmittedAt, m.InviteAcceptAttemptedAt, m.InviteAcceptedAt = nil, nil, nil
				m.UserID = ""
				m.Steps = map[string]string{}
				m.Stage, m.Status, m.Error = "invite", "interrupted", ""
				r.Cycles++
				r.SetPhase("resubmit")
			})
			if err != nil {
				return p, err
			}
		}
	}
	attempted = true
	return s.performProMergeV2Attempt(ctx, email)
}

func mergeV2RecoveryLabel(phase string) string {
	switch phase {
	case "invite":
		return "等待重试原团队邀请"
	case "login", "resubmit":
		return "等待重新登录原账号"
	case "removing":
		return "移出原团队并复查"
	case "checking":
		return "核验个人套餐"
	case "reinvite":
		return "原单等待重新排队"
	case "tail":
		return "继续移出及收尾，不重复提交"
	case "cleanup":
		return "本次未完成，清理原团队成员及邀请"
	}
	return "等待自动恢复"
}

func (s *Server) cleanupFailedProMergeV2(ctx context.Context, p model.MailAccountProfile) (model.MailAccountProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	m := p.ProMergeV2
	v, _ := s.autoAdminLocks.LoadOrStore(m.TeamID, &sync.Mutex{})
	unlock, err := lockProMutex(ctx, v.(*sync.Mutex))
	if err != nil {
		return p, err
	}
	defer unlock()
	release, err := s.proMergeSlot(ctx)
	if err != nil {
		return p, err
	}
	defer release()
	token, err := s.mergeV2AdminAccessToken(ctx, m.AdminID, m.TeamID)
	if err != nil {
		return p, err
	}
	admin, _, err := s.currentAdminCredential(ctx, m.AdminID)
	if err != nil {
		return p, err
	}
	settings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return p, err
	}
	client, err := workflow.NewClient(settings)
	if err != nil {
		return p, err
	}
	latest, _, err := s.store.MailAccountCredential(p.Email)
	if err != nil {
		return p, err
	}
	if latest.ProAuto.StopRequested && !latest.MergeV2DeadCleanupAllowed() || ctx.Err() != nil {
		return p, context.Canceled
	}
	if err = client.RemoveMergeV2Target(ctx, token, m.TeamID, p.Email, m.UserID, true); err != nil {
		return p, err
	}
	p, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		m := p.ProMergeV2
		if m.Steps == nil {
			m.Steps = map[string]string{}
		}
		m.Steps["remove"] = "completed"
		m.Recovery.FinishFailure(m)
		m.Message += "；已确认移出原团队，本轮已结束"
		p.ProWorkflowRunning = false
	})
	if err != nil {
		return p, err
	}
	return p, errors.New(p.ProMergeV2.Message)
}

func (s *Server) recoverProMergeV2Plan(ctx context.Context, p model.MailAccountProfile, rule model.ProMergeV2RecoverySettings) (model.MailAccountProfile, error) {
	m := p.ProMergeV2
	if m.SubmittedAt == nil || m.UserID == "" || m.AcceptedAt != nil {
		return p, errors.New("套餐兜底缺少原提交及团队身份检查点")
	}
	// One attempt is bounded independently of the complete job timeout.
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	value, _ := s.autoAdminLocks.LoadOrStore(m.TeamID, &sync.Mutex{})
	unlock, err := lockProMutex(ctx, value.(*sync.Mutex))
	if err != nil {
		return p, err
	}
	unlock = sync.OnceFunc(unlock)
	defer unlock()
	release, err := s.proMergeSlot(ctx)
	if err != nil {
		return p, err
	}
	defer release()
	token, err := s.mergeV2AdminAccessToken(ctx, m.AdminID, m.TeamID)
	if err != nil {
		return p, err
	}
	admin, _, err := s.currentAdminCredential(ctx, m.AdminID)
	if err != nil {
		return p, err
	}
	if admin.TeamAccountID != m.TeamID {
		return p, errors.New("原母号团队已变化，禁止在其他团队恢复")
	}
	settings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return p, err
	}
	client, err := workflow.NewClient(settings)
	if err != nil {
		return p, err
	}
	latest, _, err := s.store.MailAccountCredential(p.Email)
	if err != nil {
		return p, err
	}
	if latest.ProAuto.StopRequested || ctx.Err() != nil {
		return p, context.Canceled
	}
	if err = client.RemoveMergeV2Target(ctx, token, m.TeamID, p.Email, m.UserID, false); err != nil {
		return p, err
	}

	p, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		m := p.ProMergeV2
		m.Steps["remove"] = "completed"
		m.Recovery.Phase = "checking"
		m.Message = "已确认移出原团队，重新登录个人空间核验套餐"
	})
	if err != nil {
		return p, err
	}
	s.auditProEvent(p, "merge_v2_recovery", "running", "", p.ProMergeV2.Message, map[string]any{"phase": "checking"})
	unlock() // Personal login must not hold up other work on this mother.
	opts := &mergeV2PlanOptions{TeamID: m.TeamID, UserID: m.UserID, PersonalID: p.OAuthAccountID}
	var result map[string]any
	if s.proMergeV2Plan != nil {
		result, err = s.proMergeV2Plan(ctx, p)
	} else {
		result, err = s.executeOpenAILoginContext(context.WithValue(ctx, mergeV2PlanKey{}, opts), p.Email, "chatgpt_at", nil, func(d protocolOAuthDiagnostic) {
			s.auditMergeV2Login(p, "", "recovery", p.ProMergeV2.Recovery.Attempts, d)
		})
	}
	if err != nil || result["success"] != true {
		return p, fmt.Errorf("个人套餐核验未完成：%s", oauthErrorText(result, err))
	}
	if result["user_id"] != m.UserID {
		return p, errors.New("个人套餐核验用户身份不匹配")
	}
	if err = ctx.Err(); err != nil {
		return p, err
	}
	latest, _, err = s.store.MailAccountCredential(p.Email)
	if err != nil {
		return p, err
	}
	if latest.ProAuto.StopRequested {
		return p, context.Canceled
	}
	switch result["plan"] {
	case "free":
		p, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
			m := p.ProMergeV2
			m.CompletionBasis = "plan_free"
			m.Recovery.Phase = "tail"
			m.Recovery.Attempts = 0
			m.Error = ""
			m.Message = "个人套餐已核验为Free，按套餐兜底完成；退款仍以流水为准"
		})
		if err != nil {
			return p, err
		}
		s.auditProEvent(p, "merge_v2_recovery", "completed", "", p.ProMergeV2.Message, map[string]any{"completion_basis": "plan_free"})
		release()
		release = func() {} // The ordinary tail acquires its own slot.
		return s.performProMergeV2Attempt(ctx, p.Email)
	case "paid":
		p, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
			m := p.ProMergeV2
			r := m.Recovery
			if m.VerificationOnly {
				r.Phase = "checking"
				m.Message = "个人套餐仍为付费，仅继续核验原提交，不重新邀请或提交"
				return
			}
			r.Phase = "reinvite"
			r.Attempts = 0
			m.Message = "个人套餐仍为付费，原账号原单等待重新排队办理"
			if r.Cycles >= rule.MaxCycles {
				r.Exhausted = true
				m.Error = "已达到套餐兜底轮次上限，个人套餐仍为付费；已移出原团队，请人工核实"
				m.Message = m.Error
			}
		})
		return p, errors.Join(err, errors.New("个人套餐仍为付费"))
	default:
		return p, errors.New("个人套餐响应不明确，保留原提交证据")
	}
}

func (s *Server) tickProMergeV2Recovery(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	cfg, _, _, err := s.store.ProSettings()
	if err != nil {
		return
	}
	items, err := s.store.DueProMergeV2Recovery(time.Now())
	if err != nil {
		return
	}
	for _, p := range items {
		if ctx.Err() != nil {
			return
		}
		manual := p.ProMergeV2 != nil && p.ProMergeV2.ManualRestartID != ""
		if !manual && (!cfg.MergeV2RecoveryConfig().Enabled || s.checkProAutoFinalStepAllowed() != nil) {
			continue
		}
		value, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
		lock := value.(*sync.Mutex)
		if !lock.TryLock() {
			continue
		}
		if p.ProMergeV2.Recovery == nil {
			_, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
				next := time.Now().Add(time.Duration(cfg.MergeV2RecoveryConfig().RetrySeconds) * time.Second)
				p.ProMergeV2.Recovery = &model.ProMergeV2Recovery{Phase: "removing", Cycles: 1, NextAt: &next, OriginalError: p.ProMergeV2.Error}
			})
			lock.Unlock()
			continue
		}
		action := "merge_v2"
		if manual {
			action = "manual_merge_v2"
		}
		if _, err = s.startProEndJob(p.Email, lock.Unlock, nil, action); err != nil {
			lock.Unlock()
		}
	}
}
