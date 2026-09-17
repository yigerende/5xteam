package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

type qualityRuntime struct {
	Running bool            `json:"running"`
	Total   int             `json:"total"`
	Done    int             `json:"done"`
	Active  map[string]bool `json:"active"`
}

func qualityEligible(p model.FreeAccountProfile) bool {
	return !p.Dead && p.AcceptStatus == "completed" && p.PushStatus == "completed" && p.RemoveStatus != "completed" && p.RemoteRemovedAt == nil && p.Sub2AccountID > 0 && p.PushProvider != "cpa"
}
func qualityActionRetryEligible(p model.FreeAccountProfile) bool {
	return p.Quality.Action == "kick" && (p.Quality.ActionStatus == "pending" || p.Quality.ActionStatus == "failed") && p.RemoteRemovedAt != nil && p.RemoveStatus != "completed"
}
func qualityIdentity(p model.FreeAccountProfile, settings model.Sub2Settings) string {
	pushed := ""
	if p.PushedAt != nil {
		pushed = p.PushedAt.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%s|%d|%s|%s", strings.TrimRight(settings.URL, "/"), p.Sub2AccountID, p.CycleID, pushed)
}
func qualityDue(p model.FreeAccountProfile, q model.QualitySettings, s model.Sub2Settings, now time.Time) bool {
	if !q.QuestionEnabled && !q.ModelAuditEnabled && p.Quality.ActionStatus != "pending" && p.Quality.ActionStatus != "failed" {
		return false
	}
	return (qualityEligible(p) || qualityActionRetryEligible(p)) && (p.Quality.Revision != q.Revision || p.Quality.Identity != qualityIdentity(p, s) || p.Quality.NextAt == nil || !now.Before(*p.Quality.NextAt))
}
func (s *Server) qualityStatus() map[string]any {
	q, err := s.store.QualitySettings()
	s.qualityMu.Lock()
	runtime := s.qualityRuntime
	modelRuntime := s.qualityModelRuntime
	runtime.Active = map[string]bool{}
	for id, active := range s.qualityRuntime.Active {
		runtime.Active[id] = active
	}
	s.qualityMu.Unlock()
	status := map[string]any{"settings": q, "runtime": runtime, "model_runtime": modelRuntime, "server_now": time.Now().UTC(), "next_at": nil, "model_next_at": nil, "eligible": 0}
	if err != nil {
		status["error"] = err.Error()
		return status
	}
	summary, summaryErr := s.store.QualitySummary(q.Revision)
	if summaryErr != nil {
		status["error"] = summaryErr.Error()
		return status
	}
	status["summary"] = summary
	push, _, err := s.store.Sub2Settings()
	if err != nil || providerForSettings(push) != "sub2" {
		status["inactive_reason"] = "当前启用 CPA，降智检测仅支持 Sub2"
		return status
	}
	count, next, err := s.store.QualitySchedule(q.Revision)
	if err != nil {
		status["error"] = err.Error()
		return status
	}
	status["eligible"] = count
	if q.Enabled && (q.QuestionEnabled || q.ModelAuditEnabled) && count > 0 {
		if next.IsZero() {
			next = time.Now()
		}
		status["next_at"] = next
	}
	if q.Enabled && q.ModelAuditEnabled && count > 0 {
		modelNext, e := s.store.QualityModelSchedule(q.Revision)
		if e == nil {
			if modelNext.IsZero() {
				modelNext = time.Now()
			}
			status["model_next_at"] = modelNext
		}
	}
	return status
}
func (s *Server) getQualitySettings(w http.ResponseWriter, r *http.Request) {
	writeAPI(w, 200, s.qualityStatus(), "")
}
func (s *Server) getQualityHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, _, err := s.store.FreeAccountCredential(id)
	if err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	q, err := s.store.QualitySettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if err = s.flushAuditEvents(r.Context()); err != nil {
		writeAPI(w, 503, nil, err.Error())
		return
	}
	events, err := s.store.QualityHistory(id, q.HistoryLimit)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"account": p, "events": events, "has_more": false}, "")
}
func (s *Server) saveQualitySettings(w http.ResponseWriter, r *http.Request) {
	q := model.DefaultQualitySettings()
	q.Questions = nil
	if err := decodeJSON(w, r, &q, 1<<20); err != nil {
		return
	}
	if err := q.Validate(); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	push, password, err := s.store.Sub2Settings()
	if err == nil && q.Enabled {
		if providerForSettings(push) != "sub2" {
			err = errors.New("降智检测目前只支持 Sub2，请先启用 Sub2")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err == nil {
			err = s.sub2.QualityCapabilities(ctx, push, password)
		}
		if err == nil && q.Action == "groups" {
			var groups []sub2.Group
			groups, err = s.sub2.Groups(ctx, push, password)
			if err == nil {
				valid := map[int64]bool{}
				for _, g := range groups {
					valid[g.ID] = true
				}
				for _, id := range append(append([]int64{}, q.NormalGroupIDs...), q.DegradedGroupIDs...) {
					if !valid[id] {
						err = fmt.Errorf("分组 %d 不存在或不是 OpenAI 分组", id)
						break
					}
				}
			}
		}
	}
	if err == nil {
		q, err = s.store.SaveQualitySettings(q)
	}
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, s.qualityStatus(), "")
}
func (s *Server) triggerQuality(w http.ResponseWriter, r *http.Request) {
	q, err := s.store.QualitySettings()
	if err != nil || !q.Enabled {
		writeAPI(w, 400, nil, "请先开启并保存降智检测")
		return
	}
	push, _, err := s.store.Sub2Settings()
	if err != nil || providerForSettings(push) != "sub2" {
		writeAPI(w, 409, nil, "当前未启用 Sub2")
		return
	}
	accountID := r.PathValue("id")
	if accountID != "" {
		p, _, err := s.store.FreeAccountCredential(accountID)
		if err != nil || (!qualityEligible(p) && !qualityActionRetryEligible(p)) {
			writeAPI(w, 409, nil, "账号必须在空间内且已推送到 Sub2")
			return
		}
	}
	if !s.qualityRunMu.TryLock() {
		writeAPI(w, 409, nil, "降智检测正在执行，本轮结束后才能继续")
		return
	}
	s.qualityMu.Lock()
	if s.qualityClosed {
		s.qualityMu.Unlock()
		s.qualityRunMu.Unlock()
		writeAPI(w, 503, nil, "服务正在关闭")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	s.qualityCancel = cancel
	s.qualityWG.Add(1)
	s.qualityMu.Unlock()
	go func() {
		defer s.qualityRunMu.Unlock()
		defer s.qualityWG.Done()
		defer cancel()
		s.runRemoteQualityBatch(ctx, true, accountID)
	}()
	writeAPI(w, 202, map[string]any{"started": true}, "")
}
func (s *Server) monitorQuality(ctx context.Context) {
	s.qualityMu.Lock()
	if s.qualityClosed {
		s.qualityMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.qualityMonitorCancel = cancel
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
			if s.qualityRunMu.TryLock() {
				s.runRemoteQualityBatch(ctx, false, "")
				s.qualityRunMu.Unlock()
			}
		}
	}
}
func (s *Server) runQualityBatch(ctx context.Context, force bool, onlyID ...string) {
	q, err := s.store.QualitySettings()
	if err != nil || !q.Enabled {
		return
	}
	push, password, err := s.store.Sub2Settings()
	if err != nil || providerForSettings(push) != "sub2" {
		return
	}
	jobs := []model.FreeAccountProfile{}
	accounts, err := s.store.QualityAccounts()
	if err != nil {
		return
	}
	for _, p := range accounts {
		if len(onlyID) > 0 && onlyID[0] != "" && p.ID != onlyID[0] {
			continue
		}
		if (qualityEligible(p) || qualityActionRetryEligible(p)) && ((force && q.QuestionEnabled) || qualityDue(p, q, push, time.Now())) {
			jobs = append(jobs, p)
		}
	}
	if len(jobs) == 0 {
		return
	}
	// Oldest due first; a slow or busy account cannot starve the rest.
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i].Quality.CheckedAt, jobs[j].Quality.CheckedAt
		if a == nil {
			return b != nil
		}
		return b != nil && a.Before(*b)
	})
	s.qualityMu.Lock()
	s.qualityRuntime = qualityRuntime{Running: true, Total: len(jobs), Active: map[string]bool{}}
	s.qualityMu.Unlock()
	defer func() { s.qualityMu.Lock(); s.qualityRuntime.Running = false; s.qualityMu.Unlock() }()
	queue := make(chan model.FreeAccountProfile)
	var wg sync.WaitGroup
	for i := 0; i < q.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range queue {
				if ctx.Err() == nil {
					s.probeQualityAccount(ctx, p, q, push, password, force)
				}
				s.qualityMu.Lock()
				s.qualityRuntime.Done++
				s.qualityMu.Unlock()
			}
		}()
	}
	for _, p := range jobs {
		select {
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return
		case queue <- p:
		}
	}
	close(queue)
	wg.Wait()
}
func (s *Server) qualityPolicyCurrent(q model.QualitySettings, push model.Sub2Settings) bool {
	current, err := s.store.QualitySettings()
	if err != nil || !current.Enabled || current.Revision != q.Revision {
		return false
	}
	active, _, err := s.store.Sub2Settings()
	return err == nil && providerForSettings(active) == "sub2" && active.URL == push.URL && active.Email == push.Email
}
func (s *Server) probeQualityAccount(ctx context.Context, initial model.FreeAccountProfile, q model.QualitySettings, push model.Sub2Settings, password string, force bool) {
	// Skip, rather than queue behind, an existing OAuth/quota/account operation.
	value, _ := s.freeLocks.LoadOrStore(initial.ID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	if !mutex.TryLock() {
		return
	}
	defer mutex.Unlock()
	p, _, err := s.store.FreeAccountCredential(initial.ID)
	if err != nil || p.CycleID != initial.CycleID || (!qualityEligible(p) && !qualityActionRetryEligible(p)) || !s.qualityPolicyCurrent(q, push) {
		return
	}
	if !force && !qualityDue(p, q, push, time.Now()) {
		return
	}
	s.qualityMu.Lock()
	if s.qualityRuntime.Active == nil {
		s.qualityRuntime.Active = map[string]bool{}
	}
	s.qualityRuntime.Active[p.ID] = true
	s.qualityMu.Unlock()
	defer func() { s.qualityMu.Lock(); delete(s.qualityRuntime.Active, p.ID); s.qualityMu.Unlock() }()
	identity := qualityIdentity(p, push)
	state := p.Quality
	if state.Revision != q.Revision || state.Identity != identity {
		state.Failures, state.Successes = 0, 0
		state.QuestionStatus = ""
		state.Revision, state.Identity = q.Revision, identity
		// An unexecuted verdict under an old rule must not kick an account under
		// the new rule. A saved remote group plan is retained for reconciliation.
		if p.RemoteRemovedAt == nil && !state.PlanReady && !state.Routed && (state.ActionStatus == "pending" || state.ActionStatus == "failed") {
			state.Action, state.ActionStatus = "", ""
			state.Degraded, state.Excluded = false, false
			state.QuestionDegraded = false
			state.ModelAudit.Degraded = false
		}
	}
	// A confirmed action with an ambiguous/failed response is retried on its
	// own schedule, without consuming another answer or losing the group plan.
	if state.ActionStatus == "pending" || state.ActionStatus == "failed" {
		p, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.Quality = state })
		if err == nil {
			s.applyQualityAction(ctx, p, q, push, password)
		}
		return
	}
	if !q.QuestionEnabled {
		return
	}
	questions := q.ActiveQuestions()
	if len(questions) == 0 {
		return
	}
	index := 0
	for i, question := range questions {
		if question.ID == state.NextQuestionID {
			index = i
			break
		}
	}
	question := questions[index]
	probeSettings := q
	probeSettings.Prompt, probeSettings.Answer, probeSettings.MatchMode, probeSettings.MaxDurationMS = question.Prompt, question.Answer, question.MatchMode, question.MaxDurationMS
	state.QuestionID, state.QuestionName = question.ID, question.Name
	state.NextQuestionID = question.ID
	ctxProbe, cancel := context.WithTimeout(ctx, time.Duration(q.TimeoutSeconds+15)*time.Second)
	s.auditAccountEvent(ctx, p.ID, "quality_probe", "quality", "quality_monitor", "sub2", "开始指定子号降智检测", map[string]any{"question_id": question.ID, "question_name": question.Name, "model": q.Model, "reasoning_effort": q.ReasoningEffort, "prompt": question.Prompt, "mode": q.Mode, "answer_rule": question.Answer, "match_mode": question.MatchMode, "max_duration_ms": question.MaxDurationMS, "sub2_account_id": p.Sub2AccountID})
	result, probeErr := s.sub2.ProbeQuality(ctxProbe, push, password, p.Sub2AccountID, probeSettings)
	cancel()
	if ctx.Err() != nil {
		return
	}
	current, _, readErr := s.store.FreeAccountCredential(p.ID)
	if readErr != nil || !qualityEligible(current) || qualityIdentity(current, push) != identity || !s.qualityPolicyCurrent(q, push) {
		s.auditAccountEvent(ctx, p.ID, "quality_stale", "quality", "quality_monitor", "sub2", "账号轮次、状态或配置已改变，忽略旧探测结果", nil)
		return
	}
	now := time.Now()
	next := now.Add(time.Duration(q.IntervalSeconds) * time.Second)
	state.CheckedAt, state.NextAt, state.DurationMS = &now, &next, result.DurationMS
	state.Answer, state.Error, state.Reason = result.Text, "", ""
	state.ContentPassed, state.TimePassed = nil, nil
	if len([]rune(state.Answer)) > 2000 {
		state.Answer = string([]rune(state.Answer)[:2000]) + "..."
	}
	if probeErr != nil || !result.Complete || result.HTTPStatus != 200 {
		state.QuestionStatus = "error"
		state.Status = "error"
		state.Error = result.Error
		if probeErr != nil {
			state.Error = probeErr.Error()
		}
		if state.Error == "" {
			state.Error = "探测未返回完整有效回答"
		}
		next = now.Add(time.Duration(q.RetrySeconds) * time.Second)
		// Transport failures neither increment nor reset the valid-answer streak.
	} else {
		state.NextQuestionID = questions[(index+1)%len(questions)].ID
		passed, reason := judgeQuality(probeSettings, result.Text, result.DurationMS)
		content, timely := qualityAnswerChecks(probeSettings, result.Text, result.DurationMS)
		state.ContentPassed, state.TimePassed = &content, &timely
		state.Reason = reason
		if passed {
			state.QuestionStatus = "normal"
			state.Failures = 0
			state.Successes++
			state.Status = "normal"
			if state.Degraded {
				state.Status = "degraded"
				if state.Routed && q.AutoRestore && qualityRecoveryReady(state, q) {
					state.Action, state.ActionStatus = "restore", "pending"
					state.RestoreManual = false
				}
			}
		} else {
			state.QuestionStatus = "suspect"
			state.Successes = 0
			state.Failures++
			if state.Failures >= q.FailureLimit {
				state.QuestionDegraded = true
				state.QuestionStatus = "degraded"
			}
			state.Status = "suspect"
			next = now.Add(time.Duration(q.RetrySeconds) * time.Second)
			if state.Degraded {
				state.Status = "degraded"
			} else if state.Failures >= q.FailureLimit {
				state.Degraded = true
				state.Status = "degraded"
				state.Action = q.Action
				state.ActionStatus = "pending"
				if q.Action == "kick" {
					state.Excluded = true
				}
			}
		}
	}
	refreshQualityStatus(&state, q)
	updated, saveErr := s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.Quality = state })
	if saveErr != nil {
		return
	}
	s.auditAccountEvent(ctx, p.ID, "quality_result", "quality", "quality_monitor", "sub2", "降智检测完成", map[string]any{"response": result, "error": state.Error, "status": state.Status, "reason": state.Reason, "failures": state.Failures, "failure_limit": q.FailureLimit, "action": state.Action, "action_status": state.ActionStatus})
	if probeErr == nil && result.HTTPStatus == 401 {
		// Do not turn upstream auth failures into a quality verdict. Reuse the
		// existing status/reauth path, including its configured retry/removal limit.
		if push.Enable401Check {
			if s.reloginFailureLimit("sub2") == 0 {
				_, _ = s.removeFreeAccountWithoutRelogin(ctx, p.ID, "sub2")
			} else if loginErr := s.reloginAndRepush(ctx, p.ID); loginErr != nil && !errors.Is(loginErr, errDeadAccountHandled) {
				_, _ = s.record401ReloginFailure(ctx, p.ID, "sub2", loginErr)
			}
		}
		return
	}
	if state.ActionStatus == "pending" {
		s.applyQualityAction(ctx, updated, q, push, password)
	}
}

