package httpapi

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/vm2api"
	"chatgpt-space-merge/internal/workflow"
)

type proAccountListItem struct {
	FinancialCases     []proFinancialCase `json:"financial_cases"`
	PushElapsedMinutes int64              `json:"push_elapsed_minutes"`
	ForceMergeDeadline *time.Time         `json:"force_merge_deadline,omitempty"`
	StateEnteredAt     *time.Time         `json:"state_entered_at,omitempty"`
	model.MailAccountProfile
	ActivationCard       *store.ProActivationCard `json:"activation_card,omitempty"`
	StageProgress        model.ProStageDisplay    `json:"pro_stage_progress"`
	QuotaResumeAvailable bool                     `json:"quota_resume_available"`
}

func (s *Server) proAccountListWithCards(items []model.MailAccountProfile) ([]proAccountListItem, error) {
	return s.proAccountListWithCardsContext(context.Background(), items)
}

func (s *Server) proAccountListWithCardsContext(ctx context.Context, items []model.MailAccountProfile) ([]proAccountListItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	emails := make([]string, 0, len(items))
	for _, item := range items {
		emails = append(emails, item.Email)
	}
	cards, err := s.store.ProActivationCards(emails)
	if err != nil {
		return nil, err
	}
	orders, err := s.store.ProLatestPaymentOrders(emails)
	if err != nil {
		return nil, err
	}
	result := make([]proAccountListItem, 0, len(items))
	cases, err := s.store.EJHCasesForEmails(emails)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	financialCases, err := s.proFinancialCases(cases)
	if err != nil {
		return nil, err
	}
	byEmail := map[string][]proFinancialCase{}
	for _, c := range financialCases {
		byEmail[strings.ToLower(c.Email)] = append(byEmail[strings.ToLower(c.Email)], c)
	}
	config, _, _, err := s.store.ProSettings()
	if err != nil {
		return nil, err
	}
	quality, err := s.store.ProQualitySettings()
	if err != nil {
		return nil, err
	}
	effectiveSettings := map[string]model.ProSettings{}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Resolve account-specific targets before displaying quality and costs.
		effective := config
		if item.ProSIWC != nil || item.ProRestockTargetID != "" {
			cacheKey := item.ProRestockTargetID
			if item.ProSIWC != nil && item.ProSIWC.AccountID > 0 && item.ProSIWC.TargetID != "" {
				cacheKey = "siwc:" + item.ProSIWC.TargetID
			}
			if item.VM2 != nil {
				cacheKey += "|vm:" + item.VM2.ConnectionID
			}
			if cached, ok := effectiveSettings[cacheKey]; ok {
				effective = cached
			} else {
				if v, _, _, e := s.proAccountPushSettings(item); e == nil {
					effective = v
				}
				effectiveSettings[cacheKey] = effective
			}
		}
		item.ProQuality = proQualityDisplay(item, quality, effective)
		if !proCostBindingMatches(item, effective) {
			item.ClearProCostSnapshot()
		}
		row := proAccountListItem{MailAccountProfile: item, StageProgress: item.ProStages()}
		if order, ok := orders[strings.ToLower(item.Email)]; ok {
			row.QuotaResumeAvailable = item.CanResumeProQuota() && order.ID == item.ProAuto.OrderID && order.Status == "success"
		}
		row.FinancialCases = byEmail[strings.ToLower(item.Email)]
		if row.FinancialCases == nil {
			row.FinancialCases = []proFinancialCase{}
		}
		if item.ProPushStartedAt != nil {
			end := time.Now()
			if stopped := item.ProPushEndTime(); stopped != nil {
				end = *stopped
				row.ProPushEndedAt = stopped
			}
			row.PushElapsedMinutes = max(0, int64(end.Sub(*item.ProPushStartedAt)/time.Minute))
			if row.ProPushEndedAt == nil && (item.SpaceMergedOnce || item.DispositionStarted()) {
				row.PushElapsedMinutes = -1 // historical stopping time was not recorded
			}
			row.ForceMergeDeadline = item.ProPushDeadline()
		}
		row.ProAuto.Error = row.ProAuto.DisplayError()
		if order, ok := orders[strings.ToLower(item.Email)]; ok && ((item.ProAuto.ID == "" && item.ProManualStages["recharge"].ID == "") || (order.ID == item.ProAuto.OrderID && item.ProAuto.Status != "running")) {
			progress := proPaymentStage(order)
			row.StageProgress.Steps["recharge"], row.StageProgress.Errors["recharge"] = progress.Status, progress.Error
		}
		item.ApplyProStageOverrides(&row.StageProgress)
		if card, ok := cards[strings.ToLower(strings.TrimSpace(item.Email))]; ok {
			row.ActivationCard = &card
		}
		result = append(result, row)
	}
	return result, nil
}

func (s *Server) listProAccounts(w http.ResponseWriter, r *http.Request) {
	if paginationRequested(r) {
		reader, err := s.store.ProListReader(r.Context())
		if err != nil {
			writeAPI(w, 500, nil, "读取 Pro 账号失败")
			return
		}
		defer reader.Close()
		// Only pure local display helpers run against this read-only view.
		view := &Server{store: reader}
		page := view.parsePagination(r)
		items, total, summary, err := reader.ProAccountsPageContext(r.Context(), r.URL.Query().Get("query"), r.URL.Query().Get("merge_state"), page.Limit, page.Offset)
		if err != nil {
			writeAPI(w, http.StatusInternalServerError, nil, "读取 Pro 账号失败: "+err.Error())
			return
		}
		profiles := make([]model.MailAccountProfile, len(items))
		for i, item := range items {
			profiles[i] = item.MailAccountProfile
		}
		rows, err := view.proAccountListWithCardsContext(r.Context(), profiles)
		if err != nil {
			writeAPI(w, 500, nil, "读取开通银行卡失败")
			return
		}
		for i := range rows {
			rows[i].StateEnteredAt = items[i].StateEnteredAt
		}
		cfg, _, _, err := reader.ProSettings()
		if err != nil {
			writeAPI(w, 500, nil, "读取全自动最后一步状态失败")
			return
		}
		writeProAccountList(w, r, paginatedData(rows, total, page, map[string]any{"summary": summary, "auto_delete_paused": cfg.AutoDeletePaused}))
		return
	}
	items := s.store.MailAccountsByManagementScope("pro")
	if items == nil {
		items = []model.MailAccountProfile{}
	}
	rows, err := s.proAccountListWithCardsContext(r.Context(), items)
	if err != nil {
		writeAPI(w, 500, nil, "读取开通银行卡失败")
		return
	}
	writeProAccountList(w, r, rows)
}

func (s *Server) checkProAccountPlan(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	result, profile, err := s.runProAccountPlanCheck(r.Context(), email)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"account": profile, "result": result}, "")
}

