package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"
)

func (s *Server) restockTasks(snap restock.Snapshot, p model.ProSettings, c restock.Config) ([]restock.Task, float64, error) {
	// The tick is the only launcher. Capture ownership before reading tasks so
	// a preparation finishing during this read cannot be overwritten by stale data.
	s.proAutoMu.Lock()
	preparationActive := s.proRestockStartJobs["restock-preparation-worker"] != nil
	s.proAutoMu.Unlock()
	tasks, e := s.store.ProRestockTasks()
	if e != nil {
		return nil, 0, e
	}
	now := time.Now()
	byID := map[int64]restock.Account{}
	for _, a := range snap.Accounts {
		byID[a.ID] = a
	}
	seen := map[string]bool{}
	durations := []float64{}
	for i := range tasks {
		t := &tasks[i]
		if t.Active() {
			seen[t.Email] = true
		}
		if !t.Active() {
			if t.ReadyAt != nil && t.SubmittedAt != nil && t.Binding == snap.Binding {
				durations = append(durations, restockObservedLead(*t))
			}
			continue
		}
		// Card preparation owns this task until it returns. Monitoring must not
		// overwrite its submitting/waiting state while the issuer is slow.
		s.proAutoMu.Lock()
		preparing := s.proRestockStartJobs[t.ID] != nil
		s.proAutoMu.Unlock()
		if preparationActive || preparing {
			continue
		}
		profile, _, err := s.store.MailAccountCredential(t.Email)
		if err != nil {
			t.Status = "attention"
			t.Message = "账号无法读取，保留在途与预算等待核验"
			t.ETA = nil
			if err = s.store.SaveProRestockObservation(t); err != nil {
				return nil, 0, err
			}
			continue
		}
		if profile.ProAuto.ID != "" {
			t.OrderID = profile.ProAuto.OrderID
		}
		if replacement := profile.ProAuto.Replacement; replacement != nil {
			if replacement.Queued && t.Status == "reserved" {
				continue
			}
			if profile.ProAuto.Stage == "recharge" && profile.ProAuto.Status == "failed" && !s.proAutomationRunning(t.Email) {
				order, _, lookupErr := s.store.GPTPayOrder(firstNonEmpty(t.OrderID, profile.ProAuto.PaymentOrderID()))
				if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
					return nil, 0, lookupErr
				}
				if lookupErr != nil || order.Status != "success" {
					t.Status, t.Stage, t.ETA, t.UpdatedAt = "failed", "recharge", nil, now
					t.Message = firstNonEmpty(replacement.Message, profile.ProAuto.Error)
					if lookupErr == nil {
						t.OrderID = order.ID
						if order.Active() || order.PaymentReviewBlocksRestock() {
							t.Status = "unknown"
						}
					} else {
						t.Cost = 0
					}
					if err = s.store.SaveProRestockObservation(t); err != nil {
						return nil, 0, err
					}
					continue
				}
			}
		}
		if finished, err := s.finishUnsubmittedRestockPreparation(t, profile); err != nil {
			return nil, 0, err
		} else if finished {
			continue
		}
		if profile.IsProDead() && !s.proAutomationRunning(t.Email) {
			if _, e := s.store.ActiveGPTPayOrder(t.Email); errors.Is(e, sql.ErrNoRows) {
				t.Status, t.Stage, t.ETA = "failed", "dead", nil
				t.UpdatedAt = now
				t.Message = "已确认死号，不再计入供给；关联卡片禁止再次开通"
				if err = s.store.SaveProRestockObservation(t); err != nil {
					return nil, 0, err
				}
				continue
			} else if e != nil {
				return nil, 0, e
			}
		}
		if profile.ProAuto.ID != "" {
			t.CardID = profile.ProAuto.CardID
		}
		t.AccountID = s.siwcRestockProfileID(profile, p)
		if waiting, err := s.trackRestockCardImport(t, profile); err != nil {
			return nil, 0, err
		} else if waiting {
			if err = s.store.SaveProRestockObservation(t); err != nil {
				return nil, 0, err
			}
			continue
		}
		if t.OrderID == "" && profile.ProAuto.ID != "" {
			order, _, lookupErr := s.store.GPTPayOrder(profile.ProAuto.PaymentOrderID())
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return nil, 0, lookupErr
			}
			if lookupErr == nil {
				t.OrderID = order.ID
				_, err = s.store.UpdateProAccount(t.Email, func(p *model.MailAccountProfile) { p.ProAuto.OrderID = order.ID })
				if err != nil {
					return nil, 0, err
				}
			}
		}
		settled, err := s.restockExitSettled(profile, t.OrderID)
		if err != nil {
			return nil, 0, err
		}
		if settled {
			t.Status, t.Stage, t.ETA = "ready", "merge", nil
			t.UpdatedAt = now
			t.Message = "已完成推送并进入合并／移出阶段，不再占用补货在途；当前可用容量以监控组为准"
			// No readiness observation was made: do not invent ReadyAt and count
			// the account's entire consumption lifetime as provisioning latency.
			if err = s.store.SaveProRestockObservation(t); err != nil {
				return nil, 0, err
			}
			continue
		}
		if restockAccountExiting(profile) {
			t.Status, t.ETA = "unknown", nil
			t.UpdatedAt = now
			t.Message = "账号已进入合并／移出，但原付款记录尚未核清，保留订单与在途；不承诺新增容量"
			if err = s.store.SaveProRestockObservation(t); err != nil {
				return nil, 0, err
			}
			continue
		}
		if !profile.ProAuto.StopRequested && profile.ProAuto.Status == "interrupted" && profile.ProAuto.Steps["oauth"] == "completed" && !s.proAutomationRunning(t.Email) {
			next := time.Now()
			_, err = s.store.UpdateProAccount(t.Email, func(p *model.MailAccountProfile) {
				if !p.ProAuto.StopRequested && p.ProAuto.Status == "interrupted" {
					p.ProAuto.Status = "waiting_quota"
					p.ProAuto.NextCheckAt = &next
				}
			})
			if err == nil {
				s.queueDueProAutomation(t.Email)
			}
		}
		if t.Stages == nil {
			t.Stages = map[string]time.Time{}
		}
		for key, at := range profile.ProAuto.StageTimes {
			t.Stages[key] = at
		}
		if profile.ProAuto.Stage != "" {
			t.Stage = profile.ProAuto.Stage
			if _, ok := t.Stages[t.Stage]; !ok {
				t.Stages[t.Stage] = now
			}
		}
		verified := false
		// A task may have waited while the operator changed the push settings.
		// Verify its actual submitted destination, not its reservation snapshot.
		if profile.PushStatus == "completed" && profile.ProRestockTargetID != "" && profile.ProAuto.RestockBinding != "" && t.Binding != profile.ProAuto.RestockBinding {
			pinned, _, _, pe := s.proAccountPushSettings(profile)
			if pe != nil {
				return nil, 0, pe
			}
			if pinned.Provider == "sub2" && len(pinned.Sub2.GroupIDs) > 0 && restockBinding(pinned) == profile.ProAuto.RestockBinding {
				t.Binding = profile.ProAuto.RestockBinding
				if t.Binding == snap.Binding && slices.Contains(pinned.Sub2.GroupIDs, snap.GroupID) {
					t.GroupID = snap.GroupID
				} else if !slices.Contains(pinned.Sub2.GroupIDs, t.GroupID) {
					t.GroupID = pinned.Sub2.GroupIDs[0]
				}
			}
		}
		pushedAt := t.Stages["push:completed"]
		if profile.PushStatus == "completed" && pushedAt.IsZero() {
			// Legacy/manual completion has no milestone: persist the first
			// observation, then require a later collection rather than guessing.
			pushedAt = now
			t.Stages["push:completed"] = pushedAt
		}
		if a, ok := byID[t.AccountID]; ok {
			verified = a.Ready && !a.TakeoverPending && restockSnapshotFresh(c, snap, now) && snap.RefreshedAfter(pushedAt) && t.Binding == snap.Binding && t.GroupID == snap.GroupID
		}
		if (t.Binding != snap.Binding || t.GroupID != snap.GroupID) && profile.PushStatus == "completed" {
			pinned, secret, _, pe := s.proAccountPushSettings(profile)
			if pe == nil {
				checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				accounts, ae := s.sub2.MonitorAccounts(checkCtx, pinned.Sub2, secret, t.GroupID)
				cancel()
				if ae == nil {
					for _, a := range accounts {
						if a.ID == t.AccountID && a.Ready(now, pinned.Sub2.Models) {
							verified = true
						}
					}
				}
			}
		}
		if verified && profile.PushStatus == "completed" {
			t.Status = "ready"
			t.ReadyAt = &now
			t.ETA = nil
			t.Message = "已核验 Sub2 账号、组归属、模型和可调度状态"
		} else if profile.ProAuto.ID != "" {
			t.Status = "running"
			t.Message = profile.ProAuto.DisplayError()
			if profile.PushStatus == "completed" {
				t.Status = "verifying"
				t.Message = "推送已完成，等待新一轮监控确认账号可用"
			}
			if profile.PushStatus != "completed" && !s.proAutomationRunning(t.Email) && profile.ProAuto.Status != "waiting_quota" {
				t.Status = "attention"
				t.ETA = nil
				t.Message = firstNonEmpty(profile.ProAuto.DisplayError(), "流程已停止，保留原订单与在途，请在 Pro 管理中核验或续跑")
			}
			if t.OrderID != "" {
				order, _, err := s.store.GPTPayOrder(t.OrderID)
				if err != nil || (order.Active() && (order.Status == "submission_unknown" || !s.proAutomationRunning(t.Email))) {
					t.Status = "unknown"
					t.ETA = nil
					t.Message = "原订单结果待确认，禁止重新付款"
				} else if order.Status == "failed" && !s.proAutomationRunning(t.Email) {
					t.Status = "failed"
					t.ETA = nil
					if order.PaymentReviewBlocksRestock() {
						t.Status, t.Message = "attention", gptpay.PaymentReviewMessage
						if t.PaymentRiskSource != "" {
							if _, riskErr := s.store.FinishProRestockPaymentRisk(*t); riskErr != nil {
								return nil, 0, riskErr
							}
							t.Message = "替补账号再次支付风控，自动补货已暂停；付款结算仍待核实"
						}
					} else if order.PaymentReviewRequired() {
						t.Message = gptpay.PaymentReviewRestockMessage
						paused, riskErr := s.store.FinishProRestockPaymentRisk(*t)
						if riskErr != nil {
							return nil, 0, riskErr
						}
						if paused {
							t.Message = "替补账号再次支付风控，自动补货已暂停；原账号和订单保留待人工核实"
						} else {
							t.PaymentRiskPending = true
						}
					}
				} else if order.Status == "success" && profile.ProAuto.Steps["oauth"] != "completed" && (profile.ProAuto.Status == "interrupted" || profile.ProAuto.Status == "failed") {
					s.queueRestockPaidRecovery(profile, p)
				}
			} else if t.Status == "attention" {
				t.Status = "failed"
				t.Cost = 0
			}
		} else if t.Status != "reserved" {
			t.Status = "attention"
			t.ETA = nil
			t.Message = firstNonEmpty(t.Message, "启动结果待核验，保留预留，避免重复发卡")
		}
		t.UpdatedAt = now
		if err = s.store.SaveProRestockObservation(t); err != nil {
			return nil, 0, err
		}
	}
	lead := float64(c.LeadMinutes)
	if len(durations) >= c.LeadSamples {
		sort.Float64s(durations)
		lead = durations[min(len(durations)-1, int(math.Ceil(float64(len(durations))*.9))-1)]
	}
	// Reconciliation only needs workflow state. The mailbox list decrypts every
	// credential (including large session payloads) under the shared store lock.
	profiles, err := s.store.ProRestockProfiles()
	if err != nil {
		return nil, 0, err
	}
	for _, profile := range profiles {
		if seen[profile.Email] || profile.ProRestockTaskID != "" {
			continue
		}
		settled, err := s.restockExitSettled(profile, "")
		if err != nil {
			return nil, 0, err
		}
		if settled {
			continue
		}
		if profile.ProAuto.ID == "" {
			manual := false
			for _, stage := range profile.ProManualStages {
				if stage.Status == "running" {
					manual = true
				}
			}
			if profile.PushStatus != "completed" && profile.ProManualStages["recharge"].Status == "completed" {
				manual = true
			}
			order, err := s.store.ActiveGPTPayOrder(profile.Email)
			if err == nil {
				manual = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, 0, err
			}
			if manual {
				tasks = append(tasks, restock.Task{ID: "manual:" + profile.Email, Email: profile.Email, Status: "attention", Stage: "manual", OrderID: order.ID, Message: "手动任务占用在途；目标与就绪时间未经核验，不承诺未来供给"})
			}
			continue
		}
		if a, ok := byID[profile.Sub2AccountID]; ok && a.Ready && !restockAccountExiting(profile) {
			continue
		}
		target, _, _, targetErr := s.proAccountPushSettings(profile)
		if targetErr != nil || validateRestockTarget(c, target) != nil || restockBinding(target) != snap.Binding {
			continue
		}
		if profile.ProAuto.Status == "completed" && !restockAccountExiting(profile) {
			continue
		}
		running := s.proAutomationRunning(profile.Email)
		orderID := profile.ProAuto.OrderID
		if orderID == "" {
			// Recover the durable payment written just before its profile link.
			orderID = profile.ProAuto.PaymentOrderID()
		}
		order, _, orderErr := s.store.GPTPayOrder(orderID)
		if orderErr != nil && !errors.Is(orderErr, sql.ErrNoRows) {
			return nil, 0, orderErr
		}
		// Historical completed manual failures must not become new blocking
		// tasks on upgrade. Only a workflow stopped for review holds admission.
		review := profile.ProAuto.PaymentReviewRequired && orderErr == nil && order.PaymentReviewRequired()
		if !running && review && !order.PaymentReviewBlocksRestock() && profile.ProAuto.RestockBinding == snap.Binding && profile.ProAuto.Replacement == nil {
			// A current manual/scheduled full-auto failure gets the same durable
			// one-time replacement policy, without claiming historical failures.
			id := "external-risk:" + order.ID
			known := false
			for _, task := range tasks {
				known = known || task.ID == id
			}
			if !known {
				task := restock.Task{ID: id, Email: profile.Email, OrderID: order.ID, Binding: snap.Binding, GroupID: c.GroupID,
					ManagedExternally: true, Status: "failed", Stage: "recharge", CreatedAt: profile.ProAuto.StartedAt, UpdatedAt: now}
				if _, err := s.store.FinishProRestockPaymentRisk(task); err != nil {
					return nil, 0, err
				}
				persisted, err := s.store.ProRestockTasks()
				if err != nil {
					return nil, 0, err
				}
				for _, saved := range persisted {
					if saved.ID == id {
						tasks = append(tasks, saved)
					}
				}
			}
			continue
		}
		if !running && profile.ProAuto.Replacement != nil && orderErr == nil && order.Status == "failed" && !order.PaymentReviewBlocksRestock() {
			continue // the shared step-two chain owns replacement, not the legacy risk policy
		}
		if !running && !review && (orderErr != nil || order.Status == "failed") && profile.ProAuto.OrderID == "" && profile.ProAuto.Steps["recharge"] != "completed" && profile.ProAuto.Steps["oauth"] != "completed" && profile.PushStatus != "completed" {
			continue
		}
		if !running && orderErr == nil && order.Status == "failed" && !review {
			continue
		}
		t := restock.Task{Plan: "payment:" + profile.ProAuto.PlanCode, ID: profile.ProAuto.ID, Email: profile.Email, Status: "external", Stage: profile.ProAuto.Stage, OrderID: profile.ProAuto.OrderID, CreatedAt: profile.ProAuto.StartedAt, GroupID: c.GroupID, Binding: snap.Binding, Message: "手动／原定时任务在途，占用共用工作并发"}
		if !running {
			t.Status = "attention"
			t.Message = "旧任务已停止但付款或就绪结果仍待核验，保留在途"
		}
		if orderErr == nil {
			t.OrderID = order.ID
			if review {
				t.Status, t.Message = "attention", gptpay.PaymentReviewMessage
			}
			if order.Status == "submission_unknown" || order.Status == "manual_review" {
				t.Status = "unknown"
				t.Message = "原订单结果待确认，保留在途且不承诺未来容量"
			}
		}
		if restockAccountExiting(profile) {
			t.Status = "unknown"
			t.Message = "账号已进入合并／移出，但原付款记录尚未核清，保留订单与在途；不承诺新增容量"
		}
		t.SubmittedAt = &profile.ProAuto.StartedAt
		t.Plan = profile.ProAuto.QuotaPlan
		if profile.ProAuto.RestockBinding == "" {
			t.Binding = ""
			t.Status = "attention"
			t.Message = "旧任务缺少原推送绑定，保留在途但不承诺当前池供给"
		} else if profile.ProAuto.RestockBinding != snap.Binding {
			continue
		}
		tasks = append(tasks, t)
	}
	s.proMergeMu.Lock()
	busy := s.proAutoRunning
	s.proMergeMu.Unlock()
	s.proAutoMu.Lock()
	jobs := len(s.proAutoJobs)
	s.proAutoMu.Unlock()
	queued := max(busy, jobs)
	for i := range tasks {
		t := &tasks[i]
		if !t.Active() || t.Status == "unknown" || t.Status == "attention" || t.Status == "waiting_card" {
			continue
		}
		s.proAutoMu.Lock()
		preparing := s.proRestockStartJobs[t.ID] != nil
		s.proAutoMu.Unlock()
		if preparationActive || preparing {
			continue
		}
		remaining := lead
		if t.SubmittedAt != nil {
			remaining = restockRemainingLead(*t, tasks, c.LeadSamples, lead, now)
			if remaining <= 0 {
				t.ETA = nil
				t.Message = "已超过预计到货时间；仍占在途，不承诺未来容量"
				if t.DecisionID != "" {
					_ = s.store.SaveProRestockObservation(t)
				}
				continue
			}
		} else {
			// Queued members of a committed batch will dispatch as slots free
			// up. Do not promise an arrival by moving their ETA every tick.
			t.ETA = nil
			if t.Status == "reserved" && t.Message == "" {
				t.Message = "本批排队，等待全自动执行名额"
			}
			queued++
			if t.DecisionID != "" {
				if err := s.store.SaveProRestockObservation(t); err != nil {
					return nil, 0, err
				}
			}
			continue
		}
		eta := now.Add(time.Duration(remaining * float64(time.Minute)))
		t.ETA = &eta
		if t.DecisionID != "" {
			if err := s.store.SaveProRestockObservation(t); err != nil {
				return nil, 0, err
			}
		}
	}
	return tasks, lead * (1 + float64(queued/max(1, p.Concurrency))), nil
}

