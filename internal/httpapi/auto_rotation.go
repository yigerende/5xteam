package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

type autoRotationTraceContextKey struct{}
type autoRotationTraceContext struct {
	RunID  string
	TaskID string
}

func (s *Server) getAutoRotationSettings(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, 200, s.store.AutoRotationSettings(), "")
}
func (s *Server) saveAutoRotationSettings(w http.ResponseWriter, r *http.Request) {
	var input model.AutoRotationSettings
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	saved, err := s.store.SaveAutoRotationSettings(input)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, saved, "")
}
func (s *Server) listAutoRotationRuns(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.AutoRotationRuns(), "")
		return
	}
	page := parsePagination(r)
	items, total, err := s.store.AutoRotationRunsPage(page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取自动轮转批次失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, nil), "")
}
func (s *Server) listAutoRotationTasks(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.AutoRotationTasks(r.PathValue("id")), "")
		return
	}
	page := parsePagination(r)
	items, total, err := s.store.AutoRotationTasksPage(r.PathValue("id"), page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取自动轮转任务失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, nil), "")
}
func (s *Server) listAutoRotationEvents(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, redactExecutionEvents(s.store.AutoRotationEvents(r.URL.Query().Get("run_id"), r.URL.Query().Get("task_id"))), "")
		return
	}
	page := parsePagination(r)
	items, total, err := s.store.AutoRotationEventsPage(r.URL.Query().Get("run_id"), r.URL.Query().Get("task_id"), r.URL.Query().Get("account_id"), r.URL.Query().Get("type"), r.URL.Query().Get("query"), page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取执行事件失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(redactExecutionEvents(items), total, page, nil), "")
}

type executionLogExport struct {
	ExportedAt    time.Time                 `json:"exported_at"`
	Retention     string                    `json:"retention"`
	Account       *model.FreeAccountProfile `json:"account,omitempty"`
	LifecycleTask *model.AutoRotationTask   `json:"lifecycle_task,omitempty"`
	Events        []model.AutoRotationEvent `json:"events"`
}

var logFilePartPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func writeExecutionLogExport(w http.ResponseWriter, filename string, payload executionLogExport) {
	filename = strings.Trim(logFilePartPattern.ReplaceAllString(filename, "-"), "-.")
	if filename == "" {
		filename = "execution-logs"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

func redactExecutionEvents(events []model.AutoRotationEvent) []model.AutoRotationEvent {
	result := make([]model.AutoRotationEvent, len(events))
	for i, event := range events {
		event.Request = redactMap(event.Request)
		event.Response = redactMap(event.Response)
		event.Details = redactMap(event.Details)
		result[i] = event
	}
	return result
}

func (s *Server) exportAutoRotationEvents(w http.ResponseWriter, r *http.Request) {
	events := redactExecutionEvents(s.store.AutoRotationEvents(r.URL.Query().Get("run_id"), r.URL.Query().Get("task_id")))
	writeExecutionLogExport(w, "team-execution-logs-"+beijingNow().Format("20060102-150405"), executionLogExport{
		ExportedAt: time.Now(), Retention: "48h", Events: events,
	})
}
func (s *Server) triggerAutoRotation(w http.ResponseWriter, r *http.Request) {
	run, started, err := s.startAutoRotation(r.Context(), "manual")
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if !started {
		writeAPI(w, 409, run, "已有自动轮转批次正在执行")
		return
	}
	writeAPI(w, http.StatusAccepted, run, "")
}

func (s *Server) autoRotationLoop(ctx context.Context) {
	for {
		settings := s.store.AutoRotationSettings()
		wait := time.Duration(settings.IntervalSeconds) * time.Second
		if wait < 10*time.Second {
			wait = 10 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !s.store.AutoRotationSettings().Enabled {
			continue
		}
		_, _, _ = s.startAutoRotation(ctx, "automatic")
	}
}

func (s *Server) startAutoRotation(ctx context.Context, trigger string) (model.AutoRotationRun, bool, error) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.autoRunning {
		return model.AutoRotationRun{Status: "running"}, false, nil
	}
	// A running batch is represented by the process-wide lock. This also
	// prevents a manual click from racing the scheduler.
	accounts := s.store.FreeAccounts()
	settings := s.store.AutoRotationSettings()
	// Trigger decisions use the capacity snapshot written by the Team account
	// management page. This keeps scheduler ticks local and avoids repeatedly
	// calling the upstream seat endpoint. A manual refresh in that page is the
	// explicit way to update the snapshot after seats/mother accounts change.
	admins := s.store.AdminAccounts()
	snapshots := s.store.AdminCapacitySnapshots()
	seatTotal, insidePremium, snapshotCount := premiumSeatSnapshot(admins, snapshots, accounts)
	// No durable seat reservation is created by automatic rotation. In-flight
	// queued/running tasks are counted from their account/task state so a batch
	// still cannot plan beyond the capacity already committed to invitations.
	reserved := pendingAutoInviteCount(s.store.AutoRotationTasks(""), accounts)
	seatRemaining := seatTotal - insidePremium
	if seatRemaining < 0 {
		seatRemaining = 0
	}
	avg := averageFreeQuota(accounts, seatTotal)
	spaceCount, quotaCount := 0, 0
	for _, a := range accounts {
		if a.AcceptStatus == "completed" && a.RemoveStatus != "completed" {
			spaceCount++
			if a.Quota7D != nil {
				quotaCount++
			}
		}
	}
	run := model.AutoRotationRun{ID: randomRegistrationID(), Trigger: trigger, AveragePercent: avg, SeatTotal: seatTotal, SeatRemaining: seatRemaining, ReservedSeats: reserved, SpaceAccountCount: spaceCount, QuotaAccountCount: quotaCount, StartedAt: time.Now(), Status: "skipped", Reason: "当前平均剩余额度未低于阈值", DecisionMaxPerRun: settings.MaxPerRun}
	if snapshotCount == 0 {
		run.Reason = "没有可用的席位统计快照，请先在 Team 账号管理页面刷新 5x 席位"
		_ = s.store.SaveAutoRotationRun(run)
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "seat_snapshot", Source: "auto_rotation", Operation: "decision", Stage: "seat_snapshot", Message: run.Reason, Details: map[string]any{"seat_total": seatTotal, "seat_remaining": seatRemaining, "inside_premium": insidePremium, "reserved": reserved, "snapshot_count": snapshotCount}})
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "final", Source: "auto_rotation", Operation: "run", Stage: "decision", Message: "自动轮转未执行", Details: map[string]any{"status": run.Status, "reason": run.Reason}})
		return run, true, nil
	}
	availableSeats := availablePremiumFromSnapshot(seatTotal, insidePremium, reserved)
	shouldRun, decisionReason := autoRotationDecision(avg, settings.ThresholdPercent, availableSeats)
	if !shouldRun {
		run.Reason = decisionReason
		_ = s.store.SaveAutoRotationRun(run)
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "seat_snapshot", Source: "auto_rotation", Operation: "decision", Stage: "seat_snapshot", Message: run.Reason, Details: map[string]any{"average_percent": avg, "threshold_percent": settings.ThresholdPercent, "seat_total": seatTotal, "seat_remaining": seatRemaining, "inside_premium": insidePremium, "reserved": reserved, "available": availableSeats}})
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "final", Source: "auto_rotation", Operation: "run", Stage: "decision", Message: "自动轮转未执行", Details: map[string]any{"status": run.Status, "reason": run.Reason}})
		return run, true, nil
	}
	run.Status, run.Reason = "running", fmt.Sprintf("7天平均剩余额度 %.1f%% ≤ 阈值 %.1f%%", avg, settings.ThresholdPercent)
	_ = s.store.SaveAutoRotationRun(run)
	s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "seat_snapshot", Source: "auto_rotation", Operation: "decision", Stage: "seat_snapshot", Message: run.Reason, Details: map[string]any{"average_percent": avg, "threshold_percent": settings.ThresholdPercent, "seat_total": seatTotal, "seat_remaining": seatRemaining, "inside_premium": insidePremium, "reserved": reserved, "available": availableSeats}})
	s.autoRunning = true
	runCtx := ctx
	// A HTTP request context is cancelled as soon as the manual trigger
	// response is written. Detach the long-running batch from that request;
	// scheduled batches already receive the process lifetime context.
	if trigger == "manual" {
		runCtx = context.Background()
	}
	go s.executeAutoRotation(runCtx, run, settings)
	return run, true, nil
}

func isPremiumSeatType(seatType string) bool {
	switch strings.ToLower(strings.TrimSpace(seatType)) {
	case "prolite", "premium", "5x":
		return true
	default:
		return false
	}
}

