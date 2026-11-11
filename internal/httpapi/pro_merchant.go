package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
	"chatgpt-space-merge/internal/store"
)

func (s *Server) merchantProAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在执行操作")
		return
	}
	p, err := s.startProEndJob(email, lock.Unlock, nil, "manual_merchant")
	if err != nil {
		lock.Unlock()
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 202, p, "")
}

func (s *Server) performProMerchant(ctx context.Context, email string) (out model.MailAccountProfile, runErr error) {
	if err := ctx.Err(); err != nil {
		return out, err
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.IsProDead() {
		return p, errProAccountDead
	}
	if p.ProAuto.StopRequested {
		return p, context.Canceled
	}
	if p.MerchantCompleted() {
		return s.finishProMerchantRetirement(ctx, p)
	}
	slot, err := s.proMergeSlot(ctx)
	if err != nil {
		return p, err
	}
	defer slot()
	if !p.MerchantStarted() {
		if err = s.checkProQualityMerge(ctx, p, true); err != nil {
			return p, err
		}
		settings, _, _, readErr := s.proAccountPushSettings(p)
		if readErr != nil {
			return p, readErr
		}
		p, err = s.prepareProPostMergeGroups(p, settings)
		if err != nil {
			return p, err
		}
		if p.ProPostMergeGroups != nil && p.ProPostMergeGroups.Status != "completed" {
			p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProPostMergeGroups.Action = "merchant" })
			if err != nil {
				return p, err
			}
		}
	}
	p, submission, err := s.store.PrepareProMerchant(email)
	if err != nil {
		return p, err
	}
	client := s.merchantClient(submission)
	defer func() {
		// Keep the existing downstream retirement rule, without invoking any
		// local OpenAI merge/deletion flow. Failed cleanup remains resumable.
		latest, _, readErr := s.store.MailAccountCredential(email)
		if readErr == nil && latest.ProMerchant != nil && latest.ProMerchant.AcceptedAt != nil && !latest.ProAuto.StopRequested && ctx.Err() == nil {
			_, cleanupErr := s.finishProMerchantRetirement(ctx, latest)
			runErr = errors.Join(runErr, cleanupErr)
		}
		out, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			m := p.ProMerchant
			if m == nil {
				return
			}
			now := time.Now().UTC()
			m.UpdatedAt = now
			m.NextCheckAt = nil
			if runErr != nil {
				m.Error = runErr.Error()
				p.ProLastError = m.Error
			}
			if errors.Is(context.Cause(ctx), errProAutoFinalStepPaused) {
				m.Error, p.ProLastError = "", ""
				m.Message = errProAutoFinalStepPaused.Error()
			}
			if p.MerchantNeedsContinuation() && !p.ProAuto.StopRequested {
				next := now.Add(15 * time.Second)
				m.NextCheckAt = &next
			}
			if p.MerchantCompleted() && p.DailyMergeMatches() {
				p.ProDailyMerge.Completed = true
			}
		})
		runErr = errors.Join(runErr, err)
		if out.ProMerchant != nil {
			s.auditProEvent(out, "merchant", out.ProMerchant.Status, "api_merchant", out.ProMerchant.Message, map[string]any{"merchant_provider": submission.Provider, "job_id": out.ProMerchant.JobID, "error": out.ProMerchant.Error})
		}
	}()
	if p.ProMerchant.JobID == "" && p.ProMerchant.BatchID == "" {
		if p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			m := p.ProMerchant
			now := time.Now().UTC()
			m.Attempts++
			m.Status = "submitting"
			m.Error = ""
			m.Message = "正在提交商户申请"
			if m.SubmittedAt == nil {
				m.SubmittedAt = &now
			}
		}); err != nil {
			return p, err
		}
		if p.ProAuto.StopRequested || ctx.Err() != nil {
			return p, context.Canceled
		}
		receipt, submitErr := client.Submit(ctx, submission.APIKey, p.ProMerchant.RequestID, submission.Payload, p.Email)
		// Persist returned acceptance even if stop arrived while POST was in flight.
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			m := p.ProMerchant
			now := time.Now().UTC()
			m.Status = "waiting"
			if submitErr != nil {
				m.Message = "提交结果未确认，保留原申请"
				if m.Attempts >= 3 {
					m.Status = "failed"
					m.Message = "提交未确认，自动重试已停止，可手动续跑原申请"
				}
				return
			}
			m.JobID, m.BatchID = receipt.JobID, receipt.BatchID
			m.AcceptedAt = &now
			m.Error = ""
			m.Message = "商户已受理，等待处理进度"
		})
		return p, errors.Join(submitErr, err)
	}
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	if p.ProMerchant.JobID == "" {
		id, resolveErr := client.ResolveBatch(ctx, submission.APIKey, p.ProMerchant.BatchID, p.Email)
		if resolveErr != nil {
			return p, resolveErr
		}
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProMerchant.JobID = id })
		if err != nil {
			return p, err
		}
	}
	job, queryErr := client.Progress(ctx, submission.APIKey, p.ProMerchant.JobID, p.Email)
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		m := p.ProMerchant
		now := time.Now().UTC()
		m.CheckedAt = &now
		if queryErr != nil {
			m.Status = "waiting"
			return
		}
		m.RemoteStatus, m.Message, m.Error = job.Status, job.Label(), ""
		p.ProLastError = ""
		m.Status = "waiting"
		if job.TerminalFailure() {
			m.Status = "failed"
			m.Error = job.Label()
		}
		if job.Succeeded() {
			m.Status = "completed"
			m.CompletedAt = &now
		}
	})
	return p, errors.Join(queryErr, err)
}

func (s *Server) merchantClient(submission store.ProMerchantSubmission) refundmerchant.Client {
	client := refundmerchant.Client{Provider: submission.Provider, SubmitURL: submission.APIURL}
	if s.proMerchantClient != nil {
		client = *s.proMerchantClient
		client.Provider = submission.Provider
		if client.BaseURL == "" && client.SubmitURL == "" {
			client.SubmitURL = submission.APIURL
		}
	}
	return client
}

func (s *Server) finishProMerchantRetirement(ctx context.Context, p model.MailAccountProfile) (model.MailAccountProfile, error) {
	if !p.MerchantAccepted() || p.ProMerchant.RetiredAt != nil {
		return p, nil
	}
	if p.ProAuto.StopRequested || ctx.Err() != nil {
		return p, context.Canceled
	}
	if err := s.endSIWCService(ctx, p); err != nil {
		return p, err
	}
	if _, err := s.finishProDownstream(ctx, p); err != nil {
		return p, err
	}
	return s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { now := time.Now(); p.ProMerchant.RetiredAt = &now })
}

func (s *Server) tickProMerchants(ctx context.Context) {
	if s.checkProAutoFinalStepAllowed() != nil {
		return
	}
	items, err := s.store.DueProMerchants(time.Now())
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
		if _, err = s.startProEndJob(p.Email, lock.Unlock, nil, "merchant"); err != nil {
			lock.Unlock()
		}
	}
}
