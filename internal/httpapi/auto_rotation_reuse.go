package httpapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Reuse-enabled batches stage every selected account before starting network
// membership mutations. The disabled path keeps its established behavior.
func (s *Server) executeAutoRotationWithReuse(ctx context.Context, run model.AutoRotationRun, settings model.AutoRotationSettings) {
	run.ReuseEnabled = true
	admins := s.store.AdminAccounts()
	accounts := s.store.FreeAccounts()
	allMailAccounts := s.store.MailAccounts()
	mails := make([]model.MailAccountProfile, 0, len(allMailAccounts))
	proEmails := make(map[string]bool)
	for _, mail := range allMailAccounts {
		if strings.EqualFold(strings.TrimSpace(mail.ManagementScope), "pro") {
			proEmails[strings.ToLower(strings.TrimSpace(mail.Email))] = true
		} else {
			mails = append(mails, mail)
		}
	}
	waiting, used := reuseRotationCandidates(accounts, proEmails)
	run.CandidateRotationCount = len(waiting) + len(used)
	run.CandidateMailCount = len(mails)
	available := s.availablePremiumSlots(ctx, admins, run.ID)
	need := autoRotationPlan(settings.MaxPerRun, available, 0, available)
	run.DecisionAvailableSeats = available
	run.Planned = need
	_ = s.store.UpdateAutoRotationRun(run)
	s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "candidate_scan", Source: "auto_rotation", Operation: "plan", Stage: "prepare", Message: "复用轮次开始扫描候选账号", Details: map[string]any{"target": need, "waiting": len(waiting), "used": len(used), "mail": len(mails), "reserved": run.ReservedSeats, "gross_remaining": run.SeatRemaining}})
	if need == 0 {
		run.Status, run.Reason = "completed", "当前没有可分配的 5x 席位"
		now := time.Now()
		run.CompletedAt = &now
		_ = s.store.UpdateAutoRotationRun(run)
		return
	}

	queued := make([]model.AutoRotationTask, 0, need)
	logCandidate := func(account model.FreeAccountProfile, email, source, status, message string) {
		level := "info"
		if status == "failed" {
			level = "error"
			run.PreparationFailed++
		}
		s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, AccountID: account.ID, Email: email, Type: "candidate_" + status, Source: "auto_rotation", Operation: "prepare", Stage: "prepare", Level: level, Message: message, Details: map[string]any{"source": source}})
	}
	queue := func(account model.FreeAccountProfile, source string) {
		taskID := randomRegistrationID()
		claimed, err := s.store.ClaimAutoRotationAccount(account.ID, taskID)
		if err != nil || !claimed {
			logCandidate(account, account.Email, source, "skipped", "账号正在执行其他流程，等待下一轮")
			return
		}
		release := true
		defer func() {
			if release {
				_ = s.store.ReleaseAutoRotationClaim(account.ID)
			}
		}()
		// Resolve an unused mother before doing an expensive AT/login check.
		if _, err = s.selectPremiumAdmin(ctx, admins, account.ID, run.ID); err != nil {
			logCandidate(account, account.Email, source, "skipped", err.Error())
			return
		}
		if account.RemoveStatus == "completed" {
			unlock := s.lockFreeAccount(account.ID)
			prepareCtx := context.WithValue(ctx, autoRotationTraceContextKey{}, autoRotationTraceContext{RunID: run.ID, CycleID: account.CycleID})
			account, err = s.prepareAccountReuse(prepareCtx, account)
			unlock()
			if err != nil {
				logCandidate(account, account.Email, source, "failed", "复用准备失败："+err.Error())
				return
			}
		}
		account, err = s.store.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
			item.JoinMethod = settings.JoinMethod
			if item.RemoveMethod != "mother_kick" && item.RemoveMethod != "child_leave" {
				item.RemoveMethod = settings.RemoveMethod
			}
		})
		if err != nil {
			logCandidate(account, account.Email, source, "failed", "保存本轮进入方式失败："+err.Error())
			return
		}
		s.seatAssignmentMu.Lock()
		adminID, selectErr := s.selectPremiumAdmin(ctx, admins, account.ID, run.ID)
		if selectErr != nil {
			s.seatAssignmentMu.Unlock()
			logCandidate(account, account.Email, source, "skipped", selectErr.Error())
			return
		}
		task := model.AutoRotationTask{CycleID: account.CycleID, ID: taskID, RunID: run.ID, AccountID: account.ID, Email: account.Email, Source: source, AdminAccountID: adminID, SeatType: "prolite", Status: "queued", StartedAt: time.Now(), Steps: autoSteps(settings.JoinMethod, settings.RemoveMethod)}
		err = s.store.SaveAutoRotationTask(task)
		s.seatAssignmentMu.Unlock()
		if err != nil {
			logCandidate(account, account.Email, source, "failed", "创建待执行任务失败："+err.Error())
			return
		}
		release = false
		queued = append(queued, task)
		run.Prepared = len(queued)
		_ = s.store.UpdateAutoRotationRun(run)
		logCandidate(account, account.Email, source, "prepared", fmt.Sprintf("已进入等待进入空间，分配母号 %s，等待本批准备完成", adminID))
	}
	for _, account := range accounts {
		if account.TeamAccountID != "" && account.RemoveStatus != "completed" && account.AcceptStatus != "completed" &&
			(account.InviteStatus == "running" || account.InviteStatus == "completed") {
			logCandidate(account, account.Email, "rotation", "skipped", "已有指定母号的邀请/申请在途；保留原流程和席位占用，不重复分配")
		}
	}

	for _, account := range waiting {
		if len(queued) >= need || ctx.Err() != nil {
			break
		}
		queue(account, "rotation")
	}
	for _, account := range used {
		if len(queued) >= need || ctx.Err() != nil {
			break
		}
		queue(account, "reuse")
	}
	// Mail is a true fallback: never import an already-used identity here.
	sort.SliceStable(mails, func(i, j int) bool { return mails[i].CreatedAt.Before(mails[j].CreatedAt) })
	known := make(map[string]model.FreeAccountProfile, len(accounts))
	for _, account := range accounts {
		known[strings.ToLower(strings.TrimSpace(account.Email))] = account
	}
	mailImported := 0
	mailImportBudget := need - len(queued)
	for _, mail := range mails {
		if len(queued) >= need || ctx.Err() != nil {
			break
		}
		email := strings.ToLower(strings.TrimSpace(mail.Email))
		if !eligibleAutoRotationMail(mail) {
			continue
		}
		if existing, ok := known[email]; ok {
			if existing.ImportMode == "pure" && existing.VisitedTeamCount == 0 &&
				!existing.Dead && !existing.HistoryUncertain && !existing.Quality.Excluded &&
				existing.TeamAccountID == "" && existing.AcceptStatus != "completed" {
				queue(existing, "mail")
			}
			continue
		}
		archived, found, err := s.store.ArchivedTeamAccount(email, mail.OAuthUserID)
		if err != nil {
			logCandidate(model.FreeAccountProfile{}, email, "mail", "failed", "读取归档轮次失败："+err.Error())
			continue
		}
		if found && (archived.Dead || archived.HistoryUncertain || archived.VisitedTeamCount > 0 || archived.TeamAccountID != "") {
			logCandidate(model.FreeAccountProfile{}, email, "mail", "skipped", "归档账号已使用、已判死号或历史待确认，不能按首次使用导入")
			continue
		}
		visits, err := s.store.TeamVisits(email, mail.OAuthUserID)
		if err != nil {
			logCandidate(model.FreeAccountProfile{}, email, "mail", "failed", "读取历史母号失败："+err.Error())
			continue
		}
		if len(visits) > 0 {
			logCandidate(model.FreeAccountProfile{}, email, "mail", "skipped", "该邮箱已有进入过的母号，不能作为首次使用账号导入")
			continue
		}
		if mailImported >= mailImportBudget {
			break
		}
		account, err := s.importMailAccountToTeam(ctx, mail.Email)
		if err != nil {
			logCandidate(model.FreeAccountProfile{}, email, "mail", "failed", "邮件账号导入失败："+err.Error())
			continue
		}
		mailImported++
		known[email] = account
		if account.VisitedTeamCount > 0 {
			logCandidate(account, email, "mail", "skipped", "账号身份有历史母号，不能作为首次使用账号调度")
			continue
		}
		queue(account, "mail")
	}
	run.Prepared = len(queued)
	run.Unfilled = need - len(queued)
	run.Started = len(queued)
	_ = s.store.UpdateAutoRotationRun(run)
	s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "preparation_complete", Source: "auto_rotation", Operation: "plan", Stage: "prepare", Message: "所有候选准备完成，开始执行本批任务", Details: map[string]any{"target": need, "prepared": run.Prepared, "preparation_failed": run.PreparationFailed, "started": run.Started, "unfilled": run.Unfilled, "mail_imported": mailImported}})
	sem := make(chan struct{}, maxInt(1, settings.Concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, task := range queued {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.executeAutoTask(ctx, task, &run, &mu, settings)
		}()
	}
	wg.Wait()
	now := time.Now()
	run.CompletedAt = &now
	switch {
	case run.Unfilled > 0:
		run.Status = "partial"
		run.Reason = fmt.Sprintf("目标 %d 个，实际准备 %d 个；缺少 %d 个可用账号或席位", need, run.Prepared, run.Unfilled)
	case run.Failed > 0 && run.Succeeded > 0:
		run.Status = "partial"
	case run.Failed > 0:
		run.Status = "failed"
	default:
		run.Status = "completed"
	}
	_ = s.store.UpdateAutoRotationRun(run)
	s.enqueueAuditEvent(model.AutoRotationEvent{RunID: run.ID, Type: "final", Source: "auto_rotation", Operation: "run", Stage: "complete", Message: run.Reason, Details: map[string]any{"status": run.Status, "planned": run.Planned, "prepared": run.Prepared, "started": run.Started, "succeeded": run.Succeeded, "failed": run.Failed, "unfilled": run.Unfilled}})
}

func reuseRotationCandidates(accounts []model.FreeAccountProfile, proEmails map[string]bool) (waiting, used []model.FreeAccountProfile) {
	for _, account := range accounts {
		if proEmails[strings.ToLower(strings.TrimSpace(account.Email))] {
			continue
		}
		if reusableAccount(account) {
			used = append(used, account)
			continue
		}
		// A bound invitation is in-flight or uncertain. It already consumes
		// capacity and must not be reassigned to another mother.
		if eligibleAutoRotationAccount(account) && account.TeamAccountID == "" && !activeTeamMembership(account) {
			waiting = append(waiting, account)
		}
	}
	sort.SliceStable(waiting, func(i, j int) bool {
		left, right := waiting[i].ImportedAt, waiting[j].ImportedAt
		if left.IsZero() {
			left = waiting[i].CreatedAt
		}
		if right.IsZero() {
			right = waiting[j].CreatedAt
		}
		return left.Before(right)
	})
	sort.SliceStable(used, func(i, j int) bool {
		if used[i].RemovedAt == nil {
			return false
		}
		if used[j].RemovedAt == nil {
			return true
		}
		return used[i].RemovedAt.Before(*used[j].RemovedAt)
	})
	return waiting, used
}