var qualityFinalAnswer = regexp.MustCompile(`(?m)^\s*FINAL_ANSWER\s*=\s*([^\r\n]+)\s*$`)

func judgeQuality(q model.QualitySettings, text string, duration int64) (bool, string) {
	content, timely := qualityAnswerChecks(q, text, duration)
	passed := content
	if q.Mode == "time" {
		passed = timely
	} else if q.Mode == "content_time" {
		passed = content && timely
	}
	return passed, fmt.Sprintf("内容匹配=%t，耗时=%dms，阈值=<%dms，模式=%s", content, duration, q.MaxDurationMS, q.Mode)
}
func qualityAnswerChecks(q model.QualitySettings, text string, duration int64) (bool, bool) {
	content := false
	switch q.MatchMode {
	case "answer":
		matches := qualityFinalAnswer.FindAllStringSubmatch(strings.TrimSpace(text), -1)
		content = (len(matches) == 1 && strings.TrimSpace(matches[0][1]) == strings.TrimSpace(q.Answer)) || (len(matches) == 0 && strings.TrimSpace(text) == strings.TrimSpace(q.Answer))
	case "keyword":
		content = strings.Contains(text, q.Answer)
	case "regex":
		re, err := regexp.Compile(q.Answer)
		content = err == nil && re.MatchString(text)
	}
	return content, duration >= 0 && duration < q.MaxDurationMS
}
func transformQualityGroups(current, remove, add []int64) []int64 {
	values := map[int64]bool{}
	for _, id := range current {
		if id > 0 {
			values[id] = true
		}
	}
	for _, id := range remove {
		delete(values, id)
	}
	for _, id := range add {
		if id > 0 {
			values[id] = true
		}
	}
	out := []int64{}
	for id := range values {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func intersectQualityGroups(a, b []int64) []int64 {
	set := map[int64]bool{}
	for _, id := range b {
		set[id] = true
	}
	out := []int64{}
	for _, id := range a {
		if set[id] {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) applyQualityAction(ctx context.Context, p model.FreeAccountProfile, q model.QualitySettings, push model.Sub2Settings, password string) {
	if !s.qualityPolicyCurrent(q, push) {
		return
	}
	current, _, err := s.store.FreeAccountCredential(p.ID)
	if err != nil || current.CycleID != p.CycleID || (!qualityEligible(current) && !qualityActionRetryEligible(current)) || current.Sub2AccountID != p.Sub2AccountID {
		return
	}
	state := current.Quality
	action := state.Action
	if action == "restore" && !state.RestoreManual && (!q.AutoRestore || !qualityRecoveryReady(state, q)) {
		// New abnormal samples can arrive while a failed restoration awaits retry.
		_, _ = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) {
			item.Quality.Action, item.Quality.ActionStatus = "groups", "completed"
			item.Quality.Error = ""
		})
		return
	}
	if action == "kick" {
		_, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) {
			item.RemovalReason = "连续未通过降智检测，由母号强制踢出"
			item.Quality.Excluded = true
		})
		if err == nil {
			_, err = s.performFreeAccountRemoval(ctx, p.ID, true)
		}
	} else if action == "groups" || action == "restore" {
		// Emergency manual removal does not take the OAuth/account lock.
		// Serialize group writes with that removal as well.
		unlockRemove := s.lockFreeAccountRemove(p.ID)
		defer unlockRemove()
		latest, _, readErr := s.store.FreeAccountCredential(p.ID)
		if readErr != nil || !qualityEligible(latest) || latest.CycleID != p.CycleID {
			return
		}
		var groups []int64
		if state.RouteURL != "" && (state.RouteURL != push.URL || state.RouteAccountID != p.Sub2AccountID) {
			err = errors.New("Sub2 地址或账号已改变，不能将原分组恢复计划应用到新账号；请恢复原推送配置后重试")
		} else {
			groups, err = s.sub2.AccountGroups(ctx, push, password, p.Sub2AccountID)
		}
		if err == nil {
			if action == "groups" {
				if !state.PlanReady {
					state.RemovedGroups = intersectQualityGroups(groups, q.NormalGroupIDs)
					state.AddedGroups = transformQualityGroups(q.DegradedGroupIDs, groups, nil)
					state.AssignGroups = append([]int64{}, q.DegradedGroupIDs...)
					state.PlanReady = true
					state.RouteURL = push.URL
					state.RouteAccountID = p.Sub2AccountID
				}
				state.TargetGroups = transformQualityGroups(groups, state.RemovedGroups, state.AssignGroups)
			} else {
				state.TargetGroups = transformQualityGroups(groups, state.AddedGroups, state.RemovedGroups)
			}
			// Save before writing remotely. A restart must retain the restore baseline.
			_, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.Quality = state })
			if err == nil {
				if !s.qualityPolicyCurrent(q, push) {
					return
				}
				err = s.sub2.SetAccountGroups(ctx, push, password, p.Sub2AccountID, state.TargetGroups)
			}
			if err == nil {
				_, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) {
					item.Sub2GroupIDs = append([]int64{}, state.TargetGroups...)
					item.Sub2GroupNames = nil
					item.Sub2GroupID = 0
					item.Sub2GroupName = ""
					if len(item.Sub2GroupIDs) > 0 {
						item.Sub2GroupID = item.Sub2GroupIDs[0]
					}
				})
			}
		}
	} else {
		err = errors.New("未知降智处理动作")
	}
	now := time.Now()
	next := now.Add(time.Duration(q.IntervalSeconds) * time.Second)
	state.NextAt = &next
	if err != nil {
		state.ActionStatus = "failed"
		state.Error = err.Error()
		next = now.Add(time.Duration(q.RetrySeconds) * time.Second)
	} else {
		state.ActionStatus = "completed"
		state.Error = ""
		if action == "groups" {
			state.Routed = true
		}
		if action == "restore" {
			state.RestoreManual = false
			state.QuestionDegraded = false
			state.ModelAudit.Degraded = false
			state.ModelAudit.Failures, state.ModelAudit.Successes = 0, 0
			state.Routed = false
			state.Degraded = false
			state.Excluded = false
			state.Status = "normal"
			state.Failures = 0
			state.Successes = 0
			state.TargetGroups = nil
			state.RemovedGroups = nil
			state.AddedGroups = nil
			state.AssignGroups = nil
			state.PlanReady = false
			state.RouteURL = ""
			state.RouteAccountID = 0
		}
	}
	_, _ = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.Quality = state })
	s.auditAccountEvent(ctx, p.ID, "quality_action", "quality", "quality_monitor", "sub2", "降智处理："+action+" / "+state.ActionStatus, map[string]any{"action": action, "error": state.Error, "removed_groups": state.RemovedGroups, "added_groups": state.AddedGroups, "target_groups": state.TargetGroups, "forced_mother_kick": action == "kick"})
}

