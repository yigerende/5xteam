package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Accept manual intent even when an old job is running. Resetting its state
// must wait until that job (including its defers and login child) has exited.
func (s *Server) restartProMergeV2(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	var in struct {
		RequestID string `json:"request_id"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	in.RequestID = strings.TrimSpace(in.RequestID)
	if len(in.RequestID) < 8 || len(in.RequestID) > 100 {
		writeAPI(w, 400, nil, "重新执行请求缺少有效编号")
		return
	}
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	s.proMergeMu.Lock()
	defer s.proMergeMu.Unlock()
	s.proOAIMu.Lock()
	defer s.proOAIMu.Unlock()
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	seen, err := s.store.ProMergeV2RestartSeen(p.ID, in.RequestID)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if seen || p.MergeV2RestartPending() || p.ProMergeV2Restart != nil && p.ProMergeV2Restart.ID == in.RequestID {
		writeAPI(w, 202, p, "")
		return
	}
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProMergeV2Restart = &model.ProMergeV2Restart{ID: in.RequestID, Status: "pending", RequestedAt: time.Now()}
		p.ProAuto.StopVersion++
		p.ProAuto.StopRequested = true
		p.ProAuto.NextCheckAt, p.ProAuto.RetryAt = nil, nil
		for _, v := range p.ProOAIConfirmations {
			if v == nil || v.ConfirmedAt != nil || v.Status == "disabled" || v.Status == "stopped" {
				continue
			}
			v.Generation++
			v.Status, v.NextAttemptAt, v.Error = "stopped", nil, "手动重新执行合并2.0，等待新流程"
		}
	})
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	for _, cancel := range []context.CancelFunc{s.proAutoJobs[email], s.proMergeJobs[email]} {
		if cancel != nil {
			cancel()
		}
	}
	if job, ok := s.proOAIJobs.Load(email); ok {
		job.(*proOAIJob).Cancel()
	}
	writeAPI(w, 202, p, "")
}

func (s *Server) tickProMergeV2Restarts(ctx context.Context) {
	items, err := s.store.PendingProMergeV2Restarts()
	if err != nil {
		return
	}
	for _, p := range items {
		if ctx.Err() != nil {
			return
		}
		s.startPendingProMergeV2Restart(p.Email)
	}
}

func (s *Server) startPendingProMergeV2Restart(email string) {
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	s.proOAIMu.Lock()
	defer s.proOAIMu.Unlock()
	if s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) {
		return
	}
	if _, running := s.proOAIJobs.Load(email); running {
		return
	}
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	transferred := false
	defer func() {
		if !transferred {
			lock.Unlock()
		}
	}()
	s.proMergeMu.Lock()
	closedOrRunning := s.proMergeClosed || s.proMergeJobs[email] != nil
	s.proMergeMu.Unlock()
	if closedOrRunning {
		return
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil || !p.MergeV2RestartPending() {
		return
	}
	if p.ProMergeV2Restart.Status == "pending" {
		cfg, _, _, e := s.proAccountPushSettings(p)
		if e != nil {
			s.failProMergeV2Restart(email, e)
			return
		}
		adminID, teamID, seat := cfg.TargetAdminID, "", cfg.TargetSeatType
		if p.ProMergeV2 != nil {
			adminID, teamID, seat = p.ProMergeV2.AdminID, p.ProMergeV2.TeamID, p.ProMergeV2.SeatType
		}
		if teamID == "" {
			admin, _, e := s.store.AdminAccountCredential(adminID)
			if e != nil {
				s.failProMergeV2Restart(email, e)
				return
			}
			teamID = admin.TeamAccountID
		}
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			now := time.Now()
			p.ProMergeV2Restart.Status = "starting"
			p.ProMergeV2 = &model.ProMergeV2{ManualRestartID: p.ProMergeV2Restart.ID, Status: "pending", Stage: "invite", Steps: map[string]string{}, AdminID: adminID, TeamID: teamID, SeatType: seat, StartedAt: now, UpdatedAt: now, Message: "手动重新执行，从邀请开始"}
			p.ProWorkflowRunning, p.ProAuto.StopRequested, p.ProLastError = false, false, ""
			p.ProAuto.NextCheckAt, p.ProAuto.RetryAt = nil, nil
			if p.ProMigration != nil {
				p.ProMigration.Paused = false
			}
			p.ProPostMergeGroups = nil
		})
		if err != nil {
			return
		}
	}
	_, err = s.startProEndJob(email, lock.Unlock, nil, "restart_merge_v2")
	if err != nil {
		s.failProMergeV2Restart(email, err)
		return
	}
	transferred = true
}

func (s *Server) failProMergeV2Restart(email string, err error) {
	_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.ProMergeV2Restart != nil {
			p.ProMergeV2Restart.Status, p.ProMergeV2Restart.Error = "failed", mergeV2DiagnosticText(err.Error())
		}
	})
}
