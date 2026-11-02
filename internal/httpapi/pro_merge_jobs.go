package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

var errProTransferUncertain = errors.New("合并结果待确认：请求中断，远端可能已经合并。请核实后将合并步骤改为成功再续跑；确认未合并才改为待处理重试")

func proTransferNeedsConfirmation(p model.MailAccountProfile) bool {
	if p.TransferStatus == "unknown" {
		return true
	}
	// Older versions incorrectly recorded a cancelled transfer as failed.
	return p.TransferStatus == "failed" && (strings.Contains(p.ProLastError, "context canceled") || strings.Contains(p.ProLastError, "context deadline exceeded"))
}

// The caller holds the account lock. A non-nil unlock transfers ownership to
// the worker; nil queues a worker that acquires the same lock after the caller
// finishes its quota check. No request context is retained.
func (s *Server) startProMergeJob(email string, unlock func()) (model.MailAccountProfile, error) {
	return s.startProEndJob(email, unlock, nil, "manual_merge")
}

func (s *Server) startProMergeJobWithQuality(email string, unlock func(), quality *proQualityMergeRequest) (model.MailAccountProfile, error) {
	return s.startProEndJob(email, unlock, quality, "merge")
}

func (s *Server) startProEndJob(email string, unlock func(), quality *proQualityMergeRequest, action string) (model.MailAccountProfile, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.proMergeMu.Lock()
	defer s.proMergeMu.Unlock()
	if s.proMergeClosed {
		return model.MailAccountProfile{}, errors.New("服务正在停止，请稍后重试")
	}
	if _, exists := s.proMergeJobs[email]; exists {
		return model.MailAccountProfile{}, errors.New("该账号已在后台执行合并或删除流程")
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.IsProDead() && !(action == "merge_v2" && p.MergeV2DeadCleanupAllowed()) {
		return p, errProAccountDead
	}
	if !p.ProActionsAllowed() {
		return p, errors.New("账号不在 Pro 管理中")
	}
	manual := action == "manual_deactivate" || action == "manual_merge" || action == "manual_merchant" || action == "manual_merge_v2"
	if !manual && (p.ProAuto.StopRequested || p.ProAuto.Status == "stopped") && !p.MergeV2DeadCleanupAllowed() {
		return p, errors.New("用户已停止该账号流程，请手动续跑")
	}
	if action == "manual_merge_v2" {
		action = "merge_v2"
	}
	if p.MergeV2Started() && !p.MergeV2Completed() && action != "merge_v2" && action != "automatic" {
		return p, errors.New("合并空间2.0尚未结束，请先核对该流程")
	}
	if action == "manual_merchant" {
		action = "merchant"
	}
	if p.MerchantNeedsContinuation() && action != "merchant" && action != "automatic" {
		return p, errors.New("商户申请尚未结束，请先核对商户处理结果")
	}
	resumeDeletion := action == "manual_deactivate"
	if resumeDeletion {
		action = "deactivate"
	}
	if action == "manual_merge" {
		action = "merge"
	}
	if action == "automatic" {
		cfg, _, _, e := s.store.ProSettings()
		if e != nil {
			return p, e
		}
		action = cfg.Disposition()
		if p.DeletionSubmitted() {
			action = "deactivate"
		}
		if p.MerchantStarted() {
			action = "merchant"
		}
		if p.MergeV2Started() {
			action = "merge_v2"
		}
	}
	if !p.ProFinalStepAllowed(action) {
		return p, errors.New("邮件账号只能执行已选择的收尾流程")
	}
	if action == "merchant" && p.MerchantCompleted() && p.ProMerchant.RetiredAt != nil && !p.ProPostMergeGroupsPending() && !p.ProVM2CleanupPending() {
		return p, errors.New("API 商户已处理成功，无需重复执行")
	}
	if action == "deactivate" && p.DeletionConfirmed() && !resumeDeletion {
		return p, errors.New("删除已确认完成，后续分组调整请手动续跑")
	}
	if action == "merge_v2" && p.MergeV2Completed() {
		return p, errors.New("合并空间2.0已完成，无需重复执行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	var finishFinalStep func()
	if !manual {
		ctx, finishFinalStep, err = s.beginProAutomaticFinalStep(ctx)
		if err != nil {
			cancel()
			return p, err
		}
	}
	started := false
	defer func() {
		if !started {
			if finishFinalStep != nil {
				finishFinalStep()
			}
			cancel()
		}
	}()
	if action == "merge" && proTransferNeedsConfirmation(p) {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.TransferStatus = "unknown"
			p.ProLastError = errProTransferUncertain.Error()
		})
		return p, errProTransferUncertain
	}
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProWorkflowRunning, p.ProLastError = true, ""
		if manual {
			p.ProAuto.StopRequested = false
			if p.ProMigration != nil {
				p.ProMigration.Paused = false
				p.ProMigration.Notice = "已从保存的进度续跑合并/删除流程"
			}
		}
	})
	if err != nil {
		return p, err
	}
	finishStage := s.beginProManualStage(email, action)
	if quality != nil {
		ctx = context.WithValue(ctx, proQualityMergeKey{}, quality)
	}
	if s.proMergeJobs == nil {
		s.proMergeJobs = make(map[string]context.CancelFunc)
	}
	s.proMergeJobs[email] = cancel
	s.proMergeWG.Add(1)
	label := "空间合并"
	if action == "merge_v2" {
		label = "合并空间2.0"
	}
	if action == "deactivate" {
		label = "删除账户"
	}
	if action == "merchant" {
		label = "API 商户"
	}
	s.auditProEvent(p, action, "pending", "", label+"已提交后台，关闭或刷新页面不影响执行", nil)
	started = true
	go func() {
		defer s.proMergeWG.Done()
		defer cancel()
		if finishFinalStep != nil {
			defer finishFinalStep()
		}
		defer func() {
			s.proMergeMu.Lock()
			delete(s.proMergeJobs, email)
			if unlock != nil {
				unlock()
			}
			s.proMergeMu.Unlock()
		}()
		var runErr error
		defer func() { finishStage(runErr) }()
		if unlock == nil {
			value, _ := s.proLocks.LoadOrStore("account:"+strings.ToLower(email), &sync.Mutex{})
			unlock, runErr = lockProMutex(ctx, value.(*sync.Mutex))
		}
		if runErr == nil {
			if action == "merge_v2" {
				_, runErr = s.performProMergeV2(ctx, email)
			} else if action == "merchant" {
				_, runErr = s.performProMerchant(ctx, email)
			} else if action == "deactivate" {
				_, runErr = s.performProDeactivation(ctx, email)
			} else {
				_, runErr = s.performProMerge(ctx, email)
			}
		}
		if errors.Is(context.Cause(ctx), errProAutoFinalStepPaused) {
			runErr = errProAutoFinalStepPaused
			_ = s.waitProAutoFinalStepResume(email)
		}
		s.finishProQualityMerge(email, quality, runErr)
		// Also clear the queued flag if preparation or lock acquisition failed.
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProWorkflowRunning = false
			if runErr == nil && p.DailyMergeMatches() && (action != "deactivate" || p.DeletionAccepted()) && (action != "merchant" || p.MerchantCompleted()) && (action != "merge_v2" || p.MergeV2Completed()) {
				p.ProDailyMerge.Completed = true
			}
			if runErr != nil && !errors.Is(runErr, errProAutoFinalStepPaused) {
				p.ProLastError = runErr.Error()
			} else if runErr == nil && p.ProMigration != nil && action == "merge" {
				p.ProMigration.Paused = false
				p.ProMigration.Notice = "迁移流程已完成"
				if p.ProAuto.ID != "" {
					p.ProAuto.Status, p.ProAuto.Stage, p.ProAuto.Error = "completed", "merge", ""
					if p.ProAuto.Steps == nil {
						p.ProAuto.Steps = map[string]string{}
					}
					p.ProAuto.Steps["merge"] = "completed"
				}
			}
		})
	}()
	return p, nil
}

