package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

func qualityPollSeconds(q model.QualitySettings) int {
	if q.PollIntervalSeconds >= 10 {
		return q.PollIntervalSeconds
	}
	return 60
}
func qualityMaxAge(q model.QualitySettings) time.Duration {
	if q.MaxResultAgeSeconds >= 30 {
		return time.Duration(q.MaxResultAgeSeconds) * time.Second
	}
	return 15 * time.Minute
}
func remoteQualityFresh(v sub2.RemoteQualityVerdict, since, now time.Time, maxAge time.Duration) bool {
	if v.Status != "normal" && v.Status != "variant" && v.Status != "suspect" && v.Status != "degraded" {
		return false
	}
	return v.Error == "" && v.EvidenceAt != nil && !v.EvidenceAt.Before(since) && !v.EvidenceAt.After(now.Add(5*time.Second)) && now.Sub(*v.EvidenceAt) <= maxAge && v.StreakStartedAt != nil && !v.StreakStartedAt.Before(since)
}
func remoteQualityDecision(q model.QualitySettings, p model.FreeAccountProfile, snapshot sub2.RemoteQualitySnapshot, v sub2.RemoteQualityResult) (bad, recovered bool) {
	if !snapshot.Settings.Enabled || snapshot.Settings.Revision == "" || v.Revision != snapshot.Settings.Revision || v.Version == "" {
		return false, false
	}
	since := p.CreatedAt
	for _, at := range []*time.Time{p.JoinedAt, p.PushedAt} {
		if at != nil && at.After(since) {
			since = *at
		}
	}
	results := []bool{}
	normal := true
	for _, part := range []struct {
		selected, enabled bool
		v                 sub2.RemoteQualityVerdict
	}{{q.QuestionEnabled, snapshot.Settings.QuestionEnabled, v.Question.RemoteQualityVerdict}, {q.ModelAuditEnabled, snapshot.Settings.ModelAuditEnabled, v.Model.RemoteQualityVerdict}} {
		if !part.selected {
			continue
		}
		fresh := part.enabled && remoteQualityFresh(part.v, since, snapshot.ServerNow, qualityMaxAge(q))
		abnormal := fresh && part.v.Degraded && (part.v.Status == "degraded" || part.v.Status == "suspect")
		results = append(results, abnormal)
		normal = normal && fresh && !part.v.Degraded && (part.v.Status == "normal" || part.v.Status == "variant") && part.v.Successes >= snapshot.Settings.RecoveryLimit && part.v.Successes >= q.RecoveryLimit
	}
	if len(results) == 0 {
		return false, false
	}
	bad = q.ConditionMode == "all"
	for _, v := range results {
		if q.ConditionMode == "all" {
			bad = bad && v
		} else {
			bad = bad || v
		}
	}
	return bad, normal
}
func (s *Server) runRemoteQualityBatch(ctx context.Context, force bool, onlyID string) {
	q, err := s.store.QualitySettings()
	if err != nil || !q.Enabled {
		return
	}
	push, password, err := s.store.Sub2Settings()
	if err != nil || providerForSettings(push) != "sub2" {
		return
	}
	profiles, err := s.store.QualityAccounts()
	if err != nil {
		return
	}
	jobs := []model.FreeAccountProfile{}
	for _, p := range profiles {
		if onlyID != "" && p.ID != onlyID {
			continue
		}
		if (qualityEligible(p) || qualityActionRetryEligible(p)) && (force || qualityDue(p, q, push, time.Now())) {
			jobs = append(jobs, p)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		a, b := jobs[i].Quality.NextAt, jobs[j].Quality.NextAt
		if a == nil {
			return b != nil
		}
		if b == nil {
			return false
		}
		return a.Before(*b)
	})
	s.qualityMu.Lock()
	s.qualityRuntime = qualityRuntime{Running: true, Total: len(jobs), Active: map[string]bool{}}
	s.qualityMu.Unlock()
	defer func() { s.qualityMu.Lock(); s.qualityRuntime.Running = false; s.qualityMu.Unlock() }()
	for start := 0; start < len(jobs); start += 100 {
		if ctx.Err() != nil || !s.qualityPolicyCurrent(q, push) {
			return
		}
		end := start + 100
		if end > len(jobs) {
			end = len(jobs)
		}
		batch := jobs[start:end]
		ids := []int64{}
		seen := map[int64]bool{}
		for _, p := range batch {
			if !seen[p.Sub2AccountID] {
				seen[p.Sub2AccountID] = true
				ids = append(ids, p.Sub2AccountID)
			}
		}
		snapshot, readErr := s.sub2.QualityResults(ctx, push, password, ids)
		if readErr == nil && (snapshot.ServerNow.Before(time.Now().Add(-5*time.Minute)) || snapshot.ServerNow.After(time.Now().Add(5*time.Minute))) {
			readErr = errors.New("Sub2 与本机时间相差超过5分钟，暂停自动处理")
		}
		byID := map[int64]sub2.RemoteQualityResult{}
		for _, v := range snapshot.Accounts {
			byID[v.AccountID] = v
		}
		queue := make(chan model.FreeAccountProfile)
		var wg sync.WaitGroup
		workers := q.Concurrency
		if workers < 1 {
			workers = 1
		}
		if workers > 8 {
			workers = 8
		}
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range queue {
					s.applyRemoteQuality(ctx, p, q, push, password, snapshot, byID[p.Sub2AccountID], readErr)
					s.qualityMu.Lock()
					s.qualityRuntime.Done++
					s.qualityMu.Unlock()
				}
			}()
		}
	send:
		for _, p := range batch {
			select {
			case queue <- p:
			case <-ctx.Done():
				break send
			}
		}
		close(queue)
		wg.Wait()
	}
}
func (s *Server) applyRemoteQuality(ctx context.Context, initial model.FreeAccountProfile, q model.QualitySettings, push model.Sub2Settings, password string, snapshot sub2.RemoteQualitySnapshot, v sub2.RemoteQualityResult, readErr error) {
	if ctx.Err() != nil {
		return
	}
	lockValue, _ := s.freeLocks.LoadOrStore(initial.ID, &sync.Mutex{})
	mutex := lockValue.(*sync.Mutex)
	if !mutex.TryLock() {
		return
	}
	defer mutex.Unlock()
	p, _, err := s.store.FreeAccountCredential(initial.ID)
	if err != nil || qualityIdentity(p, push) != qualityIdentity(initial, push) || !s.qualityPolicyCurrent(q, push) || (!qualityEligible(p) && !qualityActionRetryEligible(p)) {
		return
	}
	state := p.Quality
	now := time.Now().UTC()
	next := now.Add(time.Duration(qualityPollSeconds(q)) * time.Second)
	state.Revision = q.Revision
	state.Identity = qualityIdentity(p, push)
	state.NextAt = &next
	state.ModelAudit.NextAt = &next
	// Finish already-performed removals even when the remote result service is unavailable.
	if qualityActionRetryEligible(p) {
		p.Quality = state
		_, _ = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(a *model.FreeAccountProfile) { a.Quality = state })
		s.applyQualityAction(ctx, p, q, push, password)
		return
	}
	if readErr != nil {
		state.SourceFresh = false
		state.Error = "Sub2 降智结果读取失败: " + readErr.Error()
		state.Status = "error"
		_, _ = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(a *model.FreeAccountProfile) { a.Quality = state })
		return
	}
	bad, recovered := remoteQualityDecision(q, p, snapshot, v)
	same := state.SourceVersion == v.Revision+"|"+v.Version
	state.SourceVersion = v.Revision + "|" + v.Version
	state.SourceFailureLimit = snapshot.Settings.FailureLimit
	state.SourceModel = snapshot.Settings.ModelAuditModel
	state.SourceQuestionAt = v.Question.EvidenceAt
	state.SourceModelAt = v.Model.EvidenceAt
	state.CheckedAt = v.Question.CheckedAt
	state.QuestionStatus = v.Question.Status
	state.QuestionID = v.Question.QuestionID
	state.QuestionName = v.Question.QuestionName
	state.Answer = v.Question.Answer
	state.DurationMS = v.Question.DurationMS
	state.Reason = v.Question.Reason
	state.Failures = v.Question.Failures
	state.Successes = v.Question.Successes
	state.QuestionDegraded = v.Question.Degraded
	state.ContentPassed = &v.Question.ContentPassed
	state.TimePassed = &v.Question.TimePassed
	state.ModelAudit.Status = v.Model.Status
	state.ModelAudit.Failures = v.Model.Failures
	state.ModelAudit.Successes = v.Model.Successes
	state.ModelAudit.Degraded = v.Model.Degraded
	state.ModelAudit.CheckedAt = v.Model.CheckedAt
	state.ModelAudit.SampleAt = v.Model.EvidenceAt
	state.ModelAudit.SentModel = v.Model.SentModel
	state.ModelAudit.ResponseModel = v.Model.ResponseModel
	state.ModelAudit.NoNewSamples = v.Model.NoNewSamples
	state.ModelAudit.Error = v.Model.Error
	state.ModelAudit.Revision = q.Revision
	state.SourceFresh = bad || recovered
	state.SourceRecovered = recovered
	state.Error = ""
	if !state.SourceFresh {
		state.Error = "检测未确认、结果过期、尚无当前轮转周期样本或 Sub2 检测未启用；不执行处理"
	}
	if bad {
		state.Degraded = true
		state.Status = "degraded"
		if (q.Action == "kick" && state.Action != "kick") || (!state.Routed && (state.ActionStatus != "completed" || state.Action == "restore")) {
			state.Action = q.Action
			state.ActionStatus = "pending"
		}
		if state.Action == "restore" {
			state.Action = "groups"
			state.ActionStatus = "completed"
		}
	} else {
		refreshQualityStatus(&state, q)
		if recovered && state.Routed && q.AutoRestore {
			state.Action = "restore"
			state.ActionStatus = "pending"
		}
	}
	updated, err := s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(a *model.FreeAccountProfile) { a.Quality = state })
	if err != nil {
		return
	}
	if !same {
		s.auditAccountEvent(ctx, p.ID, "quality_remote_result", "quality", "quality_monitor", "sub2", fmt.Sprintf("读取 Sub2 降智结果：答题=%s，模型=%s，满足处理条件=%t", v.Question.Status, v.Model.Status, bad), map[string]any{"version": v.Version, "question": v.Question, "model": v.Model, "condition_mode": q.ConditionMode, "bad": bad, "recovered": recovered})
	}
	if (state.ActionStatus == "pending" || state.ActionStatus == "failed") && ((state.Action == "restore" && recovered) || (state.Action != "restore" && bad)) {
		s.applyQualityAction(ctx, updated, q, push, password)
	}
}