func (s *Server) checkProAccountPlans(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emails []string `json:"emails"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	emails, err := normalizeProAccountEmails(input.Emails)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	workerCount := s.store.Settings().Concurrency
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > 4 {
		workerCount = 4
	}
	type itemResult struct {
		Email   string                       `json:"email"`
		Result  model.AccountPlanCheckResult `json:"result"`
		Account model.MailAccountProfile     `json:"account,omitempty"`
		Error   string                       `json:"error,omitempty"`
	}
	jobs := make(chan string)
	results := make(chan itemResult, len(emails))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for email := range jobs {
				s.proAutoMu.Lock()
				autoRunning := s.proAutoJobs[strings.ToLower(email)] != nil
				s.proAutoMu.Unlock()
				if autoRunning {
					results <- itemResult{Email: email, Error: "Pro 全自动流程正在执行"}
					continue
				}
				result, profile, checkErr := s.runProAccountPlanCheck(r.Context(), email)
				item := itemResult{Email: email, Result: result, Account: profile}
				if checkErr != nil {
					item.Error = checkErr.Error()
				}
				results <- item
			}
		}()
	}
	go func() {
		for _, email := range emails {
			jobs <- email
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	items := make([]itemResult, 0, len(emails))
	succeeded := 0
	for item := range results {
		items = append(items, item)
		if item.Error == "" && item.Result.OK {
			succeeded++
		}
	}
	writeAPI(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "succeeded": succeeded, "failed": len(items) - succeeded}, "")
}

func (s *Server) runProAccountPlanCheck(ctx context.Context, email string) (model.AccountPlanCheckResult, model.MailAccountProfile, error) {
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		return model.AccountPlanCheckResult{}, model.MailAccountProfile{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(profile.ManagementScope), "pro") {
		return model.AccountPlanCheckResult{}, model.MailAccountProfile{}, errors.New("账号不在 Pro 管理中")
	}
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return model.AccountPlanCheckResult{}, model.MailAccountProfile{}, errors.New("该账号尚未保存 AT")
	}
	settings := s.store.Settings()
	if strings.TrimSpace(settings.ProxyURL) == "" {
		return model.AccountPlanCheckResult{}, model.MailAccountProfile{}, errors.New("请先在接口设置中配置全局代理；OpenAI 套餐识别禁止直连")
	}
	client, err := workflow.NewClient(settings)
	if err != nil {
		return model.AccountPlanCheckResult{}, model.MailAccountProfile{}, err
	}
	if err = s.waitForProPlanCheckSlot(ctx); err != nil {
		return model.AccountPlanCheckResult{}, model.MailAccountProfile{}, err
	}
	result := client.CheckAccountPlan(ctx, credentials.AccessToken)
	updated, updateErr := s.store.UpdateMailAccountPlanCheck(email, result)
	if updateErr != nil {
		return result, model.MailAccountProfile{}, updateErr
	}
	return result, updated, nil
}

func (s *Server) waitForProPlanCheckSlot(ctx context.Context) error {
	now := time.Now()
	s.planCheckMu.Lock()
	startAt := now
	if s.planCheckNext.After(startAt) {
		startAt = s.planCheckNext
	}
	var random [2]byte
	_, _ = cryptorand.Read(random[:])
	jitter := time.Duration(binary.BigEndian.Uint16(random[:])%301) * time.Millisecond
	s.planCheckNext = startAt.Add(400*time.Millisecond + jitter)
	s.planCheckMu.Unlock()
	if wait := time.Until(startAt); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func normalizeProAccountEmails(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 500 {
		return nil, errors.New("请选择 1 到 500 个 Pro 账号")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		email := strings.ToLower(strings.TrimSpace(value))
		if !strings.Contains(email, "@") {
			return nil, fmt.Errorf("邮箱地址无效：%s", value)
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		result = append(result, email)
	}
	return result, nil
}

func (s *Server) updateMailAccountManagementScope(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if !strings.Contains(email, "@") {
		writeAPI(w, http.StatusBadRequest, nil, "邮箱地址无效")
		return
	}
	var input struct {
		Scope string `json:"scope"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope != "mail" && input.Scope != "pro" {
		writeAPI(w, http.StatusBadRequest, nil, "账号归属只能是 mail 或 pro")
		return
	}
	if input.Scope == "pro" {
		_, _, err := s.store.MailAccountCredential(email)
		if err != nil {
			writeAPI(w, http.StatusNotFound, nil, err.Error())
			return
		}
	}
	profile, err := s.store.UpdateMailAccountManagementScope(email, input.Scope)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func (s *Server) proAccountAccessTokens(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emails []string `json:"emails"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	if len(input.Emails) == 0 || len(input.Emails) > 500 {
		writeAPI(w, http.StatusBadRequest, nil, "请选择 1 到 500 个 Pro 账号")
		return
	}
	seen := make(map[string]struct{}, len(input.Emails))
	items := make([]map[string]string, 0, len(input.Emails))
	for _, rawEmail := range input.Emails {
		email := strings.ToLower(strings.TrimSpace(rawEmail))
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		profile, credentials, err := s.store.MailAccountCredential(email)
		if err != nil {
			writeAPI(w, http.StatusBadRequest, nil, fmt.Sprintf("%s：%s", email, err))
			return
		}
		if !strings.EqualFold(strings.TrimSpace(profile.ManagementScope), "pro") {
			writeAPI(w, http.StatusConflict, nil, fmt.Sprintf("%s 不在 Pro 管理中", email))
			return
		}
		accessToken := strings.TrimSpace(credentials.AccessToken)
		if accessToken == "" {
			writeAPI(w, http.StatusConflict, nil, fmt.Sprintf("%s 尚未保存 AT", email))
			return
		}
		items = append(items, map[string]string{"email": profile.Email, "access_token": accessToken})
	}
	if len(items) == 0 {
		writeAPI(w, http.StatusBadRequest, nil, "没有可用的 Pro 账号")
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"items": items, "total": len(items)}, "")
}

const proOAuthSessionTTL = 30 * time.Minute

func (s *Server) startProOAuth(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
		writeAPI(w, 400, nil, "请先配置全局代理；OpenAI OAuth 禁止直连")
		return
	}
	target := strings.ToLower(strings.TrimSpace(input.Email))
	if target != "" {
		profile, _, err := s.store.MailAccountCredential(target)
		if err != nil || !strings.EqualFold(profile.ManagementScope, "pro") {
			writeAPI(w, 404, nil, "要重新授权的 Pro 账号不存在")
			return
		}
	}
	auth, err := workflow.GenerateOpenAIPKCEAuthorization()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	now := time.Now()
	session := model.ProOAuthSession{ID: auth.SessionID, State: auth.State, TargetEmail: target, RedirectURI: workflow.OpenAIOAuthRedirectURI, CreatedAt: now, ExpiresAt: now.Add(proOAuthSessionTTL)}
	if err = s.store.SaveProOAuthSession(session, auth.CodeVerifier); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"session_id": auth.SessionID, "auth_url": auth.AuthorizationURL, "expires_at": session.ExpiresAt}, "")
}

func parseProOAuthCallback(rawURL, code, state string) (string, string, error) {
	rawURL, code, state = strings.TrimSpace(rawURL), strings.TrimSpace(code), strings.TrimSpace(state)
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return "", "", errors.New("回调 URL 无效")
		}
		if e := parsed.Query().Get("error"); e != "" {
			return "", "", fmt.Errorf("OpenAI OAuth 返回错误: %s", e)
		}
		if code == "" {
			code = parsed.Query().Get("code")
		}
		if state == "" {
			state = parsed.Query().Get("state")
		}
	}
	if code == "" || state == "" {
		return "", "", errors.New("请粘贴完整回调 URL，或同时填写 code 和 state")
	}
	return code, state, nil
}

func (s *Server) completeProOAuth(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SessionID   string `json:"session_id"`
		CallbackURL string `json:"callback_url"`
		Code        string `json:"code"`
		State       string `json:"state"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	session, verifier, err := s.store.ProOAuthSession(input.SessionID)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	w, finishStage := s.trackProStageResponse(w, session.TargetEmail, "oauth")
	defer finishStage()
	code, state, err := parseProOAuthCallback(input.CallbackURL, input.Code, input.State)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(session.State)) != 1 {
		writeAPI(w, 400, nil, "OAuth state 校验失败")
		return
	}
	settings := s.store.Settings()
	if strings.TrimSpace(settings.ProxyURL) == "" {
		writeAPI(w, 400, nil, "请先配置全局代理；OpenAI OAuth 禁止直连")
		return
	}
	tokens, err := workflow.ExchangeOpenAIOAuthCode(r.Context(), code, verifier, settings)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if strings.TrimSpace(tokens.RefreshToken) == "" {
		writeAPI(w, http.StatusBadRequest, nil, "OAuth 响应缺少 refresh_token，请重新生成授权链接")
		return
	}
	info, err := workflow.DecodeUserInfo(tokens.AccessToken)
	if err != nil {
		writeAPI(w, 400, nil, "OAuth AT 无效: "+err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if session.TargetEmail != "" && !strings.EqualFold(session.TargetEmail, email) {
		writeAPI(w, 409, nil, "登录账号与要重新授权的 Pro 账号不一致")
		return
	}
	if email == "" {
		writeAPI(w, 400, nil, "OAuth 凭据缺少邮箱")
		return
	}
	if session.TargetEmail == "" {
		if _, _, existingErr := s.store.MailAccountCredential(email); existingErr != nil {
			profile := model.MailAccountProfile{Email: email, Label: email, Group: "pro", ManagementScope: "pro", CurrentPlanType: info.PlanType}
			credentials := model.MailAccountCredentials{Email: email, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken}
			if _, saveErr := s.store.SaveMailAccount(profile, credentials); saveErr != nil {
				writeAPI(w, 400, nil, saveErr.Error())
				return
			}
		}
		if _, scopeErr := s.store.UpdateMailAccountManagementScope(email, "pro"); scopeErr != nil {
			writeAPI(w, 500, nil, scopeErr.Error())
			return
		}
	}
	if err = s.store.SaveMailAccountOAuthBundle(email, tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, info.AccountID, info.UserID, tokens.ExpiresAt); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	profile, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.CurrentPlanType = info.PlanType
		if p.InviteStatus == "" {
			p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "not_started", "not_started", "not_started", "not_started"
		}
	})
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if err = s.syncProVMAfterOAuth(r.Context(), email); err != nil {
		writeAPI(w, 400, nil, "凭证已保存，vm2api 重新授权失败："+err.Error())
		return
	}
	profile, _, _ = s.store.MailAccountCredential(email)
	_ = s.store.DeleteProOAuthSession(input.SessionID)
	writeAPI(w, 200, profile, "")
}