func premiumSeatSnapshot(admins []model.AdminAccountProfile, snapshots map[string]model.AdminSeatCapacity, accounts []model.FreeAccountProfile) (total, inside, snapshotsFound int) {
	for _, admin := range admins {
		if capacity, ok := snapshots[admin.ID]; ok {
			total += capacity.Premium.Total
			snapshotsFound++
		}
	}
	for _, account := range accounts {
		if account.AcceptStatus == "completed" && account.RemoveStatus != "completed" && isPremiumSeatType(account.SeatType) {
			inside++
		}
	}
	return total, inside, snapshotsFound
}

func availablePremiumFromSnapshot(total, inside, reserved int) int {
	available := total - inside - reserved
	if available < 0 {
		return 0
	}
	return available
}

func averageFreeQuota(accounts []model.FreeAccountProfile, seatTotal int) float64 {
	if seatTotal < 1 {
		return -1
	}
	var total float64
	count := 0
	for _, a := range accounts {
		if a.AcceptStatus == "completed" && a.RemoveStatus != "completed" && a.Quota7D != nil {
			total += 100 - a.Quota7D.UsedPercent
			count++
		}
	}
	if count == 0 {
		return -1
	}
	return total / float64(seatTotal)
}

func autoRotationDecision(avg, threshold float64, availableSeats int) (bool, string) {
	if avg < 0 {
		return false, "暂无可用额度数据"
	}
	if avg > threshold {
		return false, "当前平均剩余额度未低于阈值"
	}
	if availableSeats <= 0 {
		return false, "已达到额度阈值，但当前没有可用的 5x 剩余席位"
	}
	return true, fmt.Sprintf("7天平均剩余额度 %.1f%% ≤ 阈值 %.1f%%", avg, threshold)
}

func (s *Server) capacityForAdmin(ctx context.Context, admin model.AdminAccountProfile, token string) (model.AdminSeatCapacity, error) {
	settings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return model.AdminSeatCapacity{}, err
	}
	client, err := workflow.NewClient(settings)
	if err != nil {
		return model.AdminSeatCapacity{}, err
	}
	return client.TeamSeatCapacity(ctx, token, admin.TeamAccountID)
}

