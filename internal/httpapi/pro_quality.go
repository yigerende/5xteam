package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

type proQualityRuntime struct {
	mu              sync.Mutex
	wg              sync.WaitGroup
	cancel          context.CancelFunc
	closed, running bool
	total, done     int
	lastError       string
}

func proQualityConnection(q model.ProQualitySettings, c model.ProSettings) bool {
	return c.Provider == "sub2" && q.Downstream.Matches(model.ProDownstream(c))
}

func proQualityEligible(p model.MailAccountProfile, c model.ProSettings) bool {
	if p.IsProDead() || p.ManagementScope != "pro" || p.PushStatus != "completed" || p.PushProvider != "sub2" || p.Sub2AccountID <= 0 || p.VM2 != nil || c.Provider != "sub2" {
		return false
	}
	if p.DispositionStarted() || p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed" || proImportedDownstreamOK(p, c) != nil {
		return false
	}
	// IDs alone do not identify an account across two Sub2 installations.
	return p.ProPushDownstream != nil && p.ProPushDownstream.Matches(model.ProDownstream(c))
}

func proQualityIdentity(p model.MailAccountProfile, c model.ProSettings) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s", proCostConnectionIdentity(c.Sub2), p.ID, p.Sub2AccountID, p.OAuthAccountID, proQualitySince(p).Format(time.RFC3339Nano))
}

func proQualitySince(p model.MailAccountProfile) time.Time {
	since := p.CreatedAt
	for _, at := range []*time.Time{p.ProPushStartedAt, p.ProQualitySince} {
		if at != nil && at.After(since) {
			since = *at
		}
	}
	return since
}

func proQualityCanAct(p model.MailAccountProfile) bool {
	return !p.ProAuto.StopRequested && p.ProAuto.Status != "stopped" && !p.ProWorkflowRunning &&
		(p.ProMigration == nil || !p.ProMigration.Paused) && !proTransferNeedsConfirmation(p)
}

func (s *Server) proQualityPolicyCurrent(q model.ProQualitySettings, c model.ProSettings) bool {
	current, err := s.store.ProQualitySettings()
	if err != nil || !current.Enabled || current.Revision != q.Revision {
		return false
	}
	latest, _, _, err := s.store.ProSettings()
	return err == nil && proQualityConnection(current, latest) && model.ProDownstream(c).Matches(model.ProDownstream(latest))
}