var errProRestockBusy = errors.New("正在采集或更新补货配置，请稍后重试")

func (s *Server) tickProRestock(ctx context.Context, force bool) (resultErr error) {
	if !s.proRestock.tick.TryLock() {
		return errProRestockBusy
	}
	defer s.proRestock.tick.Unlock()
	now := time.Now()
	c, e := s.store.ProRestockConfig()
	if e != nil {
		return e
	}
	if !force && now.Before(s.proRestock.next) {
		s.sampleRestockMoneyBetweenChecks(c, now)
		return nil
	}
	// Inactive rounds and preparation errors still honor the configured cadence.
	// Active collections replace this deadline with their measured completion.
	s.proRestock.next = now.Add(time.Duration(c.CheckSeconds) * time.Second)
	tasks, e := s.store.ProRestockTasks()
	if e != nil {
		return e
	}
	active := false
	for _, t := range tasks {
		if t.Active() {
			active = true
		}
	}
	if !force && !c.Enabled && !active && !c.DemandActive(now) {
		return nil
	}
	started := time.Now()
	var snap restock.Snapshot
	s.beginRestockCollection(started)
	defer func() {
		finished := time.Now()
		// Successful rounds use a start-to-start cadence, without concurrent
		// rounds or catch-up bursts. Failures retain the full retry delay.
		s.proRestock.next = nextRestockCheck(started, finished, c.CheckSeconds, resultErr == nil && restockSnapshotFresh(c, snap, finished))
		s.finishRestockCollection(finished, s.proRestock.next, resultErr)
	}()
	p, password, _, e := s.store.ProSettings()
	if e != nil {
		return e
	}
	selectedPay, _, purchaseErr := s.store.GPTPaySettings()
	purchasePro := p
	if purchaseErr == nil {
		purchasePro, selectedPay, purchaseErr = s.resolveProPurchase(p, selectedPay, c, time.Now())
	}
	sampleCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	demandScope := ""
	if c.DemandActive(time.Now()) {
		demandScope, e = s.store.ProRestockDemandScope()
		if e != nil {
			return e
		}
	}
	s.setRestockMonitorPhase("collecting")
	if s.proRestock.monitor != nil {
		snap, e = s.proRestock.monitor(sampleCtx, c, p, password)
	} else {
		snap, e = s.collectProRestock(sampleCtx, c, p, password)
	}
	cancel()
	if e != nil {
		snap.Valid = false
		snap.Error = e.Error()
	}
	if !snap.Valid || !snap.LoadValid {
		last, le := s.store.ProRestockSnapshot()
		if le == nil && last.Binding == restockBinding(p) && last.GroupID == c.GroupID && last.TakeoverToken == snap.TakeoverToken && last.Valid && last.LoadValid {
			last.Valid, last.LoadValid, last.Error = false, false, snap.Error
			snap = last
		}
	}
	if purchaseErr == nil && purchasePro.PurchaseRoute != nil {
		snap.NewPlan = restockPlan(selectedPay.Provider, selectedPay.PlanCode, selectedPay.Country, selectedPay.CreditQuantity)
	}
	if snap.ID == "" {
		snap.ID = randomRegistrationID()
	}
	snap.GroupID = c.GroupID
	if snap.Binding == "" {
		snap.Binding = restockBinding(p)
	}
	// Publish completed collection before slower reconciliation. Do not replace
	// lastSnapshot: confirmations and exit demand must compare distinct samples.
	s.publishRestockCollection(snap)
	if c.DemandActive(time.Now()) {
		gateCtx, gateCancel := context.WithTimeout(ctx, 15*time.Second)
		gate := s.sampleRestockDemand(gateCtx, c, p, snap)
		gateCancel()
		gate.Scope = demandScope
		gate = c.AdvanceDemand(gate, time.Now())
		if e = s.store.SaveProRestockDemand(gate); e != nil {
			return e
		}
		c.DemandGate = &gate
		snap.DemandGate = &gate
	}
	tasks, lead, e := s.restockTasks(snap, p, c)
	if e != nil {
		return e
	}
	// Reconciliation can durably pause admission after a second risk rejection.
	c, e = s.store.ProRestockConfig()
	if e != nil {
		return e
	}
	tasks, e = s.store.ReconcileForceMerge(c, snap, tasks)
	if e != nil {
		return e
	}
	s.setRestockMonitorPhase("evaluating")
	batchWaiting := restock.WaitForBatch(snap, tasks) != nil
	sampleFresh := restockSnapshotFresh(c, snap, time.Now())
	s.proRestock.mu.Lock()
	if batchWaiting || c.Version != s.proRestock.lastVersion || snap.Binding != s.proRestock.lastSnapshot.Binding || snap.TakeoverToken != s.proRestock.lastSnapshot.TakeoverToken || !sampleFresh {
		s.proRestock.confirmation = 0
		s.proRestock.exitConfirmation = 0
	}
	snap.ExitDemand = restock.ObserveExitDemand(c, snap, s.proRestock.lastSnapshot, time.Now())
	fresh := snap.At.After(s.proRestock.lastSample)
	if fresh {
		if sampleFresh && !batchWaiting && restock.Pressure(c, snap) {
			s.proRestock.confirmation++
		} else {
			s.proRestock.confirmation = 0
		}
		exit := restock.ForecastExits(c, snap, tasks, p.Sub2.AccountConcurrency, lead, time.Now())
		if sampleFresh && !batchWaiting && c.ExitEnabled && exit.Valid && exit.Pressure {
			s.proRestock.exitConfirmation++
		} else {
			s.proRestock.exitConfirmation = 0
		}
		s.proRestock.lastSample = snap.At
	}
	snap.ExitConfirmations = s.proRestock.exitConfirmation
	confirm := s.proRestock.confirmation
	s.proRestock.lastVersion = c.Version
	s.proRestock.mu.Unlock()
	// Collection and reconciliation can outlive the snapshot's freshness window.
	now = time.Now()
	limits, e := s.store.ProRestockLimits(now)
	if e != nil {
		return e
	}
	resumeImports := c.Enabled && c.Mode == "auto" && limits.Failures < c.FailureLimit && ctx.Err() == nil && s.proSIWCBlock() == ""
	s.observeTakeoverRecovery(c, snap, tasks)
	tc, _, te := s.store.ProTakeoverConfig()
	ts, tse := s.store.ProTakeoverState()
	takeoverBlock := ts.Block(tc, now)
	if te != nil || tse != nil {
		takeoverBlock = "接管状态读取失败，暂停新增"
	}
	if reason := s.proSIWCBlock(); reason != "" {
		takeoverBlock = reason
	}
	if snap.TakeoverToken != ts.Token(tc) {
		takeoverBlock = "接管阶段已变化，等待新快照"
	}
	evalConfig := c
	if tc.Role == "provider" && ts.Taking() {
		evalConfig.Reserve = max(c.Reserve, tc.SupportMinAccounts)
	}
	if tc.Role == "receiver" && ts.Phase == "recovering" {
		evalConfig.Reserve = max(c.Reserve, tc.ReturnMinAccounts)
	}
	now = time.Now()
	d := restock.Evaluate(evalConfig, snap, tasks, limits, confirm, p.Sub2.AccountConcurrency, lead, now)
	if takeoverBlock != "" {
		d.Suggested, d.Reason = 0, takeoverBlock
	}
	d.CardProvider, d.CardSchedule = purchasePro.AutoCardProvider(), c.CardSchedule(now)
	d.ID = randomRegistrationID()
	replacementDispatchReady := false
	for i, t := range tasks {
		if t.Status == "reserved" && (d.NoRestockActive || takeoverBlock != "" || t.TakeoverToken != snap.TakeoverToken) {
			if err := s.store.CancelProRestock(t, "进入每日禁补时段，撤销未启动预留；结束后按新快照重新评估"); err != nil {
				return err
			}
			tasks[i].Status, tasks[i].ETA = "cancelled", nil
			continue
		}
		pinnedReplacement := false
		purchaseChanged := (snap.Valid && t.Plan != snap.NewPlan) || !store.SameProPurchase(t.PurchaseRoute, purchasePro.PurchaseRoute)
		if t.Status == "reserved" {
			_, _, pinned, pinErr := s.store.ProRestockReplacementPurchase(t, c, time.Now())
			if pinErr != nil && !pinned {
				return pinErr
			}
			if pinned {
				purchaseChanged = pinErr != nil
				pinnedReplacement = pinErr == nil
			}
		}
		if t.Status == "reserved" && (!c.Enabled || c.Mode != "auto" || t.Version != c.Version || t.Binding != snap.Binding || t.GroupID != snap.GroupID || purchaseChanged || (t.ForceMergeSource != "" && !c.ForceMergeEnabled)) {
			if err := s.store.CancelProRestock(t, "配置变化或暂停，撤销未启动预留"); err != nil {
				return err
			}
			tasks[i].Status, tasks[i].ETA = "cancelled", nil
		} else if t.Status == "reserved" && pinnedReplacement {
			replacementDispatchReady = true
		}
	}
	if purchaseErr == nil {
		purchaseErr = gptpay.ValidatePurchase(selectedPay)
	}
	if purchaseErr != nil {
		d.Suggested, d.Reason = 0, purchaseErr.Error()
	}
	if purchaseErr == nil && c.Enabled && c.Mode == "auto" && d.Suggested > 0 && fresh && snap.Valid && ctx.Err() == nil {
		target, e := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: p.Sub2, Secret: password})
		if e == nil {
			var reserved []restock.Task
			locked := s.tryBackgroundCardLock(&s.proCardAdmissionMu)
			if !locked {
				e = errors.New("已有银行卡准备正在执行，监控继续更新，等待下轮领取")
			} else if reason := s.proSIWCBlock(); reason != "" {
				e = errors.New(reason)
			} else {
				reserved, e = s.store.ReserveProRestock(d, target, d.Inflight)
			}
			if locked {
				s.proCardAdmissionMu.Unlock()
			}
			d.Started = len(reserved)
			tasks = append(tasks, reserved...)
			if d.Started > 0 {
				d.BatchWait = restock.WaitForBatch(snap, tasks)
			}
			if e == nil && d.Started > 0 {
				d.CardProvider = reserved[0].CardProvider
				s.queueNotification("restock", "自动补货已安排", fmt.Sprintf("监控分组：%d\n本次补货：%d 个账号\n补货卡台：%s\n触发原因：\n%s", c.GroupID, d.Started, restock.CardProviderName(d.CardProvider), restockNotificationReasons(d)))
			}
		}
		if e != nil {
			d.Reason = e.Error()
		}
	}
	// Every reserved member belongs to an already committed batch. Continue
	// that batch independently of fluctuating demand or missing money samples.
	dispatch := (purchaseErr == nil || replacementDispatchReady) && takeoverBlock == "" && c.Enabled && c.Mode == "auto" && !d.NoRestockActive && d.DemandKnown && limits.Failures < c.FailureLimit && ctx.Err() == nil
	s.proRestock.mu.Lock()
	s.proRestock.lastSnapshot = snap
	s.proRestock.liveSnapshot = snap
	s.proRestock.decision = d
	s.proRestock.tasks = append([]restock.Task{}, tasks...)
	record := force || d.Started > 0 || s.proRestock.lastReason != d.Reason || now.Sub(s.proRestock.lastRecord) >= 30*time.Second
	if record {
		s.proRestock.lastReason = d.Reason
		s.proRestock.lastRecord = now
	}
	s.proRestock.mu.Unlock()
	if snap.Valid && snap.LoadValid {
		_ = s.store.SaveProRestockSnapshot(snap)
	}
	if record {
		_ = s.store.SaveProRestockDecision(d)
		_ = s.store.PurgeProRestock(c.RetentionDays)
	}
	if resumeImports || dispatch {
		s.queueRestockPreparation(ctx, c, p, snap, resumeImports, dispatch)
	}
	return nil
}
func (s *Server) dispatchProRestock(ctx context.Context, c restock.Config, p model.ProSettings, snap restock.Snapshot) {
	tasks, e := s.store.ProRestockTasks()
	if e != nil {
		return
	}
	pay, _, e := s.store.GPTPaySettings()
	if e != nil {
		return
	}
	for _, t := range tasks {
		if t.Status != "reserved" {
			continue
		}
		_, pinnedPay, pinned, pinErr := s.store.ProRestockReplacementPurchase(t, c, time.Now())
		if pinErr != nil {
			continue
		}
		basePay := pay
		if pinned {
			basePay = pinnedPay
		}
		taskPay, routeErr := applyPurchasePay(basePay, t.PurchaseRoute)
		if routeErr != nil {
			continue
		}
		if t.Status != "reserved" || t.Version != c.Version || t.Binding != snap.Binding || t.GroupID != snap.GroupID || t.Plan != restockPlan(taskPay.Provider, taskPay.PlanCode, taskPay.Country, taskPay.CreditQuantity) {
			continue
		}
		if t.NextCardCheckAt != nil && t.NextCardCheckAt.After(time.Now()) {
			continue
		}
		profile, _, readErr := s.store.MailAccountCredential(t.Email)
		if readErr != nil {
			return
		}
		if profile.ProAuto.StopRequested {
			_ = s.store.CancelProRestock(t, "用户已停止账号流程，撤销未提交预留")
			continue
		}
		if !restockSnapshotFresh(c, snap, time.Now()) {
			return
		}
		s.proMergeMu.Lock()
		busy := s.proAutoRunning
		s.proMergeMu.Unlock()
		s.proAutoMu.Lock()
		jobs := len(s.proAutoJobs)
		closed := s.proAutoClosed
		s.proAutoMu.Unlock()
		if closed || max(busy, jobs) >= p.Concurrency || ctx.Err() != nil {
			return
		}
		current, _, _, e := s.store.ProSettings()
		if e != nil {
			return
		}
		policy := c.CardSchedule(time.Now())
		if policy.Blocked {
			return
		}
		current, _, e = s.resolveProPurchase(current, basePay, c, time.Now())
		if e != nil {
			continue
		}
		if !store.SameProPurchase(t.PurchaseRoute, current.PurchaseRoute) {
			_ = s.store.CancelProRestock(t, "分时套餐或模板变化，重新评估")
			continue
		}
		t.CardProvider, t.StrictCardProvider = current.CardProvider, current.PurchaseRoute != nil || policy.Provider != ""
		cards, e := s.automaticProCards(current, t.StrictCardProvider)
		if e != nil {
			return
		}
		card := ""
		if !current.AutoCardEnabled() {
			if len(cards) == 0 {
				t.Message = restock.CardProviderName(t.CardProvider) + "无可用卡，自动补卡未启用，等待卡片释放"
				_ = s.store.SaveProRestockTask(t)
				return
			}
			card = cards[0].ID
		}
		now := time.Now()
		if !restockSnapshotFresh(c, snap, now) || c.NoRestockReason(now) != "" {
			return
		}
		t.Status, t.Stage = "submitting", "card"
		t.NextCardCheckAt = nil
		t.SubmittedAt = &now
		t.UpdatedAt = now
		if !s.tryBackgroundCardLock(&s.proCardAdmissionMu) {
			return
		}
		if reason := s.proSIWCBlock(); reason != "" {
			s.proCardAdmissionMu.Unlock()
			return
		}
		e = s.store.ClaimProRestockDispatch(t)
		s.proCardAdmissionMu.Unlock()
		if e != nil {
			return
		}
		workCtx, finish, err := s.beginRestockStart(ctx, t.ID)
		if err != nil {
			return
		}
		s.startClaimedProRestock(workCtx, t, taskPay, card)
		finish()
	}
}

