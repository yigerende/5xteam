package httpapi

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

func qualityRecoveryReady(state model.AccountQuality, q model.QualitySettings) bool {
	if state.QuestionDegraded && !q.QuestionEnabled || state.ModelAudit.Degraded && !q.ModelAuditEnabled {
		return false
	}
	if q.QuestionEnabled && (state.QuestionStatus != "normal" || state.Successes < q.RecoveryLimit) {
		return false
	}
	a := state.ModelAudit
	if q.ModelAuditEnabled && ((a.Status != "normal" && a.Status != "variant") || a.Successes < q.RecoveryLimit) {
		return false
	}
	return q.QuestionEnabled || q.ModelAuditEnabled
}
func refreshQualityStatus(state *model.AccountQuality, q model.QualitySettings) {
	if state.Degraded {
		state.Status = "degraded"
		return
	}
	statuses := []string{}
	if q.QuestionEnabled {
		statuses = append(statuses, state.QuestionStatus)
	}
	if q.ModelAuditEnabled {
		statuses = append(statuses, state.ModelAudit.Status)
	}
	state.Status = "normal"
	for _, status := range statuses {
		if status == "suspect" || status == "degraded" {
			state.Status = "suspect"
			return
		}
	}
	for _, status := range statuses {
		if status == "error" {
			state.Status = "error"
			return
		}
	}
	for _, status := range statuses {
		if status != "normal" && status != "variant" {
			state.Status = "unknown"
			return
		}
	}
}
func qualityModelDue(p model.FreeAccountProfile, q model.QualitySettings, push model.Sub2Settings, now time.Time) bool {
	a := p.Quality.ModelAudit
	return qualityEligible(p) && (a.Revision != q.Revision || a.Identity != qualityIdentity(p, push) || a.NextAt == nil || !now.Before(*a.NextAt))
}
func modelAuditSince(p model.FreeAccountProfile, q model.QualitySettings, push model.Sub2Settings) time.Time {
	since := p.CreatedAt
	for _, v := range []*time.Time{p.JoinedAt, p.PushedAt, q.ModelAuditEnabledAt} {
		if v != nil && v.After(since) {
			since = *v
		}
	}
	if a := p.Quality.ModelAudit; a.Identity == qualityIdentity(p, push) && a.Since != nil && a.Since.After(since) {
		since = *a.Since
	}
	if since.IsZero() {
		since = time.Now().UTC()
	}
	return since
}
func (s *Server) monitorQualityModels(ctx context.Context) {
	s.qualityMu.Lock()
	if s.qualityClosed {
		s.qualityMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.qualityModelMonitorCancel = cancel
	s.qualityWG.Add(1)
	s.qualityMu.Unlock()
	defer s.qualityWG.Done()
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.qualityModelRunMu.TryLock() {
				s.runQualityModelBatch(ctx, false, "")
				s.qualityModelRunMu.Unlock()
			}
		}
	}
}
func (s *Server) runQualityModelBatch(ctx context.Context, force bool, onlyID string) {
	q, err := s.store.QualitySettings()
	if err != nil || !q.Enabled || !q.ModelAuditEnabled {
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
		if qualityEligible(p) && (force || qualityModelDue(p, q, push, time.Now())) {
			jobs = append(jobs, p)
		}
	}
	if len(jobs) == 0 {
		return
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i].Quality.ModelAudit.CheckedAt, jobs[j].Quality.ModelAudit.CheckedAt
		if a == nil {
			return b != nil
		}
		return b != nil && a.Before(*b)
	})
	s.qualityMu.Lock()
	s.qualityModelRuntime = qualityRuntime{Running: true, Total: len(jobs)}
	s.qualityMu.Unlock()
	defer func() { s.qualityMu.Lock(); s.qualityModelRuntime.Running = false; s.qualityMu.Unlock() }()
	started := time.Now()
	for offset, requests := 0, 0; offset < len(jobs) && requests < 20 && time.Since(started) < 30*time.Second; offset, requests = offset+10, requests+1 {
		if ctx.Err() != nil || !s.qualityPolicyCurrent(q, push) {
			return
		}
		s.qualityMu.Lock()
		lastRequest := s.qualityModelLastRequest
		s.qualityMu.Unlock()
		if wait := time.Until(lastRequest.Add(500 * time.Millisecond)); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		end := offset + 10
		if end > len(jobs) {
			end = len(jobs)
		}
		batch := jobs[offset:end]
		accounts := []sub2.ModelAuditAccount{}
		ids := map[int64]bool{}
		for _, p := range batch {
			if !ids[p.Sub2AccountID] {
				accounts = append(accounts, sub2.ModelAuditAccount{AccountID: p.Sub2AccountID, Since: modelAuditSince(p, q, push)})
				ids[p.Sub2AccountID] = true
			}
		}
		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		s.qualityMu.Lock()
		s.qualityModelLastRequest = time.Now()
		s.qualityMu.Unlock()
		results, queryErr := s.sub2.ModelAudit(queryCtx, push, password, q.ModelAuditModel, accounts)
		cancel()
		if ctx.Err() != nil {
			return
		}
		byAccount := map[int64][]sub2.ModelAuditLog{}
		for _, result := range results {
			byAccount[result.AccountID] = result.Logs
		}
		for _, p := range batch {
			s.applyQualityModelResult(ctx, p, q, push, password, byAccount[p.Sub2AccountID], queryErr)
			s.qualityMu.Lock()
			s.qualityModelRuntime.Done++
			s.qualityMu.Unlock()
		}
	}
}