func (s *Server) currentProCredential(ctx context.Context, email string) (model.MailAccountProfile, model.MailAccountCredentials, error) {
	return s.currentProCredentialForPush(ctx, email, nil)
}

func (s *Server) currentProCredentialForPush(ctx context.Context, email string, push *model.ProSettings) (model.MailAccountProfile, model.MailAccountCredentials, error) {
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, credentials, err
	}
	if !profile.ProActionsAllowed() {
		return profile, credentials, errors.New("账号不在 Pro 管理中")
	}
	settings, _, _, settingsErr := s.proAccountPushSettings(profile)
	if push != nil {
		settings, settingsErr = *push, nil
	}
	if settingsErr == nil && settings.Provider == "sub2" && settings.Sub2.AuthMode == "siwc" {
		tokens, e := s.ensureProSIWC(ctx, profile, credentials)
		if e != nil {
			return profile, credentials, e
		}
		info, e := workflow.DecodeUserInfo(tokens.WebAccessToken)
		if e != nil || !strings.EqualFold(info.Email, email) {
			return profile, credentials, errors.New("SIWC 网页凭证账号不匹配")
		}
		credentials.AccessToken = tokens.WebAccessToken
		profile.OAuthAccountID, profile.OAuthUserID = info.AccountID, info.UserID
		return profile, credentials, nil
	}
	if credentials.AccessToken == "" || credentials.RefreshToken == "" {
		return profile, credentials, errors.New("账号缺少完整 OAuth AT/RT")
	}
	shouldRefresh := profile.OAuthExpiresAt != nil && profile.OAuthExpiresAt.Before(time.Now().Add(5*time.Minute))
	if !shouldRefresh {
		if expires, ok := workflow.AccessTokenExpiry(credentials.AccessToken); ok {
			shouldRefresh = expires.Before(time.Now().Add(5 * time.Minute))
		}
	}
	if !shouldRefresh {
		if profile.OAuthAccountID == "" || profile.OAuthUserID == "" {
			if info, decodeErr := workflow.DecodeUserInfo(credentials.AccessToken); decodeErr == nil {
				profile, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.OAuthAccountID, p.OAuthUserID = info.AccountID, info.UserID
				})
			}
		}
		return profile, credentials, nil
	}
	global := s.store.Settings()
	if strings.TrimSpace(global.ProxyURL) == "" {
		return profile, credentials, errors.New("请先配置全局代理；OpenAI OAuth 刷新禁止直连")
	}
	tokens, err := workflow.RefreshOAuthTokens(ctx, credentials.RefreshToken, global)
	if err != nil {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.OAuthStatus, p.ProLastError = "reauthorize_required", err.Error() })
		return profile, credentials, err
	}
	info, err := workflow.DecodeUserInfo(tokens.AccessToken)
	if err != nil {
		return profile, credentials, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = credentials.RefreshToken
	}
	if tokens.IDToken == "" {
		tokens.IDToken = credentials.IDToken
	}
	if err = s.store.SaveMailAccountOAuthBundle(email, tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, info.AccountID, info.UserID, tokens.ExpiresAt); err != nil {
		return profile, credentials, err
	}
	return s.store.MailAccountCredential(email)
}

func (s *Server) refreshProOAuth(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	w, finishStage := s.trackProStageResponse(w, email, "oauth")
	defer finishStage()
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	if credentials.RefreshToken == "" {
		writeAPI(w, 409, nil, "账号没有 RT，请重新授权")
		return
	}
	settings := s.store.Settings()
	if strings.TrimSpace(settings.ProxyURL) == "" {
		writeAPI(w, 400, nil, "请先配置全局代理；OpenAI OAuth 刷新禁止直连")
		return
	}
	tokens, err := workflow.RefreshOAuthTokens(r.Context(), credentials.RefreshToken, settings)
	if err != nil {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.OAuthStatus, p.ProLastError = "reauthorize_required", err.Error() })
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = credentials.RefreshToken
	}
	if tokens.IDToken == "" {
		tokens.IDToken = credentials.IDToken
	}
	info, err := workflow.DecodeUserInfo(tokens.AccessToken)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if err = s.store.SaveMailAccountOAuthBundle(email, tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, info.AccountID, info.UserID, tokens.ExpiresAt); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if err = s.syncProVMAfterOAuth(r.Context(), email); err != nil {
		writeAPI(w, 400, nil, "凭证已保存，vm2api 重新授权失败："+err.Error())
		return
	}
	profile, _, _ = s.store.MailAccountCredential(email)
	writeAPI(w, 200, profile, "")
}

type proSettingsInput struct {
	model.ProSettings
	Sub2Password string `json:"sub2_password"`
	CPAKey       string `json:"cpa_key"`
	VM2APIKey    string `json:"vm2api_key"`
}