func (s *Server) proMergeSlot(ctx context.Context) (func(), error) {
	return s.proConcurrencySlot(ctx, true)
}

func (s *Server) proAutoSlot(ctx context.Context) (func(), error) {
	return s.proConcurrencySlot(ctx, false)
}

func (s *Server) proConcurrencySlot(ctx context.Context, merge bool) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Queued work follows saved changes. Running work is allowed to finish
		// when the limit is lowered; no replacement enters until below it.
		settings, _, _, err := s.store.ProSettings()
		if err != nil {
			return nil, err
		}
		limit, running := settings.Concurrency, &s.proAutoRunning
		if merge {
			limit, running = settings.MergeConcurrency, &s.proMergeRunning
		}
		s.proMergeMu.Lock()
		if *running < max(1, limit) {
			*running++
			s.proMergeMu.Unlock()
			return sync.OnceFunc(func() { s.proMergeMu.Lock(); *running--; s.proMergeMu.Unlock() }), nil
		}
		s.proMergeMu.Unlock()
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func lockProMutex(ctx context.Context, lock *sync.Mutex) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if lock.TryLock() {
			return lock.Unlock, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Server) stopProMergeJobs() {
	s.proMergeMu.Lock()
	s.proMergeClosed = true
	for _, cancel := range s.proMergeJobs {
		cancel()
	}
	s.proMergeMu.Unlock()
	s.proMergeWG.Wait()
}