func (s *Server) executeAutoRotation(ctx context.Context, run model.AutoRotationRun, settings model.AutoRotationSettings) {
	defer func() { s.autoMu.Lock(); s.autoRunning = false; s.autoMu.Unlock() }()
	admins := s.store.AdminAccounts()
	candidates := s.store.FreeAccounts()
	proEmails := make(map[string]struct{})
	for _, account := range s.store.MailAccountsByManagementScope("pro") {
		proEmails[strings.ToLower(strings.TrimSpace(account.Email))] = struct{}{}
	}
	// Prefer accounts already in the rotation list but not yet invited.
	selected := make([]model.FreeAccountProfile, 0)
	for _, a := range candidates {
		if _, managedByPro := proEmails[strings.ToLower(strings.TrimSpace(a.Email))]; managedByPro {
			continue
		}
		// Records already placed into the Team rotation list are preferred.
		// Pure mailbox imports are only considered in the fallback pass below.
		if eligibleAutoRotationAccount(a) {
			selected = append(selected, a)
		}
	}
	run.CandidateRotationCount = len(selected)
	need := s.availablePremiumSlots(ctx, admins, run.ID)
	run.DecisionAvailableSeats = need
	s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "seat_snapshot", Source: "auto_rotation", Operation: "plan", Stage: "seat_snapshot", Message: "自动补充前实时席位快照", Details: map[string]any{"available_after_reservation": need, "max_per_run": settings.MaxPerRun}})
	need = autoRotationPlan(settings.MaxPerRun, need, 0, len(candidates))
	for len(selected) < need {
		for _, mail := range s.store.MailAccountsByManagementScope("mail") {
			if len(selected) >= need {
				break
			}
			if !eligibleAutoRotationMail(mail) {
				continue
			}
			found := false
			var pureCandidate *model.FreeAccountProfile
			for _, a := range candidates {
				if strings.EqualFold(a.Email, mail.Email) {
					found = true
					if a.ImportMode == "pure" && !a.Dead {
						copy := a
						pureCandidate = &copy
					}
					break
				}
			}
			if pureCandidate != nil {
				selected = append(selected, *pureCandidate)
				continue
			}
			if found {
				continue
			}
			profile, err := s.importMailAccountToTeam(ctx, mail.Email)
			if err == nil {
				selected = append(selected, profile)
			}
		}
		break
	}
	run.CandidateMailCount = len(selected) - run.CandidateRotationCount
	if len(selected) > need {
		selected = selected[:need]
	}
	if len(selected) == 0 {
		run.Status, run.Reason = "completed", "没有符合条件的候选账号"
		now := time.Now()
		run.CompletedAt = &now
		_ = s.store.UpdateAutoRotationRun(run)
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "final", Source: "auto_rotation", Operation: "run", Stage: "complete", Message: run.Reason, Details: map[string]any{"status": run.Status, "planned": run.Planned, "succeeded": run.Succeeded, "failed": run.Failed}})
		return
	}
	run.Planned = len(selected)
	run.CandidateRotationCount = 0
	for _, a := range selected {
		if a.ImportMode != "pure" {
			run.CandidateRotationCount++
		}
	}
	run.CandidateMailCount = len(selected) - run.CandidateRotationCount
	run.DecisionAvailableSeats = need
	_ = s.store.UpdateAutoRotationRun(run)
	sem := make(chan struct{}, maxInt(1, settings.Concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	actualTasks := 0
	for _, account := range selected {
		account := account
		taskID := randomRegistrationID()
		claimed, err := s.store.ClaimAutoRotationAccount(account.ID, taskID)
		if err != nil || !claimed {
			continue
		}
		adminID, err := s.selectPremiumAdmin(ctx, admins, account.ID, run.ID)
		if err != nil {
			_ = s.store.ReleaseAutoRotationClaim(account.ID)
			continue
		}
		source := "rotation"
		if account.ImportMode == "pure" {
			source = "mail"
		}
		task := model.AutoRotationTask{ID: taskID, RunID: run.ID, AccountID: account.ID, Email: account.Email, Source: source, AdminAccountID: adminID, SeatType: "prolite", Status: "queued", SeatReserved: false, StartedAt: time.Now(), Steps: autoSteps()}
		if err := s.store.SaveAutoRotationTask(task); err != nil {
			_ = s.store.ReleaseAutoRotationClaim(account.ID)
			continue
		}
		actualTasks++
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.executeAutoTask(ctx, task, &run, &mu, settings)
		}()
	}
	run.Planned = actualTasks
	_ = s.store.UpdateAutoRotationRun(run)
	wg.Wait()
	now := time.Now()
	run.CompletedAt = &now
	if run.Failed > 0 && run.Succeeded > 0 {
		run.Status = "partial"
	} else if run.Failed > 0 {
		run.Status = "failed"
	} else {
		run.Status = "completed"
	}
	_ = s.store.UpdateAutoRotationRun(run)
	s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "final", Source: "auto_rotation", Operation: "run", Stage: "complete", Message: "自动轮转批次完成", Details: map[string]any{"status": run.Status, "planned": run.Planned, "succeeded": run.Succeeded, "failed": run.Failed, "candidate_rotation": run.CandidateRotationCount, "candidate_mail": run.CandidateMailCount}})
}

func eligibleAutoRotationAccount(account model.FreeAccountProfile) bool {
	return !account.Dead && account.ImportMode != "pure" && account.AcceptStatus != "completed" && account.InviteStatus != "completed" && account.RemoveStatus != "completed"
}

func eligibleAutoRotationMail(account model.MailAccountProfile) bool {
	return !strings.EqualFold(account.ChatGPTStatus, "dead") && !strings.EqualFold(account.RegistrationStatus, "dead")
}

