package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

var errProAutoFinalStepPaused = errors.New("全自动最后一步已暂停，额度检测继续")

type proAutomaticFinalStepJob struct{ cancel context.CancelCauseFunc }

func (s *Server) setProAutoFinalStepControl(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Paused *bool `json:"paused"`
	}
	if err := decodeJSON(w, r, &input, 4096); err != nil {
		return
	}
	if input.Paused == nil {
		writeAPI(w, 400, nil, "请选择停止或启动全自动最后一步")
		return
	}
	s.proFinalStepMu.Lock()
	defer s.proFinalStepMu.Unlock()
	if err := s.store.SetProAutoDeletePaused(*input.Paused); err != nil {
		writeAPI(w, 500, nil, "保存全自动最后一步状态失败")
		return
	}
	if *input.Paused {
		for job := range s.proFinalStepJobs {
			job.cancel(errProAutoFinalStepPaused)
		}
	}
	// Resume only opens admission. The existing monitors evaluate their rules.
	writeAPI(w, 200, map[string]any{"auto_delete_paused": *input.Paused}, "")
}

func (s *Server) checkProAutoFinalStepAllowed() error {
	cfg, _, _, err := s.store.ProSettings()
	if err != nil {
		return err
	}
	if cfg.AutoDeletePaused {
		return errProAutoFinalStepPaused
	}
	return nil
}

// Register only the final step: never cancel provisioning/push/quota jobs.
// Registration and the durable switch share a lock, including queued work.
func (s *Server) beginProAutomaticFinalStep(parent context.Context) (context.Context, func(), error) {
	s.proFinalStepMu.Lock()
	defer s.proFinalStepMu.Unlock()
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	if err := s.checkProAutoFinalStepAllowed(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	job := &proAutomaticFinalStepJob{cancel: cancel}
	if s.proFinalStepJobs == nil {
		s.proFinalStepJobs = make(map[*proAutomaticFinalStepJob]struct{})
	}
	s.proFinalStepJobs[job] = struct{}{}
	return ctx, sync.OnceFunc(func() {
		s.proFinalStepMu.Lock()
		delete(s.proFinalStepJobs, job)
		cancel(nil)
		s.proFinalStepMu.Unlock()
	}), nil
}

func (s *Server) performProAutomaticFinalStep(ctx context.Context, email string, run func(context.Context, string) (model.MailAccountProfile, error)) (model.MailAccountProfile, error) {
	if p, _, e := s.store.MailAccountCredential(email); e == nil && p.IsProDead() {
		return p, errProAccountDead
	}
	finalCtx, finish, err := s.beginProAutomaticFinalStep(ctx)
	if err != nil {
		return model.MailAccountProfile{}, err
	}
	defer finish()
	p, err := run(finalCtx, email)
	if errors.Is(context.Cause(finalCtx), errProAutoFinalStepPaused) {
		err = errProAutoFinalStepPaused
	}
	return p, err
}

// Keep the full-automation account eligible for its normal quota timer. Never
// clear upstream submission facts or restart the earlier paid provisioning.
func (s *Server) waitProAutoFinalStepResume(email string) error {
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return err
	}
	cfg, _, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return err
	}
	_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.DispositionStarted() || p.ProAuto.StopRequested || p.ProAuto.Status == "completed" {
			return
		}
		// Operator pauses are not failed attempts. Leave the original deadline
		// intact so the next normal monitor can evaluate it after resuming.
		switch p.ProMergeTrigger {
		case "push_timeout":
			p.ProForceMergeAttempts = max(0, p.ProForceMergeAttempts-1)
			p.ProForceMergeNextAt, p.ProMergeTrigger = nil, ""
		case "daily_ejh":
			if p.ProDailyMerge != nil {
				p.ProDailyMerge.Attempts = max(0, p.ProDailyMerge.Attempts-1)
				p.ProDailyMerge.NextAt = nil
			}
			p.ProMergeTrigger = ""
		}
		if p.ProAuto.ID == "" {
			return
		}
		interval := max(10, p.ProAuto.QuotaIntervalSeconds)
		if cfg.Sub2.AuthMode == "siwc" {
			interval = max(10, cfg.Sub2.QuotaCheckIntervalSeconds)
		}
		next := time.Now().Add(time.Duration(interval) * time.Second)
		p.ProAuto.Status, p.ProAuto.Stage, p.ProAuto.Error = "waiting_quota", "quota", ""
		p.ProAuto.NextCheckAt = &next
		if p.ProAuto.Steps == nil {
			p.ProAuto.Steps = map[string]string{}
		}
		p.ProAuto.Steps["quota"] = "waiting"
		if p.ProAuto.SIWCConsentFinal() {
			p.ProAuto.Stage = "oauth"
			p.ProAuto.Steps["quota"] = "skipped"
			p.ProAuto.Error = "SIWC 确认授权重试耗尽，等待恢复全自动最后一步"
		}
		for _, stage := range []string{"merge", "deactivate", "merchant", "merge_v2"} {
			if p.ProAuto.Steps[stage] == "running" {
				p.ProAuto.Steps[stage] = "interrupted"
			}
		}
		p.ProLastError = ""
	})
	return err
}

func (s *Server) tickProPausedMergeV2(ctx context.Context) {
	if ctx.Err() != nil || s.checkProAutoFinalStepAllowed() != nil {
		return
	}
	items, err := s.store.DuePausedProMergeV2(time.Now())
	if err != nil {
		return
	}
	for _, p := range items {
		if ctx.Err() != nil {
			return
		}
		value, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
		lock := value.(*sync.Mutex)
		if !lock.TryLock() {
			continue
		}
		if _, err = s.startProEndJob(p.Email, lock.Unlock, nil, "merge_v2"); err != nil {
			lock.Unlock()
		}
	}
}
