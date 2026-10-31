package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/nexovcc"
	"chatgpt-space-merge/internal/workflow"
)

type proAutomationHooks struct {
	BeforePaymentReserve func()
	PaymentReserved      func()
	BeforePaymentSubmit  func()
	Session              func(context.Context, string, func(string, map[string]any) (any, error)) error
	Push                 func(context.Context, string) (model.MailAccountProfile, error)
	Quota                func(context.Context, string, bool) (model.MailAccountProfile, error)
	Merge                func(context.Context, string) (model.MailAccountProfile, error)
	PollDelay            time.Duration
}

func (s *Server) proAutomationRunning(email string) bool {
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	return s.proAutoJobs[strings.ToLower(email)] != nil
}

func (s *Server) proLoginAlreadyRunning(email string) bool {
	match := func(job map[string]any) bool {
		return strings.EqualFold(fmt.Sprint(job["email"]), email) && (job["status"] == "running" || job["status"] == "queued")
	}
	s.registrationMu.RLock()
	for _, job := range s.registrationJobs {
		if match(job) {
			s.registrationMu.RUnlock()
			return true
		}
	}
	s.registrationMu.RUnlock()
	s.oauthMu.RLock()
	defer s.oauthMu.RUnlock()
	for _, job := range s.oauthJobs {
		if match(job) {
			return true
		}
	}
	return false
}
func (s *Server) getProAutomation(w http.ResponseWriter, r *http.Request) {
	p, _, err := s.store.MailAccountCredential(r.PathValue("email"))
	if err != nil || p.ManagementScope != "pro" {
		writeAPI(w, 404, nil, "Pro 账号不存在")
		return
	}
	state := p.ProAuto
	progress := p.ProStages()
	paymentStatus := ""
	paymentRetryBlocked := false
	paymentRetryHint := ""
	if p.ProAuto.OrderID != "" && p.ProAuto.Status != "running" {
		if order, _, e := s.store.GPTPayOrder(p.ProAuto.OrderID); e == nil && order.Email == p.Email {
			paymentStatus = order.Status
			// Known paid orders may still resume OAuth/push; this flag only blocks
			// another purchase after an explicit supplier retry prohibition.
			paymentRetryBlocked, paymentRetryHint = order.Status == "failed" && order.RetryBlocked(), order.Remote.RetryHint
			payment := proPaymentStage(order)
			progress.Steps["recharge"], progress.Errors["recharge"] = payment.Status, payment.Error
		}
	}
	p.ApplyProStageOverrides(&progress)
	state.Error = progress.Errors[state.Stage]
	restockRetryAvailable := false
	if state.ID == "" && state.OrderID == "" && p.ProRestockTaskID != "" {
		if task, e := s.store.ProRestockTask(p.ProRestockTaskID); e == nil && ((task.CardProvider == "nexovcc" && task.Status == "attention") || task.Status == "failed" || task.StoppedAt != nil) {
			restockRetryAvailable = true
			state.Error = task.Message
		}
	}
	writeAPI(w, 200, struct {
		model.ProAutoState
		StageProgress         model.ProStageDisplay  `json:"stage_progress"`
		PaymentStatus         string                 `json:"payment_status"`
		PaymentRetryBlocked   bool                   `json:"payment_retry_blocked"`
		PaymentRetryHint      string                 `json:"payment_retry_hint"`
		RestockRetryAvailable bool                   `json:"restock_retry_available"`
		QuotaResumeAvailable  bool                   `json:"quota_resume_available"`
		MergeV2               *model.ProMergeV2      `json:"pro_merge_v2,omitempty"`
		Merchant              *model.ProMerchant     `json:"pro_merchant,omitempty"`
		Deactivation          *model.ProDeactivation `json:"pro_deactivation,omitempty"`
		WorkflowRunning       bool                   `json:"pro_workflow_running"`
	}{state, progress, paymentStatus, paymentRetryBlocked, paymentRetryHint, restockRetryAvailable, p.CanResumeProQuota() && paymentStatus == "success", p.ProMergeV2, p.ProMerchant, p.ProDeactivation, p.ProWorkflowRunning}, "")
}

func (s *Server) autoProUpdate(email, stage, status, message string) error {
	shouldNotify := false
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.ProAuto.ID != "" && p.ProAuto.Steps[stage] != status {
			shouldNotify = stage == "push" && status == "completed" ||
				status == "failed" && p.ProAuto.Steps["push"] != "completed" &&
					(stage == "login" || stage == "recharge" || stage == "oauth" || stage == "push")
		}
		if p.ProAuto.Steps == nil {
			p.ProAuto.Steps = map[string]string{}
		}
		// A fresh automatic execution supersedes the manual display for this step.
		delete(p.ProManualStages, stage)
		delete(p.ProStageOverrides, stage)
		p.ProAuto.Stage = stage
		p.ProAuto.Steps[stage] = status
		p.ProAuto.UpdatedAt = time.Now()
		if status == "failed" || status == "interrupted" || stage == "login" && status == "running" {
			p.ProAuto.RetryAt = nil
		}
		if p.ProAuto.StageTimes == nil {
			p.ProAuto.StageTimes = map[string]time.Time{}
		}
		if _, ok := p.ProAuto.StageTimes[stage+":"+status]; !ok {
			p.ProAuto.StageTimes[stage+":"+status] = p.ProAuto.UpdatedAt
		}
		if status == "failed" || status == "interrupted" {
			p.ProAuto.Status = status
			p.ProAuto.Error = message
		} else {
			p.ProAuto.Error = ""
		}
	})
	if err == nil {
		s.auditProEvent(p, stage, status, "pro_auto", message, map[string]any{"automation_id": p.ProAuto.ID, "order_id": p.ProAuto.OrderID})
		if shouldNotify {
			s.notifyProAutomation(p, stage, status)
		}
	}
	return err
}