// Manual restore uses the same verified group mutation path; it cannot recover
// a dead account or fabricate membership after removal.
func (s *Server) restoreQualityAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	unlock := s.lockFreeAccount(id)
	defer unlock()
	p, _, err := s.store.FreeAccountCredential(id)
	if err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	if p.Dead {
		writeAPI(w, 409, nil, "死号不能通过降智恢复解除")
		return
	}
	if p.RemoteRemovedAt != nil && !p.DownstreamCleaned {
		writeAPI(w, 409, nil, "账号已移出但下游清理未完成，请先完成清理再恢复")
		return
	}
	q, err := s.store.QualitySettings()
	push, password, pushErr := s.store.Sub2Settings()
	if err != nil || pushErr != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	if (p.Quality.Routed || p.Quality.PlanReady) && qualityEligible(p) {
		if !q.Enabled || providerForSettings(push) != "sub2" {
			writeAPI(w, 409, nil, "请先启用 Sub2 和降智检测再恢复分组")
			return
		}
		p, err = s.store.UpdateFreeAccountCycle(id, p.CycleID, func(item *model.FreeAccountProfile) {
			item.Quality.Action = "restore"
			item.Quality.ActionStatus = "pending"
			item.Quality.RestoreManual = true
		})
		if err == nil {
			s.applyQualityAction(r.Context(), p, q, push, password)
			p, _, err = s.store.FreeAccountCredential(id)
		}
		if err == nil && p.Quality.ActionStatus != "completed" {
			err = errors.New(p.Quality.Error)
		}
	} else {
		now := time.Now().UTC()
		p, err = s.store.UpdateFreeAccountCycle(id, p.CycleID, func(item *model.FreeAccountProfile) {
			item.Quality = model.AccountQuality{Status: "unknown", ModelAudit: model.ModelAuditState{Identity: qualityIdentity(p, push), Since: &now}}
		})
	}
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	s.auditAccountEvent(r.Context(), id, "quality_restore", "quality", "manual_single", "sub2", "已人工恢复降智状态／解除停用", nil)
	writeAPI(w, 200, p, "")
}
