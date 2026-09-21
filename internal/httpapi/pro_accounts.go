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
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
)

type proAccountListItem struct {
	model.MailAccountProfile
	ActivationCard *store.ProActivationCard `json:"activation_card,omitempty"`
	StageProgress  model.ProStageDisplay    `json:"pro_stage_progress"`
}

func (s *Server) proAccountListWithCards(items []model.MailAccountProfile) ([]proAccountListItem, error) {
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
	for _, item := range items {
		row := proAccountListItem{MailAccountProfile: item, StageProgress: item.ProStages()}
		row.ProAuto.Error = row.ProAuto.DisplayError()
		if order, ok := orders[strings.ToLower(item.Email)]; ok && item.ProAuto.ID == "" && item.ProManualStages["recharge"].ID == "" {
			progress := proPaymentStage(order)
			row.StageProgress.Steps["recharge"], row.StageProgress.Errors["recharge"] = progress.Status, progress.Error
		}
		if card, ok := cards[strings.ToLower(strings.TrimSpace(item.Email))]; ok {
			row.ActivationCard = &card
		}
		result = append(result, row)
	}
	return result, nil
}

func (s *Server) listProAccounts(w http.ResponseWriter, r *http.Request) {
	if paginationRequested(r) {
		page := s.parsePagination(r)
		items, total, summary, err := s.store.ProAccountsPage(r.URL.Query().Get("query"), r.URL.Query().Get("merge_state"), page.Limit, page.Offset)
		if err != nil {
			writeAPI(w, http.StatusInternalServerError, nil, "读取 Pro 账号失败: "+err.Error())
			return
		}
		rows, err := s.proAccountListWithCards(items)
		if err != nil {
			writeAPI(w, 500, nil, "读取开通银行卡失败")
			return
		}
		writeAPI(w, http.StatusOK, paginatedData(rows, total, page, map[string]any{"summary": summary}), "")
		return
	}
	items := s.store.MailAccountsByManagementScope("pro")
	if items == nil {
		items = []model.MailAccountProfile{}
	}
	rows, err := s.proAccountListWithCards(items)
	if err != nil {
		writeAPI(w, 500, nil, "读取开通银行卡失败")
		return
	}
	writeAPI(w, http.StatusOK, rows, "")
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
	_ = s.store.DeleteProOAuthSession(input.SessionID)
	writeAPI(w, 200, profile, "")
}

func (s *Server) currentProCredential(ctx context.Context, email string) (model.MailAccountProfile, model.MailAccountCredentials, error) {
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, credentials, err
	}
	if !strings.EqualFold(profile.ManagementScope, "pro") {
		return profile, credentials, errors.New("账号不在 Pro 管理中")
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
	settings := s.store.Settings()
	if strings.TrimSpace(settings.ProxyURL) == "" {
		return profile, credentials, errors.New("请先配置全局代理；OpenAI OAuth 刷新禁止直连")
	}
	tokens, err := workflow.RefreshOAuthTokens(ctx, credentials.RefreshToken, settings)
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
	profile, _, _ = s.store.MailAccountCredential(email)
	writeAPI(w, 200, profile, "")
}

type proSettingsInput struct {
	model.ProSettings
	Sub2Password string `json:"sub2_password"`
	CPAKey       string `json:"cpa_key"`
}

func (s *Server) getProSettings(w http.ResponseWriter, _ *http.Request) {
	v, _, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}

func (s *Server) saveProSettings(w http.ResponseWriter, r *http.Request) {
	var input proSettingsInput
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	current, subPassword, cpaKey, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if strings.TrimSpace(input.Sub2Password) == "" {
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
	if input.Provider == "sub2" {
		if err := validateSub2Connection(input.Sub2.URL, input.Sub2.Email, input.Sub2Password); err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
		if len(input.Sub2.GroupIDs) == 0 {
			writeAPI(w, 400, nil, "请选择 Pro 的 Sub2 OpenAI 分组")
			return
		}
	} else if strings.TrimSpace(input.CPA.URL) == "" || strings.TrimSpace(input.CPAKey) == "" {
		writeAPI(w, 400, nil, "CPA 地址和 Management Key 不能为空")
		return
	}
	if input.AutoMergeEnabled && strings.TrimSpace(input.TargetAdminID) == "" {
		writeAPI(w, 400, nil, "开启自动合并前请选择目标母号")
		return
	}
	previousProvider := current.Provider
	input.ScheduledEnabled, input.MaxUnmerged, input.ScheduleIntervalSeconds = current.ScheduledEnabled, current.MaxUnmerged, current.ScheduleIntervalSeconds
	now := time.Now()
	input.LastQuotaSweepAt = current.LastQuotaSweepAt
	if input.QuotaEnabled {
		next := now.Add(time.Duration(input.QuotaCheckIntervalSeconds) * time.Second)
		input.NextQuotaSweepAt = &next
	} else {
		input.NextQuotaSweepAt = nil
	}
	saved, err := s.store.SaveProSettings(input.ProSettings, input.Sub2Password, input.CPAKey)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if previousProvider != saved.Provider {
		for _, account := range s.store.MailAccountsByManagementScope("pro") {
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
	finishStage := s.beginProManualStage(email, "push")
	defer func() { finishStage(retErr) }()
	profile, credentials, err := s.currentProCredential(ctx, email)
	if err != nil {
		return profile, err
	}
	v, subPassword, cpaKey, err := s.store.ProSettings()
	if err != nil {
		return profile, err
	}
	s.auditProEvent(profile, "push", "running", v.Provider, "开始推送 Pro 账号", nil)
	defer func() {
		status, message, details := "completed", "Pro 账号推送成功", map[string]any{"plan_type": "pro"}
		if retErr != nil {
			status, message, details = "failed", "Pro 账号推送失败", map[string]any{"error": retErr.Error()}
		}
		s.auditProEvent(firstProProfile(out, profile), "push", status, v.Provider, message, details)
	}()
	_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus, p.ProLastError = "running", "" })
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
	input := sub2.CreateAccountInput{Name: name, Credentials: buildProCredentials(profile, credentials), GroupIDs: v.Sub2.GroupIDs, Models: v.Sub2.Models, Concurrency: v.Sub2.AccountConcurrency, Priority: v.Sub2.Priority, CpaWS: v.Sub2.CpaWS}
	name = s.sub2NameWithMother(name, profile.TargetAdminID, "")
	input.Name = name
	fingerprint := sha256.Sum256([]byte(email + "|" + name + "|" + credentials.AccessToken))
	created, err := s.sub2.CreateAccount(ctx, v.Sub2, subPassword, input, "pro-"+profile.ID+"-"+fmt.Sprintf("%x", fingerprint[:8]))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "idempotency") {
		created, err = s.sub2.CreateAccount(ctx, v.Sub2, subPassword, input, "pro-"+profile.ID+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	}
	if err != nil {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.PushProvider, p.PushStatus = "sub2", "completed"
		if p.ProMigration != nil {
			p.ProMigration.Downstream = model.ProDownstream(v)
		}
		p.Sub2AccountID, p.Sub2AccountName = created.ID, created.Name
		p.CPAAuthFileName, p.ProLastError = "", ""
		p.QuotaCheckedAt = nil
	})
}