var modelVariantSuffix = regexp.MustCompile(`(?i)(-latest|-\d{4}-\d{2}-\d{2}|-\d{8})$`)

func modelSampleStatus(log sub2.ModelAuditLog, expected string) string {
	if !strings.EqualFold(strings.TrimSpace(log.SentModel), strings.TrimSpace(expected)) || strings.TrimSpace(log.ResponseModel) == "" || log.Mismatch == nil {
		return "unknown"
	}
	if !*log.Mismatch && strings.EqualFold(strings.TrimSpace(log.SentModel), strings.TrimSpace(log.ResponseModel)) {
		return "normal"
	}
	base := func(v string) string {
		return modelVariantSuffix.ReplaceAllString(strings.ToLower(strings.TrimSpace(v)), "")
	}
	if base(log.SentModel) == base(log.ResponseModel) {
		return "variant"
	}
	return "suspect"
}
func applyModelSamples(a *model.ModelAuditState, logs []sub2.ModelAuditLog, q model.QualitySettings, since time.Time) int {
	sort.SliceStable(logs, func(i, j int) bool {
		if logs[i].CreatedAt.Equal(logs[j].CreatedAt) {
			return logs[i].ID < logs[j].ID
		}
		return logs[i].CreatedAt.Before(logs[j].CreatedAt)
	})
	seen := map[int64]bool{}
	for _, id := range a.SeenIDs {
		seen[id] = true
	}
	processed := 0
	for _, log := range logs {
		if seen[log.ID] || log.CreatedAt.Before(since) {
			continue
		}
		seen[log.ID] = true
		a.SeenIDs = append(a.SeenIDs, log.ID)
		// Late inserts older than the already judged sample must not reverse the verdict.
		if a.SampleAt != nil && (log.CreatedAt.Before(*a.SampleAt) || log.CreatedAt.Equal(*a.SampleAt) && log.ID <= a.LatestID) {
			continue
		}
		processed++
		a.LatestID, a.SampleAt, a.SentModel, a.ResponseModel = log.ID, &log.CreatedAt, log.SentModel, log.ResponseModel
		a.Status = modelSampleStatus(log, q.ModelAuditModel)
		switch a.Status {
		case "normal", "variant":
			a.Failures = 0
			a.Successes++
		case "suspect":
			a.Successes = 0
			a.Failures++
			if a.Failures >= q.FailureLimit {
				a.Degraded = true
			}
		}
	}
	if len(a.SeenIDs) > 128 {
		a.SeenIDs = append([]int64{}, a.SeenIDs[len(a.SeenIDs)-128:]...)
	}
	a.NoNewSamples = processed == 0
	if a.Status == "" {
		a.Status = "no_samples"
	}
	return processed
}
func (s *Server) applyQualityModelResult(ctx context.Context, initial model.FreeAccountProfile, q model.QualitySettings, push model.Sub2Settings, password string, logs []sub2.ModelAuditLog, queryErr error) {
	v, _ := s.freeLocks.LoadOrStore(initial.ID, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	p, _, err := s.store.FreeAccountCredential(initial.ID)
	if err != nil || !qualityEligible(p) || qualityIdentity(p, push) != qualityIdentity(initial, push) || !s.qualityPolicyCurrent(q, push) {
		return
	}
	state := p.Quality
	identity := qualityIdentity(p, push)
	if state.Revision != q.Revision || state.Identity != identity {
		state.Revision, state.Identity = q.Revision, identity
		state.Failures, state.Successes = 0, 0
		state.QuestionStatus = ""
		if p.RemoteRemovedAt == nil && !state.PlanReady && !state.Routed && (state.ActionStatus == "pending" || state.ActionStatus == "failed") {
			state.Action, state.ActionStatus = "", ""
			state.Degraded, state.Excluded = false, false
			state.QuestionDegraded = false
			state.ModelAudit.Degraded = false
		}
	}
	a := state.ModelAudit
	if a.Identity != identity {
		a = model.ModelAuditState{Degraded: a.Degraded}
	}
	if a.Revision != q.Revision {
		a.Failures, a.Successes = 0, 0
		a.Status = ""
	}
	a.Revision, a.Identity = q.Revision, identity
	since := modelAuditSince(p, q, push)
	a.Since = &since
	now := time.Now().UTC()
	next := now.Add(time.Duration(q.ModelAuditIntervalSeconds) * time.Second)
	a.CheckedAt, a.NextAt = &now, &next
	processed := 0
	if queryErr != nil {
		a.Status = "error"
		a.Error = queryErr.Error()
		a.ErrorCount++
		delay := q.ModelAuditIntervalSeconds
		for i := 1; i < a.ErrorCount && delay < 900; i++ {
			delay *= 2
		}
		if delay > 900 {
			delay = 900
		}
		next = now.Add(time.Duration(delay) * time.Second)
	} else {
		a.Error = ""
		a.ErrorCount = 0
		processed = applyModelSamples(&a, logs, q, since)
		if processed == 0 && a.Status == "error" {
			a.Status = "unknown"
		}
	}
	state.ModelAudit = a
	if a.Degraded && !state.Degraded {
		state.Degraded = true
		state.Action, state.ActionStatus = q.Action, "pending"
		if q.Action == "kick" {
			state.Excluded = true
		}
	}
	if state.Degraded && state.Routed && q.AutoRestore && qualityRecoveryReady(state, q) {
		state.Action, state.ActionStatus = "restore", "pending"
		state.RestoreManual = false
	}
	refreshQualityStatus(&state, q)
	updated, err := s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.Quality = state })
	if err != nil {
		return
	}
	s.auditAccountEvent(ctx, p.ID, "quality_model_result", "quality", "quality_monitor", "sub2", "模型一致性检查："+a.Status, map[string]any{"sub2_account_id": p.Sub2AccountID, "model": q.ModelAuditModel, "since": since, "logs": logs, "new_samples": processed, "failures": a.Failures, "failure_limit": q.FailureLimit, "error": a.Error, "action": state.Action, "action_status": state.ActionStatus})
	if state.ActionStatus == "pending" || state.ActionStatus == "failed" {
		// Existing account and mother locks also serialize actions from both detectors.
		s.applyQualityAction(ctx, updated, q, push, password)
	}
}