func (s *Server) getProSettings(w http.ResponseWriter, r *http.Request) {
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取开通设置失败")
		return
	}
	defer reader.Close()
	v, _, _, err := reader.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}

func (s *Server) saveProSettings(w http.ResponseWriter, r *http.Request) {
	input := proSettingsInput{ProSettings: model.DefaultProSettings()}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	s.downstreamMu.Lock()
	defer s.downstreamMu.Unlock()
	current, subPassword, cpaKey, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if strings.TrimSpace(input.Sub2Password) == "" {
		if input.Sub2.AuthMode != current.Sub2.AuthMode {
			_, subPassword, err = s.store.ProSub2Variant(input.Sub2.AuthMode)
			if err != nil {
				writeAPI(w, 400, nil, err.Error())
				return
			}
		}
		input.Sub2Password = subPassword
	}
	if strings.TrimSpace(input.CPAKey) == "" {
		input.CPAKey = cpaKey
	}
	if input.Sub2.URL == "" {
		input.Sub2.URL = current.Sub2.URL
	}
	if input.CPA.URL == "" {
		input.CPA.URL = current.CPA.URL
	}
	normalizeVMSettings(&input.VM2API)
	if input.Provider == "vm2api" {
		_, key, readErr := s.store.ProVM2APISettings()
		if readErr != nil {
			writeAPI(w, 500, nil, readErr.Error())
			return
		}
		if strings.TrimSpace(input.VM2APIKey) != "" {
			key = strings.TrimSpace(input.VM2APIKey)
		}
		if err := vm2api.Validate(input.VM2API, key != ""); err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
	} else if input.Provider == "sub2" {
		if err := sub2.ValidateProxySettings(input.Sub2); err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
		if err := validateSub2Connection(input.Sub2.URL, input.Sub2.Email, input.Sub2Password); err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
		if len(input.Sub2.GroupIDs) == 0 {
			writeAPI(w, 400, nil, "请选择 Pro 的 Sub2 OpenAI 分组")
			return
		}
	} else if input.Provider != "cpa" || strings.TrimSpace(input.CPA.URL) == "" || strings.TrimSpace(input.CPAKey) == "" {
		writeAPI(w, 400, nil, "CPA 地址和 Management Key 不能为空")
		return
	}
	input.AutoDeleteEnabled, input.AutoMergeEnabled = current.AutoDeleteEnabled, current.AutoMergeEnabled
	input.AutoMergeV2Enabled = current.AutoMergeV2Enabled
	input.AutoMerchantEnabled, input.MerchantKeyPresent = current.AutoMerchantEnabled, current.MerchantKeyPresent
	input.MerchantSuccessFeeUSD = current.MerchantSuccessFeeUSD
	input.MerchantAPIURL = current.MerchantAPIURL
	input.MerchantProvider = current.MerchantProvider
	input.MerchantStarBridgeAPIURL = current.MerchantStarBridgeAPIURL
	input.MerchantStarBridgeKeyPresent = current.MerchantStarBridgeKeyPresent
	input.DeletionCheckIntervalSeconds = current.DeletionCheckIntervalSeconds
	input.OAIConfirmation = current.OAIConfirmation
	input.OAIConfirmationConcurrency = current.OAIConfirmationConcurrency
	input.OAIInquiry = current.OAIInquiry
	input.TargetAdminID, input.TargetSeatType = current.TargetAdminID, current.TargetSeatType
	if input.AutoMergeEnabled && strings.TrimSpace(input.TargetAdminID) == "" {
		writeAPI(w, 400, nil, "开启自动合并前请选择目标母号")
		return
	}
	previousProvider := current.Provider
	input.ScheduledEnabled, input.MaxUnmerged, input.ScheduleIntervalSeconds = current.ScheduledEnabled, current.MaxUnmerged, current.ScheduleIntervalSeconds
	input.EJHAutoOpen, input.EJHTemplateID, input.EJHDailyOpenLimit, input.EJHDailyAmountLimit = current.EJHAutoOpen, current.EJHTemplateID, current.EJHDailyOpenLimit, current.EJHDailyAmountLimit
	input.CardProvider = current.CardProvider
	input.NexovccAutoOpen, input.NexovccTemplateID, input.NexovccDailyOpenLimit, input.NexovccDailyAmountLimit = current.NexovccAutoOpen, current.NexovccTemplateID, current.NexovccDailyOpenLimit, current.NexovccDailyAmountLimit
	input.ForceMergeEnabled, input.ForceMergeMinutes = current.ForceMergeEnabled, current.ForceMergeMinutes
	input.MergeConcurrency = current.MergeConcurrency
	input.PurchaseScheduleEnabled, input.PurchaseWindows = current.PurchaseScheduleEnabled, current.PurchaseWindows
	input.EJHPlanTemplates, input.NexovccPlanTemplates = current.EJHPlanTemplates, current.NexovccPlanTemplates
	now := time.Now()
	input.LastQuotaSweepAt = current.LastQuotaSweepAt
	if input.QuotaEnabled {
		next := now.Add(time.Duration(input.QuotaCheckIntervalSeconds) * time.Second)
		input.NextQuotaSweepAt = &next
	} else {
		input.NextQuotaSweepAt = nil
	}
	saved, err := s.store.SaveProSettings(input.ProSettings, input.Sub2Password, input.CPAKey, input.VM2APIKey)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if previousProvider != saved.Provider {
		for _, account := range s.store.MailAccountsByManagementScope("pro") {
			if account.VM2 != nil {
				continue
			}
			_, _ = s.store.UpdateProAccount(account.Email, func(p *model.MailAccountProfile) {
				p.PushStatus, p.PushProvider, p.Sub2AccountID, p.Sub2AccountName, p.CPAAuthFileName = "pending", "", 0, "", ""
				p.Quota5H, p.Quota7D, p.QuotaCheckedAt = nil, nil, nil
			})
		}
	}
	writeAPI(w, 200, saved, "")
}

func (s *Server) testProSub2(w http.ResponseWriter, r *http.Request) {
	v, password, _, ok := s.proConnectionSettings(w, r)
	if !ok {
		return
	}
	groups, err := s.sub2.Groups(r.Context(), v.Sub2, password)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"connected": true, "groups": groups}, "")
}
func (s *Server) getProSub2Groups(w http.ResponseWriter, r *http.Request) {
	v, password, _, ok := s.proConnectionSettings(w, r)
	if !ok {
		return
	}
	groups, err := s.sub2.Groups(r.Context(), v.Sub2, password)
	if err == nil {
		writeAPI(w, 200, groups, "")
		return
	}
	writeAPI(w, 400, nil, err.Error())
}
func (s *Server) testProCPA(w http.ResponseWriter, r *http.Request) {
	v, _, key, ok := s.proConnectionSettings(w, r)
	if !ok {
		return
	}
	_, err := s.cpa.List(r.Context(), v.CPA, key)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"connected": true}, "")
}
func (s *Server) getProCPAGroups(w http.ResponseWriter, r *http.Request) {
	v, _, key, ok := s.proConnectionSettings(w, r)
	if !ok {
		return
	}
	groups, err := s.cpa.Groups(r.Context(), v.CPA, key)
	if err == nil {
		writeAPI(w, 200, groups, "")
		return
	}
	writeAPI(w, 400, nil, err.Error())
}

func proCPAFileName(profile model.MailAccountProfile) string {
	name := strings.NewReplacer("@", "-at-", "/", "-", "\\", "-", " ", "-").Replace(profile.Email)
	return "codex-pro-" + name + "--" + beijingNow().Format("150405") + ".json"
}