func autoSteps() []model.AutoRotationStep {
	return []model.AutoRotationStep{{Key: "invite", Name: "邀请并进入空间", Status: "pending"}, {Key: "oauth", Name: "获取 Codex OAuth", Status: "pending"}, {Key: "push", Name: "推送当前下游", Status: "pending"}, {Key: "quota", Name: "查询额度", Status: "pending"}}
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func autoRotationPlan(maxPerRun, remoteRemaining, reserved, candidates int) int {
	available := remoteRemaining - reserved
	if available < 0 {
		available = 0
	}
	planned := available
	if maxPerRun > 0 && planned > maxPerRun {
		planned = maxPerRun
	}
	if planned > candidates {
		planned = candidates
	}
	return planned
}
func (s *Server) availablePremiumSlots(ctx context.Context, admins []model.AdminAccountProfile, runID ...string) int {
	total := 0
	accounts := s.store.FreeAccounts()
	tasks := s.store.AutoRotationTasks("")
	for _, a := range admins {
		_, c, e := s.currentAdminCredential(ctx, a.ID)
		if e != nil {
			continue
		}
		cap, e := s.capacityForAdmin(ctx, a, c.AccessToken)
		if e == nil {
			inside, inFlight := s.premiumUsageForAdminWithTasks(a.ID, accounts, tasks)
			available := maxInt(0, cap.Premium.Total-inside-inFlight)
			total += available
			event := model.AutoRotationEvent{Type: "seat_query", AdminAccountID: a.ID, Message: "实时查询 5x 席位", Details: map[string]any{"total": cap.Premium.Total, "used": cap.Premium.Used, "remote_remaining": cap.Premium.Remaining, "inside_premium": inside, "in_flight_invites": inFlight, "available": available}}
			if len(runID) > 0 {
				event.RunID = runID[0]
			}
			s.enqueueAuditEvent(event)
		}
	}
	return total
}
func (s *Server) selectPremiumAdmin(ctx context.Context, admins []model.AdminAccountProfile, accountID, runID string) (string, error) {
	for _, a := range admins {
		v, _ := s.autoAdminLocks.LoadOrStore(a.ID, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		lock.Lock()
		_, c, e := s.currentAdminCredential(ctx, a.ID)
		if e == nil {
			cap, e := s.capacityForAdmin(ctx, a, c.AccessToken)
			if e == nil {
				accounts := s.store.FreeAccounts()
				inside, inFlight := s.premiumUsageForAdmin(a.ID, accounts)
				available := cap.Premium.Total - inside - inFlight
				if available > 0 {
					lock.Unlock()
					s.enqueueAuditEvent(model.AutoRotationEvent{RunID: runID, AccountID: accountID, AdminAccountID: a.ID, Type: "seat_check", Source: "auto_rotation", Operation: "invite", Stage: "seat_check", Message: "实时席位规则允许执行", Details: map[string]any{"remote_total": cap.Premium.Total, "remote_remaining": cap.Premium.Remaining, "inside_premium": inside, "in_flight_invites": inFlight, "available": available}})
					return a.ID, nil
				}
			}
		}
		lock.Unlock()
	}
	return "", errors.New("没有可分配的 5x 席位")
}

func (s *Server) premiumUsageForAdmin(adminID string, accounts []model.FreeAccountProfile) (inside, inFlight int) {
	tasks := s.store.AutoRotationTasks("")
	return s.premiumUsageForAdminWithTasks(adminID, accounts, tasks)
}

func (s *Server) premiumUsageForAdminWithTasks(adminID string, accounts []model.FreeAccountProfile, tasks []model.AutoRotationTask) (inside, inFlight int) {
	for _, account := range accounts {
		if account.AdminAccountID != adminID || !isPremiumSeatType(account.SeatType) || account.RemoveStatus == "completed" {
			continue
		}
		if account.AcceptStatus == "completed" {
			inside++
		}
	}
	inFlight = pendingAutoInviteCountForAdmin(tasks, accounts, adminID)
	return inside, inFlight
}

func pendingAutoInviteCount(tasks []model.AutoRotationTask, accounts []model.FreeAccountProfile) int {
	count := 0
	byID := make(map[string]model.FreeAccountProfile, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	for _, task := range tasks {
		if task.SeatType != "prolite" || (task.Status != "queued" && task.Status != "running") {
			continue
		}
		account, ok := byID[task.AccountID]
		// A durable task can outlive its account record (for example when an
		// operator deletes a failed pipeline record or after an older run was
		// recovered).  A missing account cannot still hold an invitation seat,
		// so it must not reduce the current available capacity.
		if !ok {
			continue
		}
		if account.AcceptStatus == "completed" || account.RemoveStatus == "completed" {
			continue
		}
		count++
	}
	return count
}

func pendingAutoInviteCountForAdmin(tasks []model.AutoRotationTask, accounts []model.FreeAccountProfile, adminID string) int {
	byID := make(map[string]model.FreeAccountProfile, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	count := 0
	for _, task := range tasks {
		if task.AdminAccountID != adminID || task.SeatType != "prolite" || (task.Status != "queued" && task.Status != "running") {
			continue
		}
		account, ok := byID[task.AccountID]
		if !ok {
			continue
		}
		if account.AcceptStatus == "completed" || account.RemoveStatus == "completed" {
			continue
		}
		count++
	}
	return count
}

func (s *Server) executeAutoTask(ctx context.Context, task model.AutoRotationTask, run *model.AutoRotationRun, runMu *sync.Mutex, settings model.AutoRotationSettings) {
	taskCtx := context.WithValue(ctx, autoRotationTraceContextKey{}, autoRotationTraceContext{RunID: task.RunID, TaskID: task.ID})
	provider := "sub2"
	if subSettings, _, settingsErr := s.store.Sub2Settings(); settingsErr == nil {
		provider = providerForSettings(subSettings)
	}
	defer s.store.ReleaseAutoRotationClaim(task.AccountID)
	step := func(key string, status string, msg string) {
		now := time.Now()
		for i := range task.Steps {
			if task.Steps[i].Key == key {
				task.Steps[i].Status = status
				task.Steps[i].Message = msg
				if status == "running" {
					task.Steps[i].StartedAt = &now
				} else {
					task.Steps[i].CompletedAt = &now
				}
			}
		}
		task.CurrentStep = key
		task.Status = status
		task.Error = msg
		_ = s.store.UpdateAutoRotationTask(task)
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID, Type: "step", Operation: key, Source: "auto_rotation", Provider: provider, Stage: key, ToStatus: status, Message: msg})
	}
	attemptStep := func(fn func() error) error {
		var err error
		for attempt := 0; attempt <= settings.RetryCount; attempt++ {
			if attempt > 0 {
				task.RetryCount++
				_ = s.store.UpdateAutoRotationTask(task)
				message := "自动重试"
				if task.CurrentStep == "oauth" {
					message = "OAuth 失败，重新创建全新 CodexAuthRT 会话并从登录开始执行"
				}
				s.enqueueAuditEvent(model.AutoRotationEvent{RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID, Type: "retry", Operation: task.CurrentStep, Source: "auto_rotation", Provider: provider, Stage: task.CurrentStep, Attempt: attempt, Message: message, Details: map[string]any{"fresh_oauth_session": task.CurrentStep == "oauth"}})
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			started := time.Now()
			err = fn()
			message := "请求成功"
			if err != nil {
				message = err.Error()
			}
			s.enqueueAuditEvent(model.AutoRotationEvent{RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID, Type: "request", Operation: task.CurrentStep, Source: "auto_rotation", Provider: provider, Stage: task.CurrentStep, Attempt: attempt + 1, DurationMS: time.Since(started).Milliseconds(), Message: message})
			if err == nil {
				return nil
			}
			if errors.Is(err, errDeadAccountHandled) {
				return err
			}
		}
		return err
	}
	step("invite", "running", "")
	// The local reservation is counted as in-flight before the network request
	// starts, so a crash or delayed upstream response cannot expose the seat to
	// another concurrent task.
	task.InviteTriggered = true
	_ = s.store.UpdateAutoRotationTask(task)
	if err := attemptStep(func() error {
		return s.invokeFreeHandler(taskCtx, "join", task.AccountID, map[string]any{"admin_account_id": task.AdminAccountID, "seat_type": "prolite"})
	}); err != nil {
		step("invite", "failed", err.Error())
		s.cleanupFailedAutoTask(taskCtx, &task, "invite", err)
		runMu.Lock()
		run.Failed++
		runMu.Unlock()
		return
	}
	step("invite", "completed", "")
	step("oauth", "running", "")
	if err := attemptStep(func() error { return s.autoOAuth(taskCtx, task.AccountID) }); err != nil {
		step("oauth", "failed", err.Error())
		s.cleanupFailedAutoTask(taskCtx, &task, "oauth", err)
		runMu.Lock()
		run.Failed++
		runMu.Unlock()
		return
	}
	step("oauth", "completed", "")
	step("push", "running", "")
	if err := attemptStep(func() error { return s.invokeFreeHandler(taskCtx, "push", task.AccountID, nil) }); err != nil {
		step("push", "failed", err.Error())
		s.cleanupFailedAutoTask(taskCtx, &task, "push", err)
		runMu.Lock()
		run.Failed++
		runMu.Unlock()
		return
	}
	step("push", "completed", "")
	step("quota", "running", "")
	if err := attemptStep(func() error { return s.invokeFreeHandler(taskCtx, "quota", task.AccountID, nil) }); err != nil {
		step("quota", "failed", err.Error())
		s.cleanupFailedAutoTask(taskCtx, &task, "quota", err)
		runMu.Lock()
		run.Failed++
		runMu.Unlock()
		return
	}
	step("quota", "completed", "")
	// The reservation protects the invite/enter window. Once the account has
	// been pushed to the active downstream and its quota has been read successfully, the account
	// is fully established in the Team space and no longer needs a temporary
	// reservation. Release it before the next automatic rotation so stale
	// reservations cannot consume capacity.
	if task.ReservationID != "" && task.SeatReserved {
		if err := s.store.ReleaseSeatReservation(task.ReservationID); err == nil {
			task.SeatReserved = false
			_ = s.store.UpdateAutoRotationTask(task)
			provider := "Sub2"
			if settings, _, settingsErr := s.store.Sub2Settings(); settingsErr == nil && strings.EqualFold(settings.Provider, "cpa") {
				provider = "CPA"
			}
			s.enqueueAuditEvent(model.AutoRotationEvent{RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID, Type: "seat_released", Source: "auto_rotation", Provider: strings.ToLower(provider), Operation: "quota", Stage: "quota", Message: provider + " 推送并额度获取成功，释放 5x 席位预占", Details: map[string]any{"reservation_id": task.ReservationID, "provider": strings.ToLower(provider)}})
		}
	}
	task.Status = "completed"
	task.CurrentStep = ""
	now := time.Now()
	task.CompletedAt = &now
	_ = s.store.UpdateAutoRotationTask(task)
	runMu.Lock()
	run.Succeeded++
	runMu.Unlock()
}

// cleanupFailedAutoTask runs only after all configured retries for a step have
// been exhausted. Accounts that reached the Team space are removed immediately
// so a broken OAuth/push/quota flow cannot continue consuming a 5x seat.
func (s *Server) cleanupFailedAutoTask(ctx context.Context, task *model.AutoRotationTask, failedStage string, cause error) {
	profile, _, err := s.store.FreeAccountCredential(task.AccountID)
	if err != nil {
		return
	}
	now := time.Now()
	task.CompletedAt = &now
	task.Status = "failed"
	task.CurrentStep = failedStage
	task.Error = cause.Error()
	if profile.AcceptStatus != "completed" || profile.RemoveStatus == "completed" {
		if task.ReservationID != "" && task.SeatReserved {
			if releaseErr := s.store.ReleaseSeatReservation(task.ReservationID); releaseErr == nil {
				task.SeatReserved = false
				s.enqueueAuditEvent(model.AutoRotationEvent{RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID, Type: "seat_released", Source: "auto_rotation", Operation: failedStage, Stage: failedStage, Message: "自动轮转失败且账号未进入空间，释放席位预占"})
			}
		}
		_ = s.store.UpdateAutoRotationTask(*task)
		return
	}

	removeStep := -1
	for i := range task.Steps {
		if task.Steps[i].Key == "remove" {
			removeStep = i
			break
		}
	}
	if removeStep < 0 {
		task.Steps = append(task.Steps, model.AutoRotationStep{Key: "remove", Name: "失败后移出空间"})
		removeStep = len(task.Steps) - 1
	}
	task.Steps[removeStep].Status = "running"
	task.Steps[removeStep].Message = "自动轮转重试耗尽，正在移出空间"
	task.Steps[removeStep].StartedAt = &now
	task.CurrentStep = "remove"
	_ = s.store.UpdateAutoRotationTask(*task)
	s.enqueueAuditEvent(model.AutoRotationEvent{
		RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID,
		Type: "auto_failure_remove_start", Source: "auto_rotation", Operation: "remove", Stage: "remove", Level: "error",
		Message: "自动轮转重试耗尽，开始自动移出空间", Details: map[string]any{"failed_stage": failedStage, "error": cause.Error()},
	})

	_, removeErr := s.performFreeAccountRemove(ctx, task.AccountID)
	completedAt := time.Now()
	task.CompletedAt = &completedAt
	task.Status = "failed"
	if removeErr != nil {
		task.Error = fmt.Sprintf("%s；自动移出空间失败：%v", cause.Error(), removeErr)
		task.Steps[removeStep].Status = "failed"
		task.Steps[removeStep].Message = removeErr.Error()
		s.enqueueAuditEvent(model.AutoRotationEvent{
			RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID,
			Type: "auto_failure_remove_failed", Source: "auto_rotation", Operation: "remove", Stage: "remove", Level: "error",
			Message: "自动轮转失败后自动移出空间失败", Details: map[string]any{"failed_stage": failedStage, "flow_error": cause.Error(), "remove_error": removeErr.Error()},
		})
	} else {
		task.Error = cause.Error() + "；账号已自动移出空间"
		task.SeatReserved = false
		task.Steps[removeStep].Status = "completed"
		task.Steps[removeStep].Message = "已自动移出空间"
		s.enqueueAuditEvent(model.AutoRotationEvent{
			RunID: task.RunID, TaskID: task.ID, AccountID: task.AccountID, Email: task.Email, AdminAccountID: task.AdminAccountID,
			Type: "auto_failure_remove_success", Source: "auto_rotation", Operation: "remove", Stage: "remove", Level: "error",
			Message: "自动轮转失败账号已自动移出空间", Details: map[string]any{"failed_stage": failedStage, "flow_error": cause.Error()},
		})
	}
	task.Steps[removeStep].CompletedAt = &completedAt
	_ = s.store.UpdateAutoRotationTask(*task)
}

func (s *Server) invokeFreeHandler(ctx context.Context, op, id string, body map[string]any) error {
	var path string
	var handler http.HandlerFunc
	switch op {
	case "join":
		path = "/api/free-accounts/" + id + "/join"
		handler = s.joinFreeAccount
	case "push":
		path = "/api/free-accounts/" + id + "/push"
		handler = s.pushFreeAccount
	case "quota":
		path = "/api/free-accounts/" + id + "/quota"
		handler = s.checkFreeAccountQuota
	default:
		return errors.New("不支持的自动步骤")
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(string(data)))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	handler(rec, req)
	// Capture a compact, secret-free request/response envelope for the
	// execution history. This is diagnostic-only and does not alter the
	// handler's response or control flow.
	var responsePayload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &responsePayload)
	provider := ""
	if settings, _, settingsErr := s.store.Sub2Settings(); settingsErr == nil {
		provider = providerForSettings(settings)
	}
	profile, _, _ := s.store.FreeAccountCredential(id)
	trace, _ := ctx.Value(autoRotationTraceContextKey{}).(autoRotationTraceContext)
	s.enqueueAuditEvent(model.AutoRotationEvent{
		RunID: trace.RunID, TaskID: trace.TaskID, AccountID: id, Email: profile.Email,
		Type: "exchange", Source: "auto_rotation", Provider: provider, Operation: op, Stage: op,
		HTTPStatus: rec.Code, Message: "自动步骤请求返回", Request: body,
		Response: responsePayload,
	})
	if rec.Code >= 300 {
		var out response
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return errors.New(out.Error)
	}
	return nil
}

func (s *Server) autoOAuth(ctx context.Context, id string) error {
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/free-accounts/"+id+"/oauth/start", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.startFreeAccountOAuth(rec, req)
	if rec.Code >= 300 {
		return errors.New(rec.Body.String())
	}
	var out response
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		return errors.New("OAuth 响应无效")
	}
	raw, _ := json.Marshal(out.Data)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	job, _ := payload["job"].(map[string]any)
	jobID := fmt.Sprint(job["job_id"])
	if jobID == "<nil>" || jobID == "" {
		return errors.New("OAuth 任务未创建")
	}
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("OAuth 超时")
		case <-ticker.C:
			s.oauthMu.RLock()
			j := cloneRegistrationJob(s.oauthJobs[jobID])
			s.oauthMu.RUnlock()
			if fmt.Sprint(j["status"]) == "success" {
				return nil
			}
			if fmt.Sprint(j["status"]) == "failed" {
				if profile, _, err := s.store.FreeAccountCredential(id); err == nil && profile.Dead {
					return errDeadAccountHandled
				}
				return errors.New(fmt.Sprint(j["error"]))
			}
		}
	}
}
