package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"chatgpt-space-merge/internal/workflow"
)

type proAutomationHooks struct {
	Session   func(context.Context, string, func(string, map[string]any) (any, error)) error
	Push      func(context.Context, string) (model.MailAccountProfile, error)
	Quota     func(context.Context, string, bool) (model.MailAccountProfile, error)
	Merge     func(context.Context, string) (model.MailAccountProfile, error)
	PollDelay time.Duration
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
	state.Error = state.DisplayError()
	writeAPI(w, 200, state, "")
}

func (s *Server) autoProUpdate(email, stage, status, message string) error {
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.ProAuto.Steps == nil {
			p.ProAuto.Steps = map[string]string{}
		}
		// A fresh automatic execution supersedes the manual display for this step.
		delete(p.ProManualStages, stage)
		p.ProAuto.Stage = stage
		p.ProAuto.Steps[stage] = status
		p.ProAuto.UpdatedAt = time.Now()
		if status == "failed" || status == "interrupted" {
			p.ProAuto.Status = status
			p.ProAuto.Error = message
		} else {
			p.ProAuto.Error = ""
		}
	})
	if err == nil {
		s.auditProEvent(p, stage, status, "pro_auto", message, map[string]any{"automation_id": p.ProAuto.ID, "order_id": p.ProAuto.OrderID})
	}
	return err
}

