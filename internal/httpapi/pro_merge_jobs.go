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
	s.proMergeMu.Lock()
	defer s.proMergeMu.Unlock()
	if s.proMergeClosed {
		return model.MailAccountProfile{}, errors.New("服务正在停止，请稍后重试")
	}
	if _, exists := s.proMergeJobs[email]; exists {
		return model.MailAccountProfile{}, errors.New("该账号已在后台执行空间合并")
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.ManagementScope != "pro" {
		return p, errors.New("账号不在 Pro 管理中")
	}
	if proTransferNeedsConfirmation(p) {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.TransferStatus = "unknown"
			p.ProLastError = errProTransferUncertain.Error()
		})
		return p, errProTransferUncertain
	}
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProWorkflowRunning = true; p.ProLastError = "" })
	if err != nil {
		return p, err
	}
	finishStage := s.beginProManualStage(email, "merge")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	if s.proMergeJobs == nil {
		s.proMergeJobs = make(map[string]context.CancelFunc)
	}
	s.proMergeJobs[email] = cancel
	s.proMergeWG.Add(1)
	s.auditProEvent(p, "space_merge", "pending", "", "空间合并已提交后台，关闭或刷新页面不影响执行", nil)
	go func() {
		defer s.proMergeWG.Done()
		defer cancel()
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
			var release func()
			release, runErr = s.proMergeSlot(ctx)
			if runErr == nil {
				_, runErr = s.performProMerge(ctx, email)
				release()
			}
		}
		// Also clear the queued flag if preparation or lock acquisition failed.
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProWorkflowRunning = false
			if runErr != nil {
				p.ProLastError = runErr.Error()
			} else if p.ProMigration != nil {
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
	settings, _, _, err := s.store.ProSettings()
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.proMergeMu.Lock()
		if s.proMergeRunning < max(1, settings.Concurrency) {
			s.proMergeRunning++
			s.proMergeMu.Unlock()
			return func() { s.proMergeMu.Lock(); s.proMergeRunning--; s.proMergeMu.Unlock() }, nil
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