func (s *Server) startProAutomation(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	var input struct {
		ResumeQuota    bool    `json:"resume_quota"`
		ExpectedFlowID string  `json:"expected_flow_id"`
		StopVersion    *uint64 `json:"expected_stop_version"`
		RetryCurrent   bool    `json:"retry_current"`
		CardID         string  `json:"card_id"`
		CardProvider   string  `json:"card_provider"`
		PlanCode       string  `json:"plan_code"`
		Provider       string  `json:"provider"`
		Country        *string `json:"country"`
		CreditQuantity *int    `json:"credit_quantity"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil || p.ManagementScope != "pro" {
		writeAPI(w, 404, nil, "Pro 账号不存在")
		return
	}
	if p.IsProDead() {
		writeAPI(w, 409, nil, errProAccountDead.Error())
		return
	}
	if replacement := p.ProAuto.Replacement; replacement != nil {
		if replacement.NextEmail != "" || replacement.Pending || (p.ProAuto.Stage == "recharge" && p.ProAuto.Status == "failed") {
			writeAPI(w, 409, nil, firstNonEmpty(replacement.Message, "第二步失败后仅换号，不再重试原账号"))
			return
		}
		if replacement.Queued {
			root, _, e := s.store.MailAccountCredential(replacement.RootEmail)
			if e != nil || root.ProAuto.StopRequested {
				writeAPI(w, 409, nil, "换号流程已停止")
				return
			}
		}
	}
	if p.MergeV2Started() {
		writeAPI(w, 409, nil, "该账号已进入合并空间2.0，请从该流程续跑")
		return
	}
	automatic := r.Context().Value(proRestockContextKey{}) != nil || r.Context().Value(proScheduledContextKey{}) == true
	if p.MerchantStarted() {
		writeAPI(w, 409, nil, "该账号已进入 API 商户流程，请从商户状态续跑")
		return
	}
	if p.DeletionSubmitted() {
		writeAPI(w, 409, nil, "该账号已提交删除，请使用执行或续跑删除账户，不会重新开通或付款")
		return
	}
	if !automatic {
		r = r.WithContext(nexovcc.WithImmediateRequests(r.Context()))
	}
	if input.ResumeQuota {
		if automatic || input.ExpectedFlowID != p.ProAuto.ID || input.StopVersion == nil || *input.StopVersion != p.ProAuto.StopVersion {
			writeAPI(w, 409, nil, "账号停止状态或流程已变化，请刷新后重试")
			return
		}
		s.resumeProQuota(w, r, p)
		return
	}
	if input.RetryCurrent && !automatic && p.CanResumeProQuota() {
		s.resumeProQuota(w, r, p)
		return
	}
	s.proAutoMu.Lock()
	_, running := s.proAutoJobs[email]
	s.proAutoMu.Unlock()
	if running {
		writeAPI(w, 202, p, "")
		return
	}
	if p.ProAuto.Status == "completed" {
		writeAPI(w, 409, nil, "全自动流程已经完成，不会重复开通")
		return
	}
	resume := p.ProAuto.Steps["oauth"] == "completed"
	paidRecovery := input.RetryCurrent && p.ProAuto.OrderID != "" && !resume
	restartFailed := false
	recoveryOrderID := p.ProAuto.OrderID
	recoveryFlowID := p.ProAuto.ID
	replacementOwner := proReplacementOwner(p)
	stopVersion := p.ProAuto.StopVersion
	wasStopped := p.ProAuto.StopRequested
	if automatic && p.ProAuto.StopRequested {
		writeAPI(w, 409, nil, "用户已停止该账号流程，未自动启动")
		return
	}
	if !resume && !paidRecovery && !input.RetryCurrent && p.ProRestockTaskID != "" && r.Context().Value(proRestockContextKey{}) != p.ProRestockTaskID {
		writeAPI(w, 409, nil, "账号已由 Pro 自动补货领取，请在补货记录中核验状态")
		return
	}
	if p.ProMigration != nil && !resume && !paidRecovery && !input.RetryCurrent {
		writeAPI(w, 409, nil, "迁移账号请按已保存进度使用单项按钮，完成 RT/AT 后点击继续迁移流程，不会重新自动开通")
		return
	}
	if p.ProAuto.OrderID != "" && !resume && !paidRecovery {
		writeAPI(w, 409, nil, "已有开通订单但原登录会话已结束，请查询订单并使用单项按钮处理，禁止重新付款")
		return
	}
	if p.SpaceMergedOnce && !resume {
		writeAPI(w, 409, nil, "该账号已合并过空间，不可再次自动开通")
		return
	}
	cfg, key, err := s.store.ProRechargePay(p.ProAuto.Replacement)
	if err != nil {
		writeAPI(w, 500, nil, "读取开通配置失败")
		return
	}
	pro, subPassword, _, err := s.proAccountPushSettings(p)
	if err == nil && automatic {
		ctx := r.Context()
		if replacement := p.ProAuto.Replacement; replacement != nil && replacement.PurchaseRoute != nil {
			ctx = context.WithValue(ctx, proPurchaseContextKey{}, replacement.PurchaseRoute)
		}
		ctx, cfg, err = s.purchaseContext(ctx, pro, cfg)
		if err == nil {
			r = r.WithContext(ctx)
		}
	}
	if err == nil && !automatic {
		provider := input.CardProvider
		if provider == "" {
			provider = pro.AutoCardProvider()
		}
		if provider != "ejh" && provider != "nexovcc" {
			writeAPI(w, 400, nil, "请选择 EJH 或环球卡台")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), proManualCardProviderKey{}, provider))
	}
	if err == nil {
		pro, _, err = s.restockCardSettings(r.Context(), pro)
	}
	if err == nil {
		err = s.validateProAutoPush(p, pro, subPassword)
	}
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if (pro.Disposition() == "merge" || pro.Disposition() == "merge_v2") && pro.TargetAdminID == "" {
		writeAPI(w, 400, nil, "请先在 Pro 全自动配置中选择目标母号")
		return
	}
	if err = proImportedDownstreamOK(p, pro); err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	var card gptpay.Card
	var secret gptpay.CardSecret
	pushTargetID, pushGroupIDs := p.ProRestockTargetID, p.ProAuto.Sub2PushGroupIDs
	if automatic && !resume && !paidRecovery && pro.PurchaseRoute != nil && len(pro.PurchaseRoute.Sub2GroupIDs) > 0 {
		pushTargetID, err = s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: pro.Sub2, Secret: subPassword})
		if err != nil {
			writeAPI(w, 500, nil, "保存分时推送目标失败，未启动开通")
			return
		}
		pushGroupIDs = append([]int64(nil), pro.PurchaseRoute.Sub2GroupIDs...)
	}

	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "账号正在执行其他操作")
		return
	}
	reserved := false
	if paidRecovery {
		s.proAutoMu.Lock()
		closed := s.proAutoClosed
		s.proAutoMu.Unlock()
		latest, _, readErr := s.store.MailAccountCredential(email)
		if closed || readErr != nil || latest.ProAuto.ID != recoveryFlowID || latest.ProAuto.OrderID != recoveryOrderID || latest.ProAuto.Status == "completed" || latest.ProWorkflowRunning || latest.SpaceMergedOnce || latest.ProAuto.StopVersion != stopVersion || s.proLoginAlreadyRunning(email) || s.proAutomationRunning(email) {
			lock.Unlock()
			writeAPI(w, 409, nil, "账号进度已变化，请刷新后重试")
			return
		}
		order, snapshot, readErr := s.store.GPTPayOrder(recoveryOrderID)
		if readErr != nil || order.Email != email {
			lock.Unlock()
			writeAPI(w, 409, nil, "无法读取原订单，未重新付款")
			return
		}
		if order.Active() {
			if order.Remote.ID != "" {
				order, readErr = s.syncGPTPayOrderStatus(r.Context(), order, snapshot)
			} else {
				order, readErr = s.submitGPTPayOrder(context.WithoutCancel(r.Context()), order, snapshot)
			}
		}
		if readErr != nil || (order.Status != "success" && order.Status != "failed") {
			s.updateProPaymentStage(order)
			message := fmt.Sprintf("原订单状态为 %s，尚未确认开通成功：%s；已保留原订单，未创建新付款", order.Status, order.Error)
			if readErr != nil {
				message = "原订单核查失败：" + gptpay.Redact(readErr.Error(), snapshot)
			}
			lock.Unlock()
			writeAPI(w, 409, nil, message)
			return
		}
		if order.Status == "failed" {
			paidRecovery, restartFailed = false, true
			if input.CardID == "" && !pro.AutoCardEnabled() {
				input.CardID = order.CardID
			}
		}
	}
	if !resume && !paidRecovery {
		if input.RetryCurrent {
			if input.PlanCode == "" {
				input.PlanCode = cfg.PlanCode
			}
			if input.Provider == "" {
				input.Provider = cfg.Provider
			}
			if input.CardID == "" && !pro.AutoCardEnabled() {
				input.CardID = p.ProAuto.CardID
			}
		}
		if err = s.validateGPTPayNewPurchase(email, cfg); err != nil {
			lock.Unlock()
			writeAPI(w, 409, nil, err.Error())
			return
		}
		if key == "" {
			lock.Unlock()
			writeAPI(w, 400, nil, "请先保存 GPTPay API Key")
			return
		}
		if input.PlanCode != cfg.PlanCode || gptpay.Provider(input.Provider) != cfg.Provider ||
			(input.Country != nil && strings.ToUpper(strings.TrimSpace(*input.Country)) != cfg.Country) ||
			(input.CreditQuantity != nil && *input.CreditQuantity != cfg.CreditQuantity) {
			lock.Unlock()
			writeAPI(w, 409, nil, "供应商、套餐、国家或点数配置已变化，请重新打开窗口")
			return
		}
		if input.CardID == "" && !pro.AutoCardEnabled() {
			lock.Unlock()
			writeAPI(w, 400, nil, "请选择银行卡，或启用自动补卡")
			return
		}
		if !automatic {
			var pinned *model.ProPurchaseRoute
			if p.ProAuto.Replacement != nil {
				pinned = p.ProAuto.Replacement.PurchaseRoute
			}
			ctx, selected, e := s.manualProPurchase(r.Context(), pro, cfg, pro.AutoCardProvider(), pinned)
			if e != nil {
				lock.Unlock()
				writeAPI(w, 409, nil, e.Error())
				return
			}
			pro, r = selected, r.WithContext(ctx)
		}
	}
	if !resume && !paidRecovery {
		s.proAutoMu.Lock()
		closed := s.proAutoClosed
		s.proAutoMu.Unlock()
		latest, _, checkErr := s.store.MailAccountCredential(email)
		if closed || checkErr != nil || latest.ProAuto.ID != recoveryFlowID || proReplacementOwner(latest) != replacementOwner || latest.ProAuto.StopVersion != stopVersion || (latest.ProAuto.StopRequested && (automatic || !wasStopped)) || latest.ProWorkflowRunning || latest.ProAuto.OrderID != recoveryOrderID || latest.SpaceMergedOnce || s.proAutomationRunning(email) || s.proLoginAlreadyRunning(email) {
			lock.Unlock()
			writeAPI(w, 409, nil, "账号已有任务或已开通，未购买新卡")
			return
		}
		if _, e := s.store.ActiveGPTPayOrder(email); e == nil {
			lock.Unlock()
			writeAPI(w, 409, nil, "账号已有未结束订单，未购买新卡")
			return
		} else if !errors.Is(e, sql.ErrNoRows) {
			lock.Unlock()
			writeAPI(w, 500, nil, "无法核实原订单，未启动流程或购买新卡")
			return
		}
		if latest.ProRestockTaskID != "" && input.RetryCurrent {
			if latest.ProAuto.ID == "" {
				task, taskErr := s.store.ProRestockTask(latest.ProRestockTaskID)
				if taskErr != nil {
					lock.Unlock()
					writeAPI(w, 500, nil, "读取原补货任务失败")
					return
				}
				if task.CardProvider == "nexovcc" {
					if err = s.recoverNexovccOpenForProRetry(r.Context()); err != nil {
						lock.Unlock()
						writeAPI(w, 409, nil, err.Error())
						return
					}
				}
			}
			if !s.proRestock.tick.TryLock() {
				lock.Unlock()
				writeAPI(w, 409, nil, "补货记录正在更新，请稍后重试")
				return
			}
			defer s.proRestock.tick.Unlock()
			if err = s.store.DetachFailedProRestock(email, latest.ProAuto.ID, recoveryOrderID); err != nil {
				lock.Unlock()
				writeAPI(w, 409, nil, err.Error())
				return
			}
			latest, _, err = s.store.MailAccountCredential(email)
			if err != nil {
				lock.Unlock()
				writeAPI(w, 500, nil, "读取原补货推送分组失败，未继续开通")
				return
			}
			pushTargetID, pushGroupIDs = latest.ProRestockTargetID, latest.ProAuto.Sub2PushGroupIDs
		}
		card, secret, err = s.reserveProStartCard(context.WithValue(r.Context(), proPaymentConfigKey{}, cfg), email, input.CardID, pro.AutoCardEnabled(), r.Context().Value(proScheduledContextKey{}) == true, func() error {
			latest, _, err := s.store.MailAccountCredential(email)
			if err != nil {
				return err
			}
			if proReplacementOwner(latest) != replacementOwner || latest.ProAuto.StopVersion != stopVersion || (latest.ProAuto.StopRequested && (automatic || !wasStopped)) {
				return errors.New("用户已停止该账号流程，未购买新卡")
			}
			return nil
		})
		if err != nil {
			lock.Unlock()
			var preparationWait *proCardPreparationWait
			if errors.As(err, &preparationWait) {
				w.Header().Set("X-Pro-Card-Wait", "preparation")
				s.auditProEvent(p, "card", "waiting", "pro_auto", err.Error(), nil)
				writeAPI(w, 409, nil, err.Error())
				return
			}
			var waiting *nexovccCapacityWaitError
			if errors.As(err, &waiting) || strings.Contains(err.Error(), nexovcc.StoppedMessage) {
				w.Header().Set("X-Pro-Card-Wait", "nexovcc")
				s.auditProEvent(p, "login", "waiting", "pro_auto", err.Error(), nil)
				writeAPI(w, 409, nil, err.Error())
				return
			}
			s.auditProEvent(p, "login", "failed", "pro_auto", "全自动准备银行卡失败："+err.Error(), nil)
			writeAPI(w, 409, nil, err.Error())
			return
		}
		reserved = true
	}
	s.proAutoMu.Lock()
	if s.proAutoClosed || s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) {
		if reserved {
			_ = s.store.ReleaseProCard(email)
		}
		s.proAutoMu.Unlock()
		lock.Unlock()
		writeAPI(w, 409, nil, "账号已有登录或自动任务，或服务正在停止，请稍后重试")
		return
	}
	// Re-read after the lock: another starter may have completed while we validated.
	p, _, err = s.store.MailAccountCredential(email)
	if err == nil && p.ProWorkflowRunning {
		err = errors.New("该账号已在执行空间合并，请完成后再操作")
	}
	if err == nil && p.ProAuto.Status == "completed" {
		err = errors.New("全自动流程已经完成")
	}
	if err == nil && p.ProAuto.ID != recoveryFlowID {
		err = errors.New("账号流程已变化，请刷新后重试")
	}
	if err == nil && proReplacementOwner(p) != replacementOwner {
		err = errors.New("账号已由另一条换号流程领取，请刷新后重试")
	}
	if err == nil && !resume && !paidRecovery && p.ProAuto.OrderID != recoveryOrderID {
		err = errors.New("已存在开通订单，请查询原订单")
	}
	if err == nil && paidRecovery && p.ProAuto.OrderID != recoveryOrderID {
		err = errors.New("原订单已变化，请刷新后重试")
	}
	if err == nil && restartFailed {
		old, _, readErr := s.store.GPTPayOrder(recoveryOrderID)
		if readErr != nil || old.Email != email || old.Status != "failed" {
			err = errors.New("原订单状态已变化，请刷新后重试")
		}
	}
	// A stop received during card issuance must win over this older start.
	// Versioning also detects a second stop while explicitly resuming a stopped account.
	if err == nil && (p.ProAuto.StopVersion != stopVersion || (p.ProAuto.StopRequested && (automatic || !wasStopped))) {
		err = errors.New("用户已停止该账号流程，本次启动已取消；已发出的开卡请求请在卡片记录中核对")
	}
	// An explicit continuation after stopping one restock is new manual work.
	// Keep the cancelled intent for audit, but expose the resumed workflow to
	// the monitor as external in-flight work until it is delivered.
	detachStopped := false
	if err == nil && !automatic && p.ProRestockTaskID != "" {
		task, readErr := s.store.ProRestockTask(p.ProRestockTaskID)
		if readErr != nil {
			err = readErr
		} else {
			detachStopped = task.StoppedAt != nil
		}
	}
	if err == nil {
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if detachStopped {
				p.ProRestockTaskID = ""
			}
			if !resume && !paidRecovery {
				if p.ProPushStartedAt == nil && p.ProAuto.ID == "" {
					p.CaptureProPushTimeout(pro)
				}
				if input.RetryCurrent {
					p.ProManualStages = nil
					p.ProStageOverrides = nil
				}
				replacement := p.ProAuto.Replacement
				if replacement == nil {
					replacement = &model.ProRechargeReplacement{RootEmail: email, Limit: pro.RechargeReplacementCount, FailoverLimit: pro.RechargeFailoverCount, PrimaryProvider: cfg.Provider, BackupProvider: cfg.BackupProvider, Payment: model.RechargePayment(cfg), Scheduled: r.Context().Value(proScheduledContextKey{}) == true, CardProvider: pro.AutoCardProvider(), PurchaseRoute: pro.PurchaseRoute}
				}
				if replacement.PurchaseRoute == nil {
					replacement.PurchaseRoute = pro.PurchaseRoute
				}
				replacement.Queued = false
				p.ProAuto = model.ProAutoState{Replacement: replacement, ID: randomRegistrationID(), PlanCode: cfg.PlanCode, CardID: card.ID, QuotaUsedThreshold: pro.QuotaUsedThreshold, QuotaIntervalSeconds: pro.QuotaCheckIntervalSeconds, StartedAt: time.Now(), Steps: map[string]string{}}
				p.ProAuto.StopVersion = stopVersion
				p.ProAuto.QuotaPlan = restockPlan(cfg.Provider, cfg.PlanCode, cfg.Country, cfg.CreditQuantity)
				p.ProAuto.RestockBinding = restockBinding(pro)
				p.ProRestockTargetID, p.ProAuto.Sub2PushGroupIDs = pushTargetID, pushGroupIDs
			}
			p.ProAuto.Status = "running"
			p.ProAuto.StopRequested = false
			if p.ProMigration != nil {
				p.ProMigration.Paused = false
				p.ProMigration.Notice = "已从保存的进度续跑"
			}
			p.ProAuto.Error = ""
			p.ProAuto.UpdatedAt = time.Now()
			p.ProAuto.NextCheckAt = nil
			if p.ProAuto.Steps == nil {
				p.ProAuto.Steps = map[string]string{}
			}
			if paidRecovery {
				p.ProAuto.Steps["login"] = "completed"
				p.ProAuto.Steps["recharge"] = "completed"
				p.ProAuto.Stage, p.ProAuto.Steps["oauth"] = "oauth", "pending"
				p.ProAuto.RetryAt = nil
				for _, stage := range []string{"login", "recharge", "oauth"} {
					delete(p.ProManualStages, stage)
					delete(p.ProStageOverrides, stage)
				}
			}
			if !resume && !paidRecovery {
				p.ProAuto.Stage = "login"
				p.ProAuto.Steps["login"] = "pending"
			}
			if p.TargetAdminID == "" {
				p.TargetAdminID = pro.TargetAdminID
				p.TargetSeatType = pro.TargetSeatType
			}
		})
	}
	if err != nil {
		if reserved {
			_ = s.store.ReleaseProCard(email)
		}
		s.proAutoMu.Unlock()
		lock.Unlock()
		writeAPI(w, 409, nil, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if paidRecovery {
		ctx = context.WithValue(ctx, proPaidRecoveryKey{}, recoveryOrderID)
	}
	if s.proAutoJobs == nil {
		s.proAutoJobs = map[string]context.CancelFunc{}
	}
	s.proAutoJobs[email] = cancel
	s.proAutoWG.Add(1)
	s.proAutoMu.Unlock()
	if restartFailed {
		s.auditProEvent(p, "login", "pending", "pro_auto", "原订单确认失败，用户从登录开始重新执行全自动流程", map[string]any{"previous_order_id": recoveryOrderID, "previous_automation_id": recoveryFlowID, "automation_id": p.ProAuto.ID})
	}
	go s.runProAutomation(ctx, email, lock.Unlock, cancel, resume, cfg, key, card, secret)
	writeAPI(w, 202, p, "")
}

func (s *Server) runProAutomation(ctx context.Context, email string, unlock, cancel func(), resume bool, cfg gptpay.Settings, key string, card gptpay.Card, secret gptpay.CardSecret) {
	defer s.proAutoWG.Done()
	defer cancel()
	defer func() {
		s.proAutoMu.Lock()
		_ = s.store.ReleaseProCard(email)
		delete(s.proAutoJobs, email)
		unlock()
		s.proAutoMu.Unlock()
	}()
	var err error
	// Bound the active chain from login onward; persisted quota waits release it.
	// The four-step merge additionally acquires its own independent limit.
	release, err := s.proAutoSlot(ctx)
	if err == nil {
		defer release()
	}
	if err == nil && !resume {
		err = s.runProProvisioning(ctx, email, cfg, key, card, secret)
	}
	if err == nil {
		p, _, e := s.store.MailAccountCredential(email)
		err = e
		if err == nil && p.ProAuto.Steps["oauth"] != "completed" {
			err = errors.New("连续会话没有保存开通后的新 RT/AT，停止后续步骤")
		}
	}
	if err == nil {
		err = s.runProAutomationTail(ctx, email)
	}
	if err != nil {
		p, _, _ := s.store.MailAccountCredential(email)
		stage := p.ProAuto.Stage
		if stage == "" {
			stage = "login"
		}
		status := "failed"
		if ctx.Err() != nil {
			status = "interrupted"
		}
		if p.ProAuto.Steps["oauth"] == "completed" && stage == "oauth" {
			stage = "push"
		}
		_ = s.autoProUpdate(email, stage, status, err.Error())
		if status == "failed" && stage == "recharge" {
			if e := s.store.QueueProRechargeReplacement(email, p.ProAuto.ID); e != nil {
				s.auditProEvent(p, "recharge", "failed", "pro_auto", "记录换号任务失败："+e.Error(), nil)
			}
		}
	}
}

func (s *Server) proAutoRPC(ctx context.Context, email string, cfg gptpay.Settings, key string, card gptpay.Card, secret gptpay.CardSecret, method string, args map[string]any) (any, error) {
	p, c, err := s.store.MailAccountCredential(email)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if method == "pro_recharge" || method == "pro_tokens" {
		exitIP, _ := args["exit_ip"].(string)
		baseline, _ := args["expected_exit_ip"].(string)
		if p.ProAuto.ExitIP == "" && net.ParseIP(baseline) != nil {
			if updated, e := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = baseline }); e == nil {
				p = updated
			}
		}
		expected, actual := net.ParseIP(strings.TrimSpace(p.ProAuto.ExitIP)), net.ParseIP(strings.TrimSpace(exitIP))
		ipStatus := "unavailable"
		if expected != nil && actual != nil {
			ipStatus = "changed"
			if expected.Equal(actual) {
				ipStatus = "consistent"
			}
		}
		stage, message := "login", "记录第一步登录的 GPT 客户端与出口；供应商开通不参与一致性对比"
		if method == "pro_tokens" || isProPaidRecovery(ctx) {
			stage, message = "oauth", "记录第三步获取凭证的 GPT 客户端与出口；IP 检测结果不阻断全自动流程"
		}
		s.auditProEvent(p, stage, "running", "pro_auto", message, map[string]any{
			"automation_id": p.ProAuto.ID, "session_id": p.ProAuto.ID, "client_id": args["client_id"], "phase": method,
			"initial_client_id": args["initial_client_id"], "client_consistent": args["client_consistent"], "login_exit_ip": args["login_exit_ip"],
			"expected_exit_ip": p.ProAuto.ExitIP, "exit_ip": exitIP, "ip_check_status": ipStatus, "diagnostic_only": true,
		})
	}
	if method == "pro_recharge" {
		if orderID, _ := ctx.Value(proPaidRecoveryKey{}).(string); orderID != "" {
			if p.ProAuto.OrderID != orderID {
				return nil, errors.New("恢复任务与原订单不匹配")
			}
			return s.confirmProPaidSession(p, args)
		}
		if taskID, _ := ctx.Value(proRestockRecoveryKey{}).(string); taskID != "" {
			return s.confirmRestockPaidSession(ctx, p, taskID, args)
		}
		if p.ProAuto.OrderID != "" {
			return nil, errors.New("本次自动流程已提交过订单，禁止重新创建")
		}
		sessionMap, _ := args["session"].(map[string]any)
		at, _ := sessionMap["access_token"].(string)
		if at == "" {
			return nil, errors.New("首次登录缺少 AT")
		}
		raw, err := json.Marshal(sessionMap)
		if err != nil {
			return nil, errors.New("首次登录 Session 无法保存")
		}
		c.AccessToken = at
		c.ChatGPTSession = string(raw)
		if _, err = s.store.SaveMailAccount(p, c); err != nil {
			return nil, err
		}
		if _, err = s.store.UpdateMailAccountATStatus(email, time.Now(), true, 200, "Pro 自动登录 AT 已保存"); err != nil {
			return nil, err
		}
		if err = s.autoProUpdate(email, "login", "completed", "首次登录成功，持续保留会话和代理"); err != nil {
			return nil, err
		}
		// Persist the successful login even if purchase preparation later fails.
		if err = s.autoProUpdate(email, "recharge", "running", "AT 和 Session 已保存，准备开通 Pro"); err != nil {
			return nil, err
		}
		currentCard, _, cardErr := s.store.GPTPayCard(card.ID)
		if cardErr != nil || !currentCard.Enabled {
			return nil, errors.New("所选银行卡已被禁用或删除，未提交开通订单")
		}
		session, err := gptPaySession(p, c)
		if err != nil {
			return nil, err
		}
		if err = validateGPTPaySession(cfg.Provider, session); err != nil {
			return nil, err
		}
		if err = gptpay.ValidatePlanSession(cfg, session); err != nil {
			return nil, err
		}
		order := gptpay.Order{Provider: cfg.Provider, Country: cfg.Country, CreditQuantity: cfg.CreditQuantity, ID: p.ProAuto.PaymentOrderID(), Email: email, CardID: card.ID, CardName: card.Name, CardLast4: card.Last4, PlanCode: cfg.PlanCode, Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		snap := gptpay.NewSnapshot(cfg, key, secret, session)
		order, err = s.submitProAutoPayment(ctx, p.ProAuto, order, snap)
		if err != nil {
			return nil, err
		}
		for {
			if order.RateLimited {
				if !order.CanRetryRateLimit() {
					return nil, errors.New(order.Error)
				}
				if err = waitProAuto(ctx, max(time.Second, time.Until(*order.NextStatusCheckAt))); err != nil {
					return nil, err
				}
				order, err = s.submitGPTPayOrder(ctx, order, snap)
				if err != nil {
					return nil, err
				}
				continue
			}
			if order.Status == "success" {
				break
			}
			if order.Status == "failed" {
				return nil, fmt.Errorf("供应商开通失败：%s", order.Error)
			}
			if order.Remote.ID == "" {
				return nil, errors.New("GPTPay 提交结果待确认，已保留原订单；不会新建订单或继续授权")
			}
			if order.Status == "manual_review" || order.Status == "submission_unknown" {
				return nil, fmt.Errorf("GPTPay 订单需要核实，原订单已保留：%s", order.Error)
			}
			delay := 15 * time.Second
			if time.Since(order.CreatedAt) < time.Minute {
				delay = 5 * time.Second
			}
			if s.proAutoHooks != nil && s.proAutoHooks.PollDelay > 0 {
				delay = s.proAutoHooks.PollDelay
			}
			if snap.Provider == gptpay.XUPay && order.NextStatusCheckAt != nil {
				delay = max(delay, time.Until(*order.NextStatusCheckAt))
			}
			if err = waitProAuto(ctx, delay); err != nil {
				return nil, errors.New("开通等待超时或流程已停止；请查询原订单，禁止重复付款")
			}
			if snap.Provider == gptpay.XUPay {
				order, err = s.syncGPTPayOrderStatus(ctx, order, snap)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			items, e := s.gptPayClient().StatusForProvider(ctx, snap.Provider, snap.URL, snap.APIKey, []string{order.Remote.ID})
			if e != nil {
				continue
			} // Reads can retry; creating a second order cannot.
			for _, remote := range items {
				if remote.ID == order.Remote.ID && gptpay.ValidStatus(remote.Status) {
					remote.FailureReason = gptpay.Redact(remote.FailureReason, snap)
					remote.RetryHint = gptpay.Redact(remote.RetryHint, snap)
					order.Remote = remote
					order.Status = remote.Status
					order.Error = remote.FailureReason
					order.UpdatedAt = time.Now()
					if err = s.store.UpdateGPTPayOrder(order); err != nil {
						return nil, err
					}
					s.auditGPTPayOrder(order)
				}
			}
		}
		s.auditGPTPayOrder(order)
		if err = s.autoProUpdate(email, "recharge", "completed", "供应商已确认开通成功"); err != nil {
			return nil, err
		}
		if err = s.autoProUpdate(email, "oauth", "running", "沿用首次登录会话获取开通后的新 RT/AT"); err != nil {
			return nil, err
		}
		return map[string]any{"status": "success"}, nil
	}
	if method == "pro_tokens" {
		if p.ProAuto.Steps["recharge"] != "completed" {
			return nil, errors.New("开通尚未成功，拒绝推进 OAuth 阶段")
		}
		tokens, _ := args["tokens"].(map[string]any)
		if tokens["auth_mode"] == "siwc" {
			var siwc model.SIWCCredentials
			raw, e := json.Marshal(tokens)
			if e == nil {
				e = json.Unmarshal(raw, &siwc)
			}
			if e == nil {
				_, paidSnapshot, readErr := s.store.GPTPayOrder(p.ProAuto.OrderID)
				info, tokenErr := workflow.DecodeUserInfo(siwc.WebAccessToken)
				if readErr != nil || tokenErr != nil || !strings.EqualFold(info.Email, email) || info.AccountID != paidSnapshot.Input.Session.Account.ID || siwc.WebAccountID != paidSnapshot.Input.Session.Account.ID {
					return nil, errors.New("SIWC 网页凭证与本次开通空间不匹配，停止推送")
				}
				e = s.store.SaveMailAccountSIWC(email, siwc)
			}
			if e != nil {
				return nil, e
			}
			return true, s.autoProUpdate(email, "oauth", "completed", "SIWC RT/AT 已保存，沿用首次登录会话完成授权")
		}
		at, _ := tokens["access_token"].(string)
		rt, _ := tokens["refresh_token"].(string)
		idToken, _ := tokens["id_token"].(string)
		if at == "" || rt == "" {
			return nil, errors.New("开通后缺少新 RT/AT")
		}
		info, err := workflow.DecodeUserInfo(at)
		if err != nil || !strings.EqualFold(info.Email, email) {
			return nil, errors.New("开通后的 AT 账号与当前账号不匹配")
		}
		_, paidSnapshot, readErr := s.store.GPTPayOrder(p.ProAuto.OrderID)
		if readErr != nil || info.AccountID != paidSnapshot.Input.Session.Account.ID {
			return nil, errors.New("新 AT 所属空间不是本次开通的账号，停止推送")
		}
		webSession := ""
		if web, ok := args["web_session"].(map[string]any); ok && len(web) > 0 {
			raw, encodeErr := json.Marshal(web)
			if encodeErr != nil {
				return nil, errors.New("开通后的 Session 无法保存")
			}
			webSession = string(raw)
		}
		expiry, _ := workflow.AccessTokenExpiry(at)
		if err = s.store.SaveMailAccountOAuthSessionBundle(email, at, rt, idToken, info.AccountID, info.UserID, expiry, webSession); err != nil {
			return nil, err
		}
		if _, err = s.store.UpdateMailAccountATStatus(email, time.Now(), true, 200, "Pro 开通后新 AT 已保存"); err != nil {
			return nil, err
		}
		return true, s.autoProUpdate(email, "oauth", "completed", "新 RT/AT 和 Session 已保存，连续登录会话可以释放")
	}
	return nil, fmt.Errorf("不支持的 Pro 连续会话消息：%s", method)
}

func (s *Server) runProContinuousSession(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
	lease, err := s.acquireOAuthProxy()
	if err != nil {
		return err
	}
	defer lease.Release()
	proxy, err := workflow.PrepareIPRoyalOAuthProxy(rotateOAuthProxySession(lease.url, 1, 1))
	if err != nil {
		return err
	}
	_, c, err := s.store.MailAccountCredential(email)
	if err != nil {
		return err
	}
	selection := selectOAuthLogin(s.store.AutoRotationSettings().OAuthLoginMode, c)
	payload := map[string]any{"email": email, "proxy": proxy, "pickup_url": c.PickupURL, "stdio_rpc": true, "paid_recovery": isProPaidRecovery(ctx)}
	profile, _, e := s.store.MailAccountCredential(email)
	if e != nil {
		return e
	}
	push, _, _, e := s.proAccountPushSettings(profile)
	if e != nil {
		return e
	}
	if push.Sub2.AuthMode == "siwc" {
		payload["credential_mode"], payload["siwc_host_id"] = "siwc", s.store.MailSIWCHostID(email, c)
	}
	selection.apply(payload)
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.ProxyName = lease.label
		// The live login client records the first available IP before/through login.
		p.ProAuto.ExitIP = ""
	})
	if err != nil {
		return err
	}
	stage, message := "login", "建立连续登录客户端与代理会话；从首次登录前记录出口，IP 不一致不阻断任务"
	if isProPaidRecovery(ctx) {
		stage, message = "oauth", "为原账号建立恢复授权会话；原订单已成功，不重新开通或付款"
	}
	s.auditProEvent(p, stage, "running", "pro_auto", message, map[string]any{
		"automation_id": p.ProAuto.ID, "session_id": p.ProAuto.ID, "proxy_name": lease.label, "diagnostic_only": true,
	})
	python := workflow.FindPython()
	script := workflow.FindInternalScript("protocol_pro_auto.py")
	if python == "" || script == "" {
		return errors.New("Pro 连续会话 Python 运行环境不完整")
	}
	cmd := exec.CommandContext(ctx, python, script)
	cmd.Dir, _ = os.Getwd()
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	localRPC := &oauthProtocolRPC{server: s, ctx: ctx, email: email}
	defer localRPC.close()
	result, err := runOAuthChild(ctx, cmd, payload, func(method string, args map[string]any) (any, error) {
		if strings.HasPrefix(method, "pro_") {
			return rpc(method, args)
		}
		return localRPC.call(method, args)
	}, func(line string) {
		if event, ok := parseProtocolOAuthDiagnostic(line); ok {
			p, _, e := s.store.MailAccountCredential(email)
			if e != nil {
				return
			}
			if baseline, _ := event.Details["expected_exit_ip"].(string); p.ProAuto.ExitIP == "" && net.ParseIP(baseline) != nil {
				// Logs still record the observation if its persistence fails.
				if updated, e := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					if p.ProAuto.ExitIP == "" {
						p.ProAuto.ExitIP = baseline
					}
				}); e == nil {
					p = updated
				}
			}
			s.auditProEvent(p, p.ProAuto.Stage, "running", "pro_auto", event.Message, map[string]any{"automation_id": p.ProAuto.ID, "session_id": p.ProAuto.ID, "proxy_name": lease.label, "diagnostic": event})
		}
	})
	if err != nil {
		return err
	}
	if result["success"] != true {
		if isDeadOAuthResult(result, fmt.Sprint(result["error"])) {
			if p, e := s.markProDead(email, "login", fmt.Sprint(result["error"])); e != nil {
				return e
			} else if p.IsProDead() {
				return errProAccountDead
			}
		}
		return errors.New(firstNonEmpty(fmt.Sprint(result["error"]), "Pro 连续会话失败"))
	}
	return nil
}

func (s *Server) runProAutomationTail(ctx context.Context, email string) (runErr error) {
	defer func() {
		if errors.Is(runErr, errProAutoFinalStepPaused) {
			runErr = s.waitProAutoFinalStepResume(email)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reconcile saved four-step results before any quota query or other
	// upstream call, including legacy rows completed by a manual operation.
	p, err := s.store.UpdateProAccount(email, nil)
	if err != nil {
		return err
	}
	if p.IsProDead() {
		return errProAccountDead
	}
	if p.DeletionConfirmed() || p.ProAuto.Status == "completed" {
		return nil
	}
	if p.DispositionStarted() {
		_, err = s.performProDisposition(ctx, email)
		return err
	}
	settings, _, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return err
	}
	if settings.Provider != "sub2" && settings.Provider != "vm2api" {
		return errors.New("Pro 全自动需使用 Sub2 或 vm2api 推送设置")
	}
	push, quota, merge := s.performProPush, s.performProQuota, s.performProDisposition
	if h := s.proAutoHooks; h != nil {
		if h.Push != nil {
			push = h.Push
		}
		if h.Quota != nil {
			quota = h.Quota
		}
		if h.Merge != nil && settings.Disposition() == "merge" {
			merge = h.Merge
		}
	}
	if p.ProAuto.Steps["push"] != "completed" {
		if err = s.autoProUpdate(email, "push", "running", "开始推送 "+settings.Provider); err != nil {
			return err
		}
		if _, err = push(ctx, email); err != nil {
			return err
		}
		if err = s.autoProUpdate(email, "push", "completed", "已推送 "+settings.Provider); err != nil {
			return err
		}
	}
	p, _, err = s.store.MailAccountCredential(email)
	if err != nil {
		return err
	}
	forceSettings, _, _, forceErr := s.store.ProSettings()
	if forceErr != nil {
		return forceErr
	}
	forceProfile := p
	forceProfile.ProWorkflowRunning = false
	forceReason := s.forcedMergeReason(forceProfile, forceSettings, time.Now())
	finalStepPaused := forceSettings.AutoDeletePaused
	if finalStepPaused {
		p.ProAuto.Steps["quota"] = "waiting"
	}
	if (p.ProMergeTrigger == "push_timeout" || p.ProMergeTrigger == "daily_ejh") && p.ProAuto.Steps["quota"] == "skipped" && forceReason == "" {
		if err = s.autoProUpdate(email, "quota", "waiting", "超时条件已关闭或不可执行，恢复额度检测"); err != nil {
			return err
		}
		p.ProAuto.Steps["quota"] = "waiting"
	}
	if forceReason != "" && p.ProAuto.Steps["quota"] != "completed" {
		if _, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			recordForcedMergeAttempt(p, forceReason, forceSettings)
		}); err != nil {
			return err
		}
		message := "推送超时，跳过额度等待"
		if forceReason == "daily_ejh" {
			message = "每日 EJH 定点合并，跳过额度等待"
		}
		if err = s.autoProUpdate(email, "quota", "skipped", message); err != nil {
			return err
		}
		p.ProAuto.Steps["quota"] = "skipped"
	}
	if p.ProAuto.Steps["quota"] != "completed" && p.ProAuto.Steps["quota"] != "skipped" {
		if err = s.autoProUpdate(email, "quota", "running", "检测 7 天已用额度"); err != nil {
			return err
		}
		p, err = quota(ctx, email, false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && validProQuotaWindow(p.Quota7D) {
			if _, e := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				if p.ProAuto.ProvisionedAt == nil {
					now := time.Now()
					p.ProAuto.ProvisionedAt = &now
				}
			}); e != nil {
				return e
			}
		}
		quotaSettings, _, _, quotaSettingsErr := s.proAccountPushSettings(p)
		if err == nil && quotaSettingsErr != nil {
			err = quotaSettingsErr
		}
		if err != nil || finalStepPaused || !s.proQuotaExitReached(p, quotaSettings, p.ProAuto.QuotaUsedThreshold) {
			message := ""
			if err != nil {
				message = "额度检测暂未成功，等待下次重试：" + err.Error()
			} else if !validProQuotaWindow(p.Quota7D) {
				message = "额度检测未返回有效的 7 天额度，等待下次重试"
			}
			_, saveErr := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				interval := p.ProAuto.QuotaIntervalSeconds
				if quotaSettings.Sub2.AuthMode == "siwc" {
					// Resolved before UpdateProAccount acquires the store lock.
					interval = quotaSettings.Sub2.QuotaCheckIntervalSeconds
				}
				next := time.Now().UTC().Add(time.Duration(max(10, interval)) * time.Second)
				if deadline := p.ProPushDeadline(); deadline != nil {
					due := *deadline
					if due.After(time.Now()) && due.Before(next) {
						next = due
					}
				}
				p.ProAuto.Status = "waiting_quota"
				p.ProAuto.Steps["quota"] = "waiting"
				p.ProAuto.NextCheckAt = &next
				p.ProAuto.Error = message
				p.ProAuto.UpdatedAt = time.Now()
			})
			return saveErr
		}
		if err = s.autoProUpdate(email, "quota", "completed", "额度达到配置阈值，开始"+forceSettings.DispositionLabel()); err != nil {
			return err
		}
	}
	action := forceSettings.Disposition()
	merge = s.performProMerge
	if action == "merge_v2" {
		merge = s.performProMergeV2
	} else if action == "merchant" {
		merge = s.performProMerchant
	} else if action == "deactivate" {
		merge = s.performProDeactivation
	} else if s.proAutoHooks != nil && s.proAutoHooks.Merge != nil {
		merge = s.proAutoHooks.Merge
	}
	if err = s.autoProUpdate(email, action, "running", "执行"+forceSettings.DispositionLabel()); err != nil {
		return err
	}
	if _, err = s.performProAutomaticFinalStep(ctx, email, merge); err != nil {
		if errors.Is(err, errProAutoFinalStepPaused) {
			return err
		}
		// Preserve original merge semantics, only reschedule a safe failed tail.
		latest, _, readErr := s.store.MailAccountCredential(email)
		if readErr == nil && ctx.Err() == nil && s.forcedMergeRetry(latest, forceSettings) && !proTransferNeedsConfirmation(latest) && !latest.DispositionStarted() && !latest.ProAuto.StopRequested && latest.ProAuto.Status != "stopped" && (latest.ProMigration == nil || !latest.ProMigration.Paused) {
			_, saveErr := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto.Status = "waiting_quota"
				p.ProAuto.Steps[action] = "failed"
				p.ProAuto.Steps["quota"] = "waiting"
				next := time.Now().Add(time.Duration(max(30, forceSettings.RetryIntervalSeconds)) * time.Second)
				p.ProAuto.NextCheckAt = &next
				if p.ProMergeTrigger == "daily_ejh" && p.ProDailyMerge != nil {
					p.ProDailyMerge.NextAt = &next
				} else {
					p.ProForceMergeNextAt = &next
				}
				p.ProAuto.Error = err.Error()
			})
			return saveErr
		}
		return err
	}
	if action == "deactivate" || action == "merchant" || action == "merge_v2" {
		return nil
	}
	if err = s.autoProUpdate(email, "merge", "completed", "空间四步流程完成"); err != nil {
		return err
	}
	_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.Status = "completed"
		if p.DailyMergeMatches() {
			p.ProDailyMerge.Completed = true
		}
		p.ProAuto.Error = ""
		p.ProAuto.NextCheckAt = nil
	})
	return err
}

func waitProAuto(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Server) startProAutomationMonitor() {
	ctx, cancel := context.WithCancel(context.Background())
	s.proAutoCancel = cancel
	s.startProRestockMonitor(ctx)
	s.startProSIWCWorker(ctx)
	s.startProOAIWorker(ctx)
	s.startProDeadWorker(ctx)
	s.proAutoWG.Add(1)
	go func() {
		defer s.proAutoWG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.queueProRechargeReplacements(ctx)
				s.tickProDeactivations(ctx)
				s.tickProMerchants(ctx)
				s.tickProPausedMergeV2(ctx)
				s.tickProMergeV2Recovery(ctx)
				s.tickProSchedule(false)
				items, err := s.store.DueProAutomations(time.Now())
				if err != nil {
					continue
				}
				for _, p := range items {
					s.queueDueProAutomation(p.Email)
				}
			}
		}
	}()
}
func (s *Server) queueDueProAutomation(email string) {
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	if s.proAutoClosed || s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) {
		return
	}
	v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil || p.ProAuto.StopRequested || (p.ProMigration != nil && p.ProMigration.Paused) || p.ProAuto.Status != "waiting_quota" || p.ProAuto.NextCheckAt == nil || p.ProAuto.NextCheckAt.After(time.Now()) {
		lock.Unlock()
		return
	}
	_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "running" })
	if err != nil {
		lock.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if s.proAutoJobs == nil {
		s.proAutoJobs = map[string]context.CancelFunc{}
	}
	s.proAutoJobs[email] = cancel
	s.proAutoWG.Add(1)
	go s.runProAutomation(ctx, email, lock.Unlock, cancel, true, gptpay.Settings{}, "", gptpay.Card{}, gptpay.CardSecret{})
}
func (s *Server) stopProAutomations() {
	s.proAutoMu.Lock()
	s.proAutoClosed = true
	if s.proAutoCancel != nil {
		s.proAutoCancel()
	}
	for _, cancel := range s.proAutoJobs {
		cancel()
	}
	for _, cancel := range s.proRestockStartJobs {
		cancel()
	}
	s.proAutoMu.Unlock()
	s.proAutoWG.Wait()
}
func (s *Server) stopProAutomation(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	// Serialize with both job registries. A queued disposition either registers
	// before this stop and is cancelled, or observes the persisted stop below.
	s.proMergeMu.Lock()
	defer s.proMergeMu.Unlock()
	s.proOAIMu.Lock()
	defer s.proOAIMu.Unlock()
	chain, chainErr := s.store.StopProRechargeReplacementChain(email)
	if chainErr != nil {
		writeAPI(w, 500, nil, chainErr.Error())
		return
	}
	for _, member := range chain {
		for _, cancel := range []context.CancelFunc{s.proAutoJobs[member], s.proMergeJobs[member]} {
			if cancel != nil {
				cancel()
			}
		}
	}
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.StopVersion++
		p.ProAuto.StopRequested = true
		p.ProAuto.Status = "stopped"
		p.ProAuto.NextCheckAt = nil
		p.ProAuto.Error = "用户已停止账号流程"
		for _, v := range p.ProOAIConfirmations {
			if v == nil || v.ConfirmedAt != nil || v.Status == "disabled" || v.Status == "stopped" {
				continue
			}
			v.Generation++
			v.Status = "stopped"
			v.NextAttemptAt = nil
			v.Error = "随账号全自动停止"
			v.Step(v.Stage, "interrupted", time.Now())
		}
	})
	if err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	cancelled := false
	if cancel, ok := s.proOAIJobs.Load(email); ok {
		cancel.(*proOAIJob).Cancel()
		cancelled = true
	}
	for _, cancel := range []context.CancelFunc{s.proAutoJobs[email], s.proMergeJobs[email]} {
		if cancel != nil {
			cancel()
			cancelled = true
		}
	}
	s.auditProEvent(p, "stop", "stopped", "pro_auto", "用户停止全自动及独立合并/删除任务；已提交的上游请求无法撤回", nil)
	if cancelled {
		writeAPI(w, 202, p, "")
		return
	}
	writeAPI(w, 200, p, "")
}

// Reject conflicting single-account actions immediately instead of blocking the
// HTTP request behind the long-lived Pro account lock. Order reads stay usable.
func (s *Server) guardProAutomation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			email := ""
			action := ""
			if len(parts) >= 3 && parts[0] == "api" && parts[1] == "pro-accounts" {
				email, _ = url.PathUnescape(parts[2])
				if len(parts) > 3 {
					action = parts[3]
				}
			}
			if len(parts) >= 4 && parts[0] == "api" && parts[1] == "mail" && parts[2] == "accounts" {
				email, _ = url.PathUnescape(parts[3])
				if len(parts) > 4 {
					action = parts[4]
				}
			}
			if strings.Contains(email, "@") && action != "auto-pro" && action != "oai-confirmation" {
				s.proAutoMu.Lock()
				running := s.proAutoJobs[strings.ToLower(email)] != nil
				if !running && len(parts) >= 5 && parts[1] == "mail" && action != "final-step" {
					if p, _, err := s.store.MailAccountCredential(email); err == nil && p.MailProActions != nil {
						_, oaiRunning := s.proOAIJobs.Load(strings.ToLower(email))
						running = p.ProWorkflowRunning || oaiRunning
					}
				}
				if !running && len(parts) >= 5 && parts[1] == "mail" && (action == "login" || action == "oauth" || action == "siwc") {
					// Hold the startup gate until the asynchronous login is registered.
					defer s.proAutoMu.Unlock()
					value, _ := s.proLocks.LoadOrStore("account:"+strings.ToLower(email), &sync.Mutex{})
					lock := value.(*sync.Mutex)
					if !lock.TryLock() {
						writeAPI(w, 409, nil, "该账号正在执行操作，请稍后重试")
						return
					}
					defer lock.Unlock()
					next.ServeHTTP(w, r)
					return
				}
				s.proAutoMu.Unlock()
				if running {
					writeAPI(w, 409, nil, "该账号的 Pro 全自动流程正在执行，请先停止流程")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