func (s *Server) startProAutomation(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	var input struct {
		CardID   string `json:"card_id"`
		PlanCode string `json:"plan_code"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil || p.ManagementScope != "pro" {
		writeAPI(w, 404, nil, "Pro 账号不存在")
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
	if p.ProMigration != nil && !resume {
		writeAPI(w, 409, nil, "迁移账号请按已保存进度使用单项按钮，完成 RT/AT 后点击继续迁移流程，不会重新自动开通")
		return
	}
	if p.ProAuto.OrderID != "" && !resume {
		writeAPI(w, 409, nil, "已有开通订单但原登录会话已结束，请查询订单并使用单项按钮处理，禁止重新付款")
		return
	}
	if p.SpaceMergedOnce && !resume {
		writeAPI(w, 409, nil, "该账号已合并过空间，不可再次自动开通")
		return
	}
	cfg, key, err := s.store.GPTPaySettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取开通配置失败")
		return
	}
	pro, subPassword, _, err := s.store.ProSettings()
	if err != nil || pro.Provider != "sub2" || pro.Sub2.URL == "" || pro.Sub2.Email == "" || subPassword == "" || len(pro.Sub2.GroupIDs) == 0 {
		writeAPI(w, 400, nil, "请先配置 Pro 的 Sub2 推送地址、登录凭证和分组")
		return
	}
	if pro.TargetAdminID == "" {
		writeAPI(w, 400, nil, "请先在 Pro 全自动配置中选择目标母号")
		return
	}
	if err = proImportedDownstreamOK(p, pro); err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	var card gptpay.Card
	var secret gptpay.CardSecret
	if !resume {
		if key == "" {
			writeAPI(w, 400, nil, "请先保存 GPTPay API Key")
			return
		}
		if input.PlanCode != cfg.PlanCode {
			writeAPI(w, 409, nil, "套餐配置已变化，请重新打开窗口")
			return
		}
		card, secret, err = s.store.GPTPayCard(input.CardID)
		if err != nil || !card.Enabled {
			writeAPI(w, 400, nil, "请选择已启用的银行卡")
			return
		}
		if err = gptpay.ValidateCard(secret, time.Now()); err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
		if !card.Available {
			writeAPI(w, 409, nil, "银行卡没有剩余开通名额")
			return
		}
		if existing, e := s.store.ActiveGPTPayOrder(email); e == nil {
			writeAPI(w, 409, existing, "该账号已有未结束订单，请先查询")
			return
		}
	}
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "账号正在执行其他操作")
		return
	}
	s.proAutoMu.Lock()
	if s.proAutoClosed || s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) {
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
	if err == nil && !resume && p.ProAuto.OrderID != "" {
		err = errors.New("已存在开通订单，请查询原订单")
	}
	reserved := false
	if err == nil && !resume {
		err = s.store.ReserveProCard(email, card.ID, r.Context().Value(proScheduledContextKey{}) == true)
		reserved = err == nil
	}
	if err == nil {
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if !resume {
				p.ProAuto = model.ProAutoState{ID: randomRegistrationID(), PlanCode: cfg.PlanCode, CardID: card.ID, QuotaUsedThreshold: pro.QuotaUsedThreshold, QuotaIntervalSeconds: pro.QuotaCheckIntervalSeconds, StartedAt: time.Now(), Steps: map[string]string{}}
			}
			p.ProAuto.Status = "running"
			if p.ProMigration != nil {
				p.ProMigration.Paused = false
				p.ProMigration.Notice = "已从保存的进度续跑"
			}
			p.ProAuto.Error = ""
			p.ProAuto.UpdatedAt = time.Now()
			p.ProAuto.NextCheckAt = nil
			if !resume {
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
	if s.proAutoJobs == nil {
		s.proAutoJobs = map[string]context.CancelFunc{}
	}
	s.proAutoJobs[email] = cancel
	s.proAutoWG.Add(1)
	s.proAutoMu.Unlock()
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
	// Share the existing Pro concurrency bound, but release it during quota waits.
	release, err := s.proMergeSlot(ctx)
	if err == nil {
		defer release()
	}
	if err == nil && !resume {
		err = s.autoProUpdate(email, "login", "running", "开始 Pro 全自动流程，建立连续登录会话")
		if err == nil {
			sessionCtx, stop := context.WithTimeout(ctx, time.Duration(cfg.SessionTimeoutMinutes)*time.Minute)
			rpc := func(method string, args map[string]any) (any, error) {
				return s.proAutoRPC(sessionCtx, email, cfg, key, card, secret, method, args)
			}
			if s.proAutoHooks != nil && s.proAutoHooks.Session != nil {
				err = s.proAutoHooks.Session(sessionCtx, email, rpc)
			} else {
				err = s.runProContinuousSession(sessionCtx, email, rpc)
			}
			stop()
		}
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
		if method == "pro_tokens" {
			stage, message = "oauth", "记录第三步获取凭证的 GPT 客户端与出口；IP 检测结果不阻断全自动流程"
		}
		s.auditProEvent(p, stage, "running", "pro_auto", message, map[string]any{
			"automation_id": p.ProAuto.ID, "session_id": p.ProAuto.ID, "client_id": args["client_id"], "phase": method,
			"initial_client_id": args["initial_client_id"], "client_consistent": args["client_consistent"], "login_exit_ip": args["login_exit_ip"],
			"expected_exit_ip": p.ProAuto.ExitIP, "exit_ip": exitIP, "ip_check_status": ipStatus, "diagnostic_only": true,
		})
	}
	if method == "pro_recharge" {
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
		order := gptpay.Order{ID: "pro-auto-" + p.ProAuto.ID, Email: email, CardID: card.ID, CardName: card.Name, CardLast4: card.Last4, PlanCode: cfg.PlanCode, Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		snap := gptpay.Snapshot{URL: cfg.URL, APIKey: key, Input: gptpay.CreateInput{PlanCode: cfg.PlanCode, CardSecret: secret, Session: session}}
		// Reserve the payment atomically before attaching its stable ID. A crash
		// between these writes still leaves an active order, preventing repayment.
		if err = s.store.CreateGPTPayOrder(order, snap); err != nil {
			return nil, err
		}
		if _, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProAuto.OrderID = order.ID
		}); err != nil {
			return nil, err
		}
		if err = s.autoProUpdate(email, "recharge", "running", "提交 GPTPay 开通订单，原登录会话保持中"); err != nil {
			return nil, err
		}
		order, err = s.submitGPTPayOrder(ctx, order, snap)
		if err != nil {
			return nil, err
		}
		for {
			if order.Status == "success" {
				break
			}
			if order.Status == "failed" {
				return nil, fmt.Errorf("GPTPay 开通失败：%s", order.Error)
			}
			if order.Remote.ID == "" {
				return nil, errors.New("GPTPay 提交结果待确认，已保留原订单；不会新建订单或继续授权")
			}
			if order.Status == "manual_review" || order.Status == "submission_unknown" {
				return nil, errors.New("GPTPay 订单需要核实，原订单已保留")
			}
			delay := 15 * time.Second
			if time.Since(order.CreatedAt) < time.Minute {
				delay = 5 * time.Second
			}
			if s.proAutoHooks != nil && s.proAutoHooks.PollDelay > 0 {
				delay = s.proAutoHooks.PollDelay
			}
			if err = waitProAuto(ctx, delay); err != nil {
				return nil, errors.New("开通等待超时或流程已停止；请查询原订单，禁止重复付款")
			}
			items, e := s.gptPayClient().Status(ctx, snap.URL, snap.APIKey, []string{order.Remote.ID})
			if e != nil {
				continue
			} // Reads can retry; creating a second order cannot.
			for _, remote := range items {
				if remote.ID == order.Remote.ID && gptpay.ValidStatus(remote.Status) {
					remote.FailureReason = gptpay.Redact(remote.FailureReason, snap)
					order.Remote = remote
					order.Status = remote.Status
					order.Error = remote.FailureReason
					order.UpdatedAt = time.Now()
					if err = s.store.UpdateGPTPayOrder(order); err != nil {
						return nil, err
					}
				}
			}
		}
		s.auditGPTPayOrder(order)
		if err = s.autoProUpdate(email, "recharge", "completed", "GPTPay 已确认开通成功"); err != nil {
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
	payload := map[string]any{"email": email, "proxy": proxy, "pickup_url": c.PickupURL, "stdio_rpc": true}
	selection.apply(payload)
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.ProxyName = lease.label
		// The live login client records the first available IP before/through login.
		p.ProAuto.ExitIP = ""
	})
	if err != nil {
		return err
	}
	s.auditProEvent(p, "login", "running", "pro_auto", "建立连续登录客户端与代理会话；从首次登录前记录出口，IP 不一致不阻断任务", map[string]any{
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
		return errors.New(firstNonEmpty(fmt.Sprint(result["error"]), "Pro 连续会话失败"))
	}
	return nil
}

func (s *Server) runProAutomationTail(ctx context.Context, email string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reconcile saved four-step results before any quota query or other
	// upstream call, including legacy rows completed by a manual operation.
	p, err := s.store.UpdateProAccount(email, nil)
	if err != nil {
		return err
	}
	if p.ProAuto.Status == "completed" {
		return nil
	}
	settings, _, _, err := s.store.ProSettings()
	if err != nil {
		return err
	}
	if settings.Provider != "sub2" {
		return errors.New("Pro 推送设置已切换，请恢复 Sub2 后续跑")
	}
	push, quota, merge := s.performProPush, s.performProQuota, s.performProMerge
	if h := s.proAutoHooks; h != nil {
		if h.Push != nil {
			push = h.Push
		}
		if h.Quota != nil {
			quota = h.Quota
		}
		if h.Merge != nil {
			merge = h.Merge
		}
	}
	if p.ProAuto.Steps["push"] != "completed" {
		if err = s.autoProUpdate(email, "push", "running", "开始推送 Sub2"); err != nil {
			return err
		}
		if _, err = push(ctx, email); err != nil {
			return err
		}
		if err = s.autoProUpdate(email, "push", "completed", "已推送 Sub2"); err != nil {
			return err
		}
	}
	p, _, err = s.store.MailAccountCredential(email)
	if err != nil {
		return err
	}
	if p.ProAuto.Steps["quota"] != "completed" && p.ProAuto.Steps["quota"] != "skipped" {
		if err = s.autoProUpdate(email, "quota", "running", "检测 7 天已用额度"); err != nil {
			return err
		}
		p, err = quota(ctx, email, false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && p.Quota7D != nil && !math.IsNaN(p.Quota7D.UsedPercent) && !math.IsInf(p.Quota7D.UsedPercent, 0) {
			if _, e := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				if p.ProAuto.ProvisionedAt == nil {
					now := time.Now()
					p.ProAuto.ProvisionedAt = &now
				}
			}); e != nil {
				return e
			}
		}
		if err != nil || p.Quota7D == nil || math.IsNaN(p.Quota7D.UsedPercent) || math.IsInf(p.Quota7D.UsedPercent, 0) || p.Quota7D.UsedPercent < p.ProAuto.QuotaUsedThreshold {
			message := ""
			if err != nil {
				message = "额度检测暂未成功，等待下次重试：" + err.Error()
			} else if p.Quota7D == nil || math.IsNaN(p.Quota7D.UsedPercent) || math.IsInf(p.Quota7D.UsedPercent, 0) {
				message = "额度检测未返回有效的 7 天额度，等待下次重试"
			}
			_, saveErr := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				next := time.Now().UTC().Add(time.Duration(max(10, p.ProAuto.QuotaIntervalSeconds)) * time.Second)
				p.ProAuto.Status = "waiting_quota"
				p.ProAuto.Steps["quota"] = "waiting"
				p.ProAuto.NextCheckAt = &next
				p.ProAuto.Error = message
				p.ProAuto.UpdatedAt = time.Now()
			})
			return saveErr
		}
		if err = s.autoProUpdate(email, "quota", "completed", "额度达到配置阈值，开始合并空间"); err != nil {
			return err
		}
	}
	if err = s.autoProUpdate(email, "merge", "running", "执行邀请、进入、合并、移出四步流程"); err != nil {
		return err
	}
	if _, err = merge(ctx, email); err != nil {
		return err
	}
	if err = s.autoProUpdate(email, "merge", "completed", "空间四步流程完成"); err != nil {
		return err
	}
	_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.Status = "completed"
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
	if s.proAutoClosed || s.proAutoJobs[email] != nil {
		return
	}
	v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil || (p.ProMigration != nil && p.ProMigration.Paused) || p.ProAuto.Status != "waiting_quota" || p.ProAuto.NextCheckAt == nil || p.ProAuto.NextCheckAt.After(time.Now()) {
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
	s.proAutoMu.Unlock()
	s.proAutoWG.Wait()
}
func (s *Server) stopProAutomation(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(r.PathValue("email"))
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	if cancel := s.proAutoJobs[email]; cancel != nil {
		cancel()
		writeAPI(w, 202, nil, "")
		return
	}
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.Status = "stopped"
		p.ProAuto.NextCheckAt = nil
		p.ProAuto.Error = "用户已停止全自动流程"
	})
	if err != nil {
		writeAPI(w, 404, nil, err.Error())
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
			if strings.Contains(email, "@") && action != "auto-pro" {
				s.proAutoMu.Lock()
				running := s.proAutoJobs[strings.ToLower(email)] != nil
				if !running && len(parts) >= 5 && parts[1] == "mail" && (action == "login" || action == "oauth") {
					// Hold the startup gate until the asynchronous login is registered.
					defer s.proAutoMu.Unlock()
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