func restockSnapshotFresh(c restock.Config, snap restock.Snapshot, now time.Time) bool {
	return snap.Valid && snap.LoadValid && !snap.At.IsZero() && now.Sub(snap.At) <= time.Duration(c.FreshSeconds)*time.Second && !snap.At.After(now.Add(5*time.Second))
}
func restockObservedLead(t restock.Task) float64 {
	started := t.SubmittedAt
	if started == nil || t.ReadyAt == nil {
		return 0
	}
	return math.Max(.5, t.ReadyAt.Sub(*started).Minutes())
}

// Use the most recent observed milestone, anchored to its original timestamp.
// A stalled task must not get a new ETA merely because the monitor ran again.
func restockRemainingLead(t restock.Task, history []restock.Task, minimum int, fallback float64, now time.Time) float64 {
	remaining := fallback - now.Sub(*t.SubmittedAt).Minutes()
	key := ""
	var at time.Time
	for stage, reached := range t.Stages {
		if reached.After(at) {
			key, at = stage, reached
		}
	}
	if key == "" {
		return remaining
	}
	samples := []float64{}
	for _, prior := range history {
		if prior.ReadyAt == nil || prior.Binding != t.Binding || prior.Plan != t.Plan {
			continue
		}
		reached, ok := prior.Stages[key]
		if ok && prior.ReadyAt.After(reached) {
			samples = append(samples, prior.ReadyAt.Sub(reached).Minutes())
		}
	}
	if len(samples) < minimum {
		return remaining
	}
	sort.Float64s(samples)
	p90 := samples[min(len(samples)-1, int(math.Ceil(float64(len(samples))*.9))-1)]
	return math.Max(.25, p90) - now.Sub(at).Minutes()
}