func proAccountName(profile model.MailAccountProfile) string {
	name := strings.TrimSpace(profile.Label)
	if name == "" {
		name = profile.Email
	}
	return name + "--" + beijingNow().Format("15:04")
}

func buildProCredentials(profile model.MailAccountProfile, credentials model.MailAccountCredentials) map[string]any {
	result := map[string]any{"access_token": credentials.AccessToken, "refresh_token": credentials.RefreshToken, "chatgpt_account_id": profile.OAuthAccountID, "email": profile.Email, "plan_type": "pro", "chatgpt_plan_type": "pro"}
	if credentials.IDToken != "" {
		result["id_token"] = credentials.IDToken
	}
	return result
}

func (s *Server) performProPush(ctx context.Context, email string) (out model.MailAccountProfile, retErr error) {
	return s.performProPushMode(ctx, email, false)
}

func (s *Server) performProPushMode(ctx context.Context, email string, manual bool) (out model.MailAccountProfile, retErr error) {
	finishStage := s.beginProManualStage(email, "push")
	defer func() { finishStage(retErr) }()
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, err
	}
	if manual {
		profile, err = s.prepareProManualPush(profile)
		if err != nil {
			return profile, err
		}
	}
	previous := profile
	defer func() {
		if retErr == nil || previous.ProPendingPushTargetID == "" {
			return
		}
		pending, _, _, e := s.proPendingPushSettings(previous)
		if e != nil {
			return
		}
		attempt, e := s.proHasPushAttempt(previous, pending)
		if e != nil {
			return
		}
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if p.ProPendingPushTargetID != previous.ProPendingPushTargetID {
				return
			}
			if !attempt {
				p.ProPendingPushTargetID = ""
			}
			if previous.Sub2AccountID > 0 && previous.PushStatus == "completed" {
				p.PushStatus = previous.PushStatus
			}
		})
	}()
	v, subPassword, cpaKey, err := s.proNewPushSettings(profile)
	if err != nil {
		return profile, err
	}
	if v.Provider == "sub2" && v.Sub2.AuthMode == "siwc" {
		return s.performDirectSIWCPush(ctx, profile, credentials, v, subPassword)
	}
	if v.Provider != "sub2" && profile.ProRestockTargetID != "" && profile.Sub2AccountID == 0 && profile.CPAAuthFileName == "" && profile.VM2 == nil {
		// The old reservation must not override the selected provider after
		// this new push. Submitted Sub2 attempts never reach this branch.
		profile, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProRestockTargetID = ""
			p.ProAuto.Sub2PushGroupIDs = nil
		})
		if err != nil {
			return profile, err
		}
	}
	profile, credentials, err = s.currentProCredentialForPush(ctx, email, &v)
	if err != nil {
		return profile, err
	}
	s.auditProEvent(profile, "push", "running", v.Provider, "开始推送 Pro 账号", nil)
	defer func() {
		if retErr != nil && (v.Provider == "vm2api" || profile.VM2 != nil) {
			_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.PushStatus, p.ProLastError = "failed", retErr.Error()
				if p.VM2 != nil && p.VM2.DeletedAt != nil {
					p.PushStatus = "removed"
				}
			})
		}
		status, message, details := "completed", "Pro 账号推送成功", map[string]any{"plan_type": "pro"}
		if retErr != nil {
			status, message, details = "failed", "Pro 账号推送失败", map[string]any{"error": retErr.Error()}
		}
		s.auditProEvent(firstProProfile(out, profile), "push", status, v.Provider, message, details)
	}()
	if _, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.PushStatus, p.ProLastError = "running", ""

	}); err != nil {
		return profile, err
	}
	if v.Provider == "vm2api" {
		return s.pushProVM(ctx, profile, credentials)
	}
	name := proAccountName(profile)
	if v.Provider == "cpa" {
		if strings.TrimSpace(v.CPA.URL) == "" || cpaKey == "" {
			return profile, errors.New("Pro CPA 推送设置未配置完整")
		}
		fileName := proCPAFileName(profile)
		payload := buildProCredentials(profile, credentials)
		payload["type"], payload["name"] = "codex", name
		if len(v.CPA.GroupIDs) > 0 {
			payload["group_ids"] = v.CPA.GroupIDs
		}
		encoded, _ := json.Marshal(payload)
		if err = s.cpa.Upload(ctx, v.CPA, cpaKey, fileName, encoded); err != nil {
			_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus, p.ProLastError = "failed", err.Error() })
			return profile, err
		}
		now := time.Now()
		return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.PushProvider, p.PushStatus, p.CPAAuthFileName = "cpa", "completed", fileName
			if p.ProMigration != nil {
				p.ProMigration.Downstream = model.ProDownstream(v)
			}
			p.Sub2AccountID, p.Sub2AccountName, p.ProLastError = 0, name, ""
			p.QuotaCheckedAt = nil
			p.UpdatedAt = now
		})
	}
	if len(v.Sub2.GroupIDs) == 0 {
		return profile, errors.New("请选择 Pro 的 Sub2 OpenAI 分组")
	}
	profile, groups, err := s.pinProSub2Push(profile, v, subPassword)
	if err != nil {
		return profile, err
	}
	input := sub2.CreateAccountInput{Name: name, Credentials: buildProCredentials(profile, credentials), GroupIDs: groups, Models: v.Sub2.Models, Concurrency: v.Sub2.AccountConcurrency, Priority: v.Sub2.Priority, CpaWS: v.Sub2.CpaWS}
	name = s.sub2NameWithMother(name, profile.TargetAdminID, "")
	input.Name = name
	fingerprint := sha256.Sum256([]byte(email + "|" + name + "|" + credentials.AccessToken))
	pushScope := "pro:" + profile.ID
	created, err := s.createSub2WithProxy(ctx, v.Sub2, subPassword, input, pushScope, "pro-"+profile.ID+"-"+fmt.Sprintf("%x", fingerprint[:8]), func(message string, details map[string]any) {
		s.auditProEvent(profile, "push", "running", "sub2", message, details)
	})
	if err != nil {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	out, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		completeProPushTarget(p, v)
		p.PushProvider, p.PushStatus = "sub2", "completed"
		ref := model.ProDownstream(v)
		p.ProPushDownstream = &ref
		now := time.Now()
		p.ProQualitySince, p.ProQuality = &now, model.ProQualityState{}
		if p.ProMigration != nil {
			p.ProMigration.Downstream = model.ProDownstream(v)
		}
		p.Sub2ProxyBindingID = created.ProxyBindingID
		p.Sub2AccountID, p.Sub2AccountName = created.ID, created.Name
		p.CPAAuthFileName, p.ProLastError = "", ""
		p.QuotaCheckedAt = nil
	})
	if err == nil {
		s.completeSub2Push(v.Sub2, pushScope)
		if attachErr := s.attachProPushTakeover(ctx, v, subPassword, out); attachErr != nil {
			// The account already exists. Never turn a membership retry into
			// another account creation; the monitor verifies/retries coverage.
			s.auditProEvent(out, "push", "running", "sub2", "账号已推送，接管分组等待重试", map[string]any{"error": attachErr.Error()})
		}
	}
	return out, err
}

func (s *Server) pushProAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	unlock := s.lockProAccount(email)
	defer unlock()
	p, err := s.performProPushMode(r.Context(), email, true)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, p, "")
}