func (s *Server) getProQualitySettings(w http.ResponseWriter, r *http.Request) {
	q, err := s.store.ProQualitySettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	c, _, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	profiles, err := s.store.ProQualityCandidates(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	summary := map[string]int{"total": 0, "normal": 0, "degraded": 0, "unknown": 0, "stale": 0, "error": 0, "routed": 0}
	for _, p := range profiles {
		if !proQualityEligible(p, c) {
			continue
		}
		summary["total"]++
		state := proQualityDisplay(p, q, c)
		status := state.Status
		if status == "" {
			status = "unknown"
		}
		summary[status]++
		if state.Routed {
			summary["routed"]++
		}
	}
	s.proQuality.mu.Lock()
	runtime := map[string]any{"running": s.proQuality.running, "total": s.proQuality.total, "done": s.proQuality.done, "error": s.proQuality.lastError}
	s.proQuality.mu.Unlock()
	reason := ""
	if c.Provider != "sub2" {
		reason = "请在 Pro 推送设置中启用 Sub2"
	} else if q.Enabled && !proQualityConnection(q, c) {
		reason = "Pro 推送连接已变化，请重新保存降智设置"
	}
	writeAPI(w, 200, map[string]any{"settings": q, "provider": c.Provider, "inactive_reason": reason, "summary": summary, "runtime": runtime}, "")
}

func (s *Server) saveProQualitySettings(w http.ResponseWriter, r *http.Request) {
	q := model.DefaultProQualitySettings()
	if decodeJSON(w, r, &q, 1<<20) != nil {
		return
	}
	if err := q.Validate(); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	c, password, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if q.Enabled {
		if c.Provider != "sub2" {
			writeAPI(w, 400, nil, "Pro 降智检测仅支持 Pro 推送设置中的 Sub2")
			return
		}
		if q.Action == "merge" && c.Disposition() == "merge" && strings.TrimSpace(c.TargetAdminID) == "" {
			writeAPI(w, 400, nil, "请先在空间合并设置中选择目标母号")
			return
		}
		if q.Action == "siwc" && c.Sub2.AuthMode != "siwc" {
			cfg, secret, e := s.store.ProSIWCSettings()
			if e != nil || cfg.Sub2.URL == "" || secret == "" || len(cfg.Sub2.GroupIDs) == 0 || len(cfg.MonitorGroupIDs) == 0 {
				writeAPI(w, 400, nil, "请先保存 SIWC 转入连接、推送和监控分组")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		err = s.sub2.QualityCapabilities(ctx, c.Sub2, password)
		if err == nil && q.Action == "groups" {
			var groups []sub2.Group
			groups, err = s.sub2.Groups(ctx, c.Sub2, password)
			valid := map[int64]bool{}
			for _, g := range groups {
				valid[g.ID] = true
			}
			for _, id := range append(append([]int64{}, q.NormalGroupIDs...), q.DegradedGroupIDs...) {
				if err == nil && !valid[id] {
					err = fmt.Errorf("分组 %d 不存在或不是 OpenAI 分组", id)
				}
			}
		}
		if err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
	}
	q.Downstream = model.ProDownstream(c)
	latest, _, _, err := s.store.ProSettings()
	if err != nil || (q.Enabled && !q.Downstream.Matches(model.ProDownstream(latest))) {
		writeAPI(w, 409, nil, "Pro 推送连接已变化，请刷新后重试")
		return
	}
	if _, err = s.store.SaveProQualitySettings(q); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	s.getProQualitySettings(w, r)
}

func (s *Server) triggerProQuality(w http.ResponseWriter, r *http.Request) {
	q, err := s.store.ProQualitySettings()
	if err != nil || !q.Enabled {
		writeAPI(w, 400, nil, "请先开启并保存 Pro 降智检测")
		return
	}
	c, _, _, err := s.store.ProSettings()
	if err != nil || !proQualityConnection(q, c) {
		writeAPI(w, 409, nil, "Pro 推送连接已变化，请重新保存降智设置")
		return
	}
	if !s.launchProQuality(true) {
		writeAPI(w, 409, nil, "Pro 降智查询正在执行或服务正在停止")
		return
	}
	writeAPI(w, 202, map[string]bool{"started": true}, "")
}

func (s *Server) startProQualityMonitor() {
	s.proQuality.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	s.proQuality.cancel = cancel
	s.proQuality.wg.Add(1)
	s.proQuality.mu.Unlock()
	go func() {
		defer s.proQuality.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.launchProQuality(false)
			}
		}
	}()
}

func (s *Server) launchProQuality(force bool) bool {
	s.proQuality.mu.Lock()
	defer s.proQuality.mu.Unlock()
	if s.proQuality.closed || s.proQuality.running {
		return false
	}
	s.proQuality.running, s.proQuality.total, s.proQuality.done, s.proQuality.lastError = true, 0, 0, ""
	// The monitor cancellation also interrupts a manual run during shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	previousCancel := s.proQuality.cancel
	s.proQuality.cancel = func() {
		cancel()
		if previousCancel != nil {
			previousCancel()
		}
	}
	s.proQuality.wg.Add(1)
	go func() {
		defer s.proQuality.wg.Done()
		defer cancel()
		err := s.runProQualityBatch(ctx, force)
		s.proQuality.mu.Lock()
		s.proQuality.running = false
		s.proQuality.cancel = previousCancel
		if err != nil {
			s.proQuality.lastError = err.Error()
		}
		s.proQuality.mu.Unlock()
	}()
	return true
}

func (s *Server) stopProQuality() {
	s.proQuality.mu.Lock()
	s.proQuality.closed = true
	if s.proQuality.cancel != nil {
		s.proQuality.cancel()
	}
	s.proQuality.mu.Unlock()
	s.proQuality.wg.Wait()
}

func (s *Server) runProQualityBatch(ctx context.Context, force bool) error {
	q, err := s.store.ProQualitySettings()
	if err != nil || !q.Enabled {
		return err
	}
	c, password, _, err := s.store.ProSettings()
	if err != nil {
		return err
	}
	if !proQualityConnection(q, c) {
		return errors.New("Pro 推送连接已变化，请重新保存降智设置")
	}
	profiles, err := s.store.ProQualityCandidates(ctx)
	if err != nil {
		return err
	}
	jobs := []model.MailAccountProfile{}
	for _, p := range profiles {
		state := p.ProQuality
		if p.ProSIWC != nil {
			if p.ProSIWC.Status == "active" && !p.DispositionStarted() && !p.SpaceMergedOnce && (force || state.NextAt == nil || !time.Now().Before(*state.NextAt)) {
				s.observeSIWCQuality(ctx, p, q)
			}
			continue
		}
		if proQualityEligible(p, c) && !p.ProWorkflowRunning && (force || state.Revision != q.Revision || state.Identity != proQualityIdentity(p, c) || state.NextAt == nil || !time.Now().Before(*state.NextAt)) {
			jobs = append(jobs, p)
		}
	}
	s.proQuality.mu.Lock()
	s.proQuality.total = len(jobs)
	s.proQuality.mu.Unlock()
	for start := 0; start < len(jobs); start += 100 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !s.proQualityPolicyCurrent(q, c) {
			return nil
		}
		batch := jobs[start:min(start+100, len(jobs))]
		ids, seen := []int64{}, map[int64]bool{}
		for _, p := range batch {
			if !seen[p.Sub2AccountID] {
				seen[p.Sub2AccountID] = true
				ids = append(ids, p.Sub2AccountID)
			}
		}
		snapshot, readErr := s.sub2.QualityResults(ctx, c.Sub2, password, ids)
		received := time.Now()
		if readErr == nil {
			readErr = proQualityClockError(snapshot, received)
		}
		byID := map[int64]sub2.RemoteQualityResult{}
		for _, v := range snapshot.Accounts {
			byID[v.AccountID] = v
		}
		queue := make(chan model.MailAccountProfile)
		var wg sync.WaitGroup
		for range min(max(1, q.Concurrency), 8) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range queue {
					currentSnapshot := snapshot
					currentSnapshot.ServerNow = snapshot.ServerNow.Add(time.Since(received))
					s.applyProQuality(ctx, p, q, c, password, currentSnapshot, byID[p.Sub2AccountID], readErr)
					s.proQuality.mu.Lock()
					s.proQuality.done++
					s.proQuality.mu.Unlock()
				}
			}()
		}
		for _, p := range batch {
			queue <- p
		}
		close(queue)
		wg.Wait()
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