func (s *Server) pushProAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	unlock := s.lockProAccount(email)
	defer unlock()
	p, err := s.performProPush(r.Context(), email)
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
	result := s.runProBatch(r.Context(), emails, func(ctx context.Context, email string) error { _, err := s.performProPush(ctx, email); return err })
	writeAPI(w, 200, result, "")
}

func (s *Server) performProQuota(ctx context.Context, email string, auto bool) (out model.MailAccountProfile, retErr error) {
	finishStage := s.beginProManualStage(email, "quota")
	defer func() { finishStage(retErr) }()
	profile, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, err
	}
	v, subPassword, cpaKey, err := s.store.ProSettings()
	if err != nil {
		return profile, err
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
		if proTransferNeedsConfirmation(*p) {
			p.TransferStatus = "unknown"
		} else {
			p.ProLastError = ""
		}
	})
	var five, seven *model.FreeQuotaWindow
	if v.Provider == "cpa" {
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
		quota, err = s.sub2.QueryQuota(ctx, v.Sub2, subPassword, profile.Sub2AccountID)
		if err == nil {
			five, seven = quota.Windows()
		}
	}
	if err != nil {
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.QuotaStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	if seven == nil {
		err = errors.New("下游未返回可识别的 7d 额度窗口")
		_, _ = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.QuotaStatus, p.ProLastError = "failed", err.Error() })
		return profile, err
	}
	now := time.Now()
	profile, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.Quota5H, p.Quota7D, p.QuotaCheckedAt = five, seven, &now
		p.QuotaStatus = "completed"
		if !proTransferNeedsConfirmation(*p) {
			p.ProLastError = ""
		}
	})
	if err == nil && auto && v.AutoMergeEnabled && !profile.SpaceMergedOnce && seven.UsedPercent >= v.QuotaUsedThreshold && !proTransferNeedsConfirmation(profile) {
		profile, err = s.startProMergeJob(email, nil)
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
	profile, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		return profile, err
	}
	if profile.ManagementScope != "pro" {
		return profile, errors.New("账号不在 Pro 管理中")
	}
	if proTransferNeedsConfirmation(profile) {
		return profile, errProTransferUncertain
	}
	executionID := randomRegistrationID()
	v, _, _, err := s.store.ProSettings()
	if err != nil {
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
	if profile.SpaceMergedOnce && profile.RemoveStatus == "completed" {
		return profile, nil
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
	defer unlockAdmin()
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
	// Pro keeps its Sub2/CPA account after leaving, including legacy cleanup states.
	return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" })
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
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			v, _, _, err := s.store.ProSettings()
			if err != nil || !v.QuotaEnabled {
				continue
			}
			if v.NextQuotaSweepAt != nil && now.Before(*v.NextQuotaSweepAt) {
				continue
			}
			s.proMonitorMu.Lock()
			if s.proMonitorRunning {
				s.proMonitorMu.Unlock()
				continue
			}
			s.proMonitorRunning = true
			s.proMonitorMu.Unlock()
			last := now
			next := now.Add(time.Duration(v.QuotaCheckIntervalSeconds) * time.Second)
			v.LastQuotaSweepAt, v.NextQuotaSweepAt = &last, &next
			_ = s.store.UpdateProQuotaSweep(last, next)
			emails := []string{}
			for _, p := range s.store.MailAccountsByManagementScope("pro") {
				// The new full workflow owns its own quota threshold and due time.
				if p.ProAuto.ID != "" || (p.ProMigration != nil && p.ProMigration.Paused) {
					continue
				}
				needsQuota := !p.SpaceMergedOnce
				needsCleanup := p.SpaceMergedOnce && p.RemoveStatus != "completed"
				if p.PushStatus == "completed" && p.PushProvider == v.Provider && !p.ProWorkflowRunning && (needsQuota || needsCleanup) {
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
					if profile.ProAuto.ID != "" || (profile.ProMigration != nil && profile.ProMigration.Paused) {
						return nil
					}
					if profile.SpaceMergedOnce && profile.RemoveStatus != "completed" {
						_, e := s.performProMerge(jobCtx, email)
						return e
					}
					_, e := s.performProQuota(jobCtx, email, true)
					return e
				})
			}()
		}
	}
}