func (s *Server) runProBatch(ctx context.Context, emails []string, operation func(context.Context, string) error) map[string]any {
	v, _, _, _ := s.store.ProSettings()
	workers := v.Concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(emails) {
		workers = len(emails)
	}
	type result struct{ Email, Error string }
	jobs := make(chan string)
	results := make(chan result, len(emails))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for email := range jobs {
				if s.proAutomationRunning(email) {
					results <- result{Email: email, Error: "Pro 全自动流程正在执行"}
					continue
				}
				unlock := s.lockProAccount(email)
				err := operation(ctx, email)
				unlock()
				item := result{Email: email}
				if err != nil {
					item.Error = err.Error()
				}
				results <- item
			}
		}()
	}
	go func() {
		for _, email := range emails {
			jobs <- email
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	items := []result{}
	succeeded := 0
	for item := range results {
		items = append(items, item)
		if item.Error == "" {
			succeeded++
		}
	}
	return map[string]any{"items": items, "total": len(items), "succeeded": succeeded, "failed": len(items) - succeeded}
}

func decodeProEmails(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var input struct {
		Emails []string `json:"emails"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return nil, false
	}
	emails, err := normalizeProAccountEmails(input.Emails)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return nil, false
	}
	return emails, true
}
func (s *Server) pushProAccounts(w http.ResponseWriter, r *http.Request) {
	emails, ok := decodeProEmails(w, r)
	if !ok {
		return
	}
	result := s.runProBatch(r.Context(), emails, func(ctx context.Context, email string) error {
		_, err := s.performProPushMode(ctx, email, true)
		return err
	})
	writeAPI(w, 200, result, "")
}

func (s *Server) performProQuota(ctx context.Context, email string, auto bool) (out model.MailAccountProfile, retErr error) {
	if p, _, e := s.store.MailAccountCredential(email); e == nil && p.SIWCTransferring() {
		return p, errors.New("SIWC 转入中，保留原超时，暂停额度退出判断")
	}
	finishStage := s.beginProManualStage(email, "quota")
	defer func() { finishStage(retErr) }()
	profile, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, err
	}
	v, subPassword, cpaKey, err := s.proAccountPushSettings(profile)
	if err != nil {
		return profile, err
	}
	if profile.ProVM2CleanupPending() {
		return s.finishProDownstream(ctx, profile)
	}
	if profile.PushStatus != "completed" || profile.PushProvider != v.Provider {
		return profile, errors.New("账号尚未推送到当前 Pro 下游")
	}
	if err = proImportedDownstreamOK(profile, v); err != nil {
		return profile, err
	}
	s.auditProEvent(profile, "quota", "running", v.Provider, "开始检测 Pro 额度", map[string]any{"automatic": auto})
	defer func() {
		final := firstProProfile(out, profile)
		status, message := "completed", "Pro 额度检测完成"
		details := map[string]any{"quota_5h": final.Quota5H, "quota_7d": final.Quota7D}
		if retErr != nil {
			status, message, details = "failed", "Pro 额度检测失败", map[string]any{"error": retErr.Error()}
		}
		s.auditProEvent(final, "quota", status, v.Provider, message, details)
	}()
	_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.QuotaStatus = "running"
		if !proCostBindingMatches(*p, v) {
			p.ClearProCostSnapshot()
		}
		if proTransferNeedsConfirmation(*p) {
			p.TransferStatus = "unknown"
		} else {
			p.ProLastError = ""
		}
	})
	var five, seven *model.FreeQuotaWindow
	queriedProfile := profile
	if v.Provider == "sub2" && v.Sub2.AuthMode == "siwc" {
		_, credentials, readErr := s.store.MailAccountCredential(email)
		if readErr != nil {
			return profile, readErr
		}
		needsCredentials := credentials.SIWC == nil
		if credentials.SIWC != nil {
			expiry, ok := workflow.AccessTokenExpiry(credentials.SIWC.WebAccessToken)
			needsCredentials = !ok || !expiry.After(time.Now().Add(time.Minute))
		}
		if needsCredentials {
			tokens, authErr := s.ensureProSIWC(ctx, profile, credentials)
			if authErr != nil {
				return profile, authErr
			}
			if _, authErr = s.sub2.ApplyOAuthCredentials(ctx, v.Sub2, subPassword, profile.Sub2AccountID, siwcCredentials(tokens)); authErr != nil {
				return profile, authErr
			}
		}
	}
	if v.Provider == "vm2api" {
		five, seven, err = s.proVMQuota(ctx, profile)
	} else if v.Provider == "cpa" {
		if profile.CPAAuthFileName == "" {
			return profile, errors.New("CPA auth 文件不存在")
		}
		f, se, e := s.cpa.Quota(ctx, v.CPA, cpaKey, profile.CPAAuthFileName)
		err = e
		if f.LimitWindowSeconds > 0 {
			five = &f
		}
		if se.LimitWindowSeconds > 0 {
			seven = &se
		}
	} else {
		if profile.Sub2AccountID < 1 {
			return profile, errors.New("Sub2 账号 ID 不存在")
		}
		var quota sub2.QuotaUsage
		var costs sub2.AccountCosts
		var costErr error
		var queries sync.WaitGroup
		queries.Add(2)
		go func() {
			defer queries.Done()
			quota, err = s.sub2.QueryQuota(ctx, v.Sub2, subPassword, profile.Sub2AccountID)
		}()
		go func() {
			defer queries.Done()
			costs, costErr = s.sub2.QueryTotalCosts(ctx, v.Sub2, subPassword, profile.Sub2AccountID)
		}()
		queries.Wait()
		// Cost is advisory and independent of the 5h/7d quota result.
		if costErr == nil {
			var saved model.MailAccountProfile
			saved, costErr = s.saveProCostSnapshot(profile, v, costs)
			if costErr == nil {
				profile = saved
			}
		}
		if costErr != nil {
			s.auditProEvent(profile, "cost", "failed", "sub2", "Pro 消耗额度更新失败，保留上次成功数据", map[string]any{"error": costErr.Error()})
		}
		if err == nil {
			five, seven = quota.Windows()
		}
	}
	if err != nil {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.QuotaStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	// Settings can change without taking this account's execution lock. Keep
	// validation and quota admission serialized with configuration updates.
	s.downstreamMu.Lock()
	defer s.downstreamMu.Unlock()
	current, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, err
	}
	currentSettings, _, _, err := s.proAccountPushSettings(current)
	if err != nil {
		return profile, err
	}
	if !sameProQuotaBinding(queriedProfile, current) || !model.ProDownstream(v).Matches(model.ProDownstream(currentSettings)) {
		err = errors.New("额度查询期间下游连接或账号已变化，已丢弃旧响应，请重新查询")
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.QuotaStatus, p.ProLastError = "failed", err.Error() })
		return current, err
	}
	if seven == nil {
		err = errors.New("下游未返回可识别的 7d 额度窗口")
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.QuotaStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	if !validProQuotaWindow(seven) || (five != nil && !validProQuotaWindow(five)) {
		err = errors.New("下游额度百分比无效，须为 0～100；保留原额度并停止本次自动合并")
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.QuotaStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	now := time.Now()
	applied := false
	profile, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if !sameProQuotaBinding(queriedProfile, *p) {
			return
		}
		applied = true
		p.Quota5H, p.Quota7D, p.QuotaCheckedAt = five, seven, &now
		p.QuotaStatus = "completed"
		if !proTransferNeedsConfirmation(*p) {
			p.ProLastError = ""
		}
	})
	if err == nil && !applied {
		return profile, errors.New("额度查询期间下游账号已变化，已丢弃旧响应，请重新查询")
	}
	if err == nil && auto && !currentSettings.AutoDeletePaused && (currentSettings.AutoMergeEnabled || currentSettings.AutoMergeV2Enabled || currentSettings.AutoMerchantEnabled || currentSettings.AutoDeleteEnabled) && !profile.SpaceMergedOnce && !profile.DispositionStarted() && s.proQuotaExitReached(profile, currentSettings, currentSettings.QuotaUsedThreshold) && !proTransferNeedsConfirmation(profile) {
		profile, err = s.startProDispositionJob(email, nil, nil)
		if errors.Is(err, errProAutoFinalStepPaused) {
			err = nil
		}
	}
	return profile, err
}

func (s *Server) checkProAccountQuota(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	unlock := s.lockProAccount(email)
	defer unlock()
	p, err := s.performProQuota(r.Context(), email, true)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, p, "")
}
func (s *Server) checkProAccountQuotas(w http.ResponseWriter, r *http.Request) {
	emails, ok := decodeProEmails(w, r)
	if !ok {
		return
	}
	result := s.runProBatch(r.Context(), emails, func(ctx context.Context, email string) error {
		_, err := s.performProQuota(ctx, email, true)
		return err
	})
	writeAPI(w, 200, result, "")
}

func retryProStep(ctx context.Context, attempts, delay int, operation func() error) error {
	var err error
	for n := 0; n <= attempts; n++ {
		if n > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(delay) * time.Second):
			}
		}
		if err = operation(); err == nil {
			return nil
		}
		if errors.Is(err, errProTransferUncertain) {
			return err
		}
	}
	return err
}

func (s *Server) performProMerge(ctx context.Context, email string) (out model.MailAccountProfile, retErr error) {
	if err := ctx.Err(); err != nil {
		return out, err
	}
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, err
	}
	if profile.IsProDead() {
		return profile, errProAccountDead
	}
	if !profile.ProFinalStepAllowed("merge") {
		return profile, errors.New("账号不在 Pro 管理中")
	}
	notice := &mergeNoticeState{}
	ctx = context.WithValue(ctx, mergeNoticeKey{}, notice)
	s.notifyMergeStarted(profile)
	defer func() {
		if retErr != nil && !notice.failed {
			s.notifyMergeFailure(ctx, profile, "", false, errors.Is(retErr, errProTransferUncertain))
		}
		if retErr == nil && !notice.removed {
			s.notifyMergeRemoved(ctx, firstProProfile(out, profile))
		}
	}()
	if err = s.checkProQualityMerge(ctx, profile, false); err != nil {
		return profile, err
	}
	if proTransferNeedsConfirmation(profile) {
		return profile, errProTransferUncertain
	}
	executionID := randomRegistrationID()
	v, _, _, err := s.proAccountPushSettings(profile)
	if err != nil {
		return profile, err
	}
	if err = s.endSIWCService(ctx, profile); err != nil {
		return profile, err
	}
	s.auditProEvent(profile, "space_merge", "running", v.Provider, "开始执行 Pro 空间四步流程", map[string]any{"execution_id": executionID, "state": proMergeState(profile), "configured_admin_id": v.TargetAdminID})
	defer func() {
		if retErr != nil {
			_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProLastError = retErr.Error() })
		}
		if latest, _, readErr := s.store.MailAccountCredential(email); readErr == nil {
			out = latest
		}
		status, message := "completed", "Pro 空间四步流程完成"
		details := map[string]any{"execution_id": executionID, "state": proMergeState(out)}
		if retErr != nil {
			status, message, details["error"] = "failed", "Pro 空间四步流程失败", retErr.Error()
			if errors.Is(retErr, errProTransferUncertain) {
				status, message = "unknown", "Pro 合并结果待确认，已停止重复提交及移出"
			}
		}
		s.auditProEvent(firstProProfile(out, profile), "space_merge", status, v.Provider, message, details)
	}()
	if profile.SpaceMergedOnce && (profile.RemoveStatus == "completed" || (profile.VM2 != nil && profile.RemoveStatus == "team_removed")) {
		release, err := s.proMergeSlot(ctx)
		if err != nil {
			return profile, err
		}
		defer release()
		return s.finishProDownstream(ctx, profile)
	}
	// Only a started workflow is bound to its original Team. Preselected
	// targets and fully reset workflows use the current Pro configuration.
	profile, err = s.importedProTarget(profile)
	if err != nil {
		return profile, err
	}
	pinnedTarget := proMergeTargetPinned(profile)
	if pinnedTarget && profile.TargetAdminID != "" {
		v.TargetAdminID = profile.TargetAdminID
		if profile.TargetSeatType != "" {
			v.TargetSeatType = profile.TargetSeatType
		}
	}
	if v.TargetAdminID == "" {
		return profile, errors.New("未配置目标母号")
	}
	admin, adminCreds, err := s.currentAdminCredential(ctx, v.TargetAdminID)
	if err != nil {
		return profile, err
	}
	if admin.TeamAccountID == "" {
		return profile, errors.New("目标母号缺少 Team ID")
	}
	if pinnedTarget && profile.TargetTeamID != "" && profile.TargetTeamID != admin.TeamAccountID {
		return profile, errors.New("原流程空间与母号当前空间不一致，禁止向其他空间续跑")
	}
	needsChild := profile.AcceptStatus != "completed" || profile.TransferStatus != "completed"
	if needsChild {
		s.auditProEvent(profile, "credentials", "running", v.Provider, "准备子号空间操作凭据", map[string]any{"execution_id": executionID})
		if err = s.prepareMailMergeCredential(ctx, profile, credentials); err != nil {
			return profile, err
		}
		profile, credentials, err = s.currentProCredential(ctx, email)
		if err != nil {
			return profile, err
		}
		if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
			return profile, errors.New("请先配置全局代理；子号空间操作禁止直连")
		}
	}
	if profile.OAuthUserID == "" {
		if info, decodeErr := workflow.DecodeUserInfo(credentials.AccessToken); decodeErr == nil {
			profile.OAuthUserID = info.UserID
		}
	}
	if profile.OAuthUserID == "" && profile.RemoveStatus != "completed" && profile.RemoveStatus != "team_removed" {
		return profile, errors.New("缺少子号 OpenAI 用户 ID，无法执行母号移出")
	}
	value, _ := s.autoAdminLocks.LoadOrStore(admin.TeamAccountID, &sync.Mutex{})
	unlockAdmin, err := lockProMutex(ctx, value.(*sync.Mutex))
	if err != nil {
		return profile, err
	}
	unlockAdmin = sync.OnceFunc(unlockAdmin)
	defer unlockAdmin()
	// Wait for this Team first so same-Team queues do not consume the global
	// merge allowance. Retain one slot through all four steps and downstream.
	release, err := s.proMergeSlot(ctx)
	if err != nil {
		return profile, err
	}
	defer release()
	if err = s.checkProQualityMerge(ctx, profile, true); err != nil {
		return profile, err
	}
	adminSettings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return profile, err
	}
	adminClient, err := workflow.NewClient(adminSettings)
	if err != nil {
		return profile, err
	}
	var userClient *workflow.Client
	if needsChild {
		userClient, err = workflow.NewClient(s.store.Settings())
		if err != nil {
			return profile, err
		}
	}
	userID := profile.OAuthUserID
	profile, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProWorkflowRunning, p.ProLastError = true, ""
		p.OAuthUserID = userID
		p.TargetAdminID, p.TargetTeamID, p.TargetSeatType = admin.ID, admin.TeamAccountID, v.TargetSeatType
		for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
			if proStageStatus(*p, stage) == "" {
				setProStage(p, stage, "pending")
			}
		}
	})
	if err != nil {
		return profile, err
	}
	defer func() {
		_, saveErr := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProWorkflowRunning = false
			if retErr != nil {
				p.ProLastError = retErr.Error()
			} else {
				p.ProLastError = ""
			}
		})
		if saveErr != nil {
			retErr = errors.Join(retErr, saveErr)
		}
	}()
	adminProxy := s.adminProxyLogDetails(adminSettings, admin)
	childProxy := s.proxyLogDetails(s.store.Settings(), "global")
	steps := []struct {
		name  string
		proxy map[string]any
		run   func(context.Context) (workflow.Response, error)
	}{
		{"invite", adminProxy, func(stepCtx context.Context) (workflow.Response, error) {
			return adminClient.Invite(stepCtx, adminCreds.AccessToken, admin.TeamAccountID, profile.Email, v.TargetSeatType)
		}},
		{"accept", childProxy, func(stepCtx context.Context) (workflow.Response, error) {
			return userClient.Accept(stepCtx, credentials.AccessToken, admin.TeamAccountID, profile.OAuthUserID)
		}},
		{"transfer", childProxy, func(stepCtx context.Context) (workflow.Response, error) {
			return userClient.Transfer(stepCtx, credentials.AccessToken, admin.TeamAccountID)
		}},
		{"remove", adminProxy, func(stepCtx context.Context) (workflow.Response, error) {
			return adminClient.Kick(stepCtx, adminCreds.AccessToken, admin.TeamAccountID, profile.OAuthUserID)
		}},
	}
	for _, step := range steps {
		if step.name == "remove" {
			profile, err = s.prepareProPostMergeGroups(profile, v)
			if err != nil {
				return profile, err
			}
		}
		unlockStep := func() {}
		if step.name == "remove" && profile.RemoveStatus != "completed" && profile.RemoveStatus != "team_removed" {
			value, _ := s.teamRemoveLocks.LoadOrStore(admin.TeamAccountID, &sync.Mutex{})
			unlockStep, err = lockProMutex(ctx, value.(*sync.Mutex))
			if err != nil {
				return profile, err
			}
		}
		err = s.runProMergeStep(ctx, profile, v, executionID, step.name, step.proxy, step.run)
		unlockStep()
		if err != nil {
			return profile, err
		}
		var readErr error
		profile, _, readErr = s.store.MailAccountCredential(email)
		if readErr != nil {
			return profile, readErr
		}
	}
	// Sub2/CPA stay after leaving; vm2api cleanup must complete before removal does.
	profile, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.VM2 == nil || p.VM2.DeletedAt != nil {
			p.RemoveStatus = "completed"
		}
	})
	if err != nil {
		return profile, err
	}
	// Downstream grouping must not hold up unrelated operations on this Team.
	unlockAdmin()
	return s.finishProDownstream(ctx, profile)
}

func (s *Server) mergeProAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在执行操作，请等待后台流程完成")
		return
	}
	p, err := s.startProMergeJob(email, lock.Unlock)
	if err != nil {
		lock.Unlock()
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusAccepted, p, "")
}
func (s *Server) lockProAccount(email string) func() {
	v, _ := s.proLocks.LoadOrStore("account:"+strings.ToLower(email), &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

func firstProProfile(candidate, fallback model.MailAccountProfile) model.MailAccountProfile {
	if candidate.ID != "" {
		return candidate
	}
	return fallback
}

func (s *Server) auditProEvent(profile model.MailAccountProfile, operation, status, provider, message string, details map[string]any) {
	level := "info"
	if status == "failed" {
		level = "error"
	}
	s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: profile.ID, Email: profile.Email, AdminAccountID: profile.TargetAdminID, Type: "pro_step", Source: "pro_management", Provider: provider, Operation: operation, Stage: operation, ToStatus: status, Level: level, Message: message, Details: details})
}
func (s *Server) lockProTeam(adminID string) func() {
	v, _ := s.autoAdminLocks.LoadOrStore(adminID, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

func (s *Server) monitorProAccounts(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	siwcNext := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			v, _, _, err := s.store.ProSettings()
			if err != nil || !v.QuotaEnabled {
				continue
			}
			regularDue := v.NextQuotaSweepAt == nil || !now.Before(*v.NextQuotaSweepAt)
			s.proMonitorMu.Lock()
			if s.proMonitorRunning {
				s.proMonitorMu.Unlock()
				continue
			}
			s.proMonitorRunning = true
			s.proMonitorMu.Unlock()
			if regularDue {
				last := now
				next := now.Add(time.Duration(v.QuotaCheckIntervalSeconds) * time.Second)
				_ = s.store.UpdateProQuotaSweep(last, next)
			}
			emails := []string{}
			profiles, readErr := s.store.ProQuotaCandidates(ctx)
			if readErr != nil {
				s.proMonitorMu.Lock()
				s.proMonitorRunning = false
				s.proMonitorMu.Unlock()
				continue
			}
			for _, p := range profiles {
				// The new full workflow owns its own quota threshold and due time.
				if p.MergeV2Started() || p.ProAuto.ID != "" || p.ProAuto.StopRequested || p.ProAuto.Status == "stopped" || (p.ProMigration != nil && p.ProMigration.Paused) {
					continue
				}
				needsQuota := !p.SpaceMergedOnce && !p.DispositionStarted()
				needsCleanup := p.ProVM2CleanupPending() || p.SpaceMergedOnce && p.RemoveStatus != "completed"
				if p.PushStatus == "completed" && (p.PushProvider == v.Provider || p.VM2 != nil) && !p.ProWorkflowRunning && (needsQuota || needsCleanup) {
					target, _, _, targetErr := s.proAccountPushSettings(p)
					if targetErr != nil || p.SIWCTransferring() {
						continue
					}
					if target.Provider == "sub2" && target.Sub2.AuthMode == "siwc" && needsQuota {
						if !siwcQuotaSweepDue(p, now, siwcNext[p.Email], target.Sub2.QuotaCheckIntervalSeconds) {
							continue
						}
						// Back off failed queries too; a failure must not retry every tick.
						siwcNext[p.Email] = now.Add(time.Duration(max(10, target.Sub2.QuotaCheckIntervalSeconds)) * time.Second)
					} else if !regularDue {
						continue
					}
					emails = append(emails, p.Email)
				}
			}
			go func() {
				defer func() { s.proMonitorMu.Lock(); s.proMonitorRunning = false; s.proMonitorMu.Unlock() }()
				s.runProBatch(ctx, emails, func(jobCtx context.Context, email string) error {
					profile, _, readErr := s.store.MailAccountCredential(email)
					if readErr != nil {
						return readErr
					}
					if profile.ProAuto.ID != "" || profile.ProAuto.StopRequested || profile.ProAuto.Status == "stopped" || (profile.ProMigration != nil && profile.ProMigration.Paused) {
						return nil
					}
					if profile.SpaceMergedOnce && profile.RemoveStatus != "completed" {
						_, e := s.startProDispositionJob(email, nil, nil)
						return e
					}
					_, e := s.performProQuota(jobCtx, email, true)
					return e
				})
			}()
		}
	}
}