var errProQualityClock = errors.New("Sub2 与本机时间相差超过 5 分钟，暂停自动处理")

func proQualityClockError(snapshot sub2.RemoteQualitySnapshot, now time.Time) error {
	if snapshot.ServerNow.Before(now.Add(-5*time.Minute)) || snapshot.ServerNow.After(now.Add(5*time.Minute)) {
		return errProQualityClock
	}
	return nil
}

func proQualityDisplay(p model.MailAccountProfile, q model.ProQualitySettings, c model.ProSettings) model.ProQualityState {
	state := p.ProQuality
	if p.PushStatus == "completed" && p.PushProvider == "sub2" && p.RemoveStatus != "completed" && p.RemoveStatus != "team_removed" && !proQualityEligible(p, c) {
		return model.ProQualityState{AccountQuality: model.AccountQuality{Status: "unknown", Error: "当前账号绑定与 Pro 推送连接不一致，请重新推送后检测"}}
	}
	if state.Identity != "" && state.Identity != proQualityIdentity(p, c) {
		return model.ProQualityState{AccountQuality: model.AccountQuality{Status: "unknown", Error: "账号或推送连接已变化，等待重新查询"}}
	}
	if state.Status == "normal" || state.Status == "degraded" {
		if state.ValidUntil == nil || !time.Now().Before(*state.ValidUntil) || state.Revision != q.Revision {
			state.Status, state.SourceFresh = "stale", false
		}
	}
	if state.Status == "suspect" && (state.FetchedAt == nil || time.Since(*state.FetchedAt) > time.Duration(q.MaxResultAgeSeconds)*time.Second || state.Revision != q.Revision) {
		state.Status = "stale"
	}
	return state
}
