package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

func (s *Server) gptPayClient() *gptpay.Client {
	if s.gptpay != nil {
		return s.gptpay
	}
	return gptpay.New()
}
func (s *Server) getGPTPaySettings(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.store.GPTPayProviderSettings(r.URL.Query().Get("provider"))
	if err != nil {
		writeAPI(w, 500, nil, "读取 GPTPay 配置失败")
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) saveGPTPaySettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		gptpay.Settings
		APIKey string `json:"api_key"`
	}
	if decodeJSON(w, r, &input, 16<<10) != nil {
		return
	}
	v, err := s.store.SaveGPTPaySettings(input.Settings, input.APIKey)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) getGPTPayAccount(w http.ResponseWriter, r *http.Request) {
	v, key, err := s.store.GPTPayProviderSettings(r.URL.Query().Get("provider"))
	if err != nil || key == "" {
		writeAPI(w, 400, nil, "请先保存 GPTPay API Key")
		return
	}
	account, err := s.gptPayClient().AccountForProvider(r.Context(), v.Provider, v.URL, key)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	writeAPI(w, 200, account, "")
}

func (s *Server) getGPTPayCatalog(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == gptpay.XUPay {
		// Never send a saved bearer key to an unsaved URL supplied by the browser.
		cfg, key, err := s.store.GPTPayProviderSettings(provider)
		if err != nil || key == "" {
			writeAPI(w, 400, nil, "请先保存序付地址和 API Key，再刷新国家与套餐")
			return
		}
		catalog, err := s.gptPayClient().XUPayCatalog(r.Context(), cfg.URL, key)
		if err != nil {
			writeAPI(w, 502, nil, err.Error())
			return
		}
		writeAPI(w, 200, catalog, "")
		return
	}
	if provider != gptpay.CMSNav {
		writeAPI(w, 400, nil, "该供应商不支持公开国家与套餐目录")
		return
	}
	base := r.URL.Query().Get("url")
	if base == "" {
		cfg, _, err := s.store.GPTPayProviderSettings(provider)
		if err != nil {
			writeAPI(w, 500, nil, "读取供应商配置失败")
			return
		}
		base = cfg.URL
	}
	// Accept the address currently being edited without saving settings or
	// requiring an API key. The route remains behind the application's login.
	cfg, err := gptpay.NormalizeSettings(gptpay.Settings{Provider: provider, URL: base})
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	catalog, err := s.gptPayClient().Catalog(r.Context(), cfg.URL)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	writeAPI(w, 200, catalog, "")
}
func (s *Server) listGPTPayCards(w http.ResponseWriter, r *http.Request) {
	p := s.parsePagination(r)
	var items []gptpay.Card
	var total int
	var err error
	if provider := r.URL.Query().Get("card_provider"); provider != "" {
		if (provider != "configured" && provider != "ejh" && provider != "nexovcc") || r.URL.Query().Get("enabled") != "true" {
			writeAPI(w, 400, nil, "请选择有效卡台和可用卡筛选")
			return
		}
		var cfg model.ProSettings
		cfg, _, _, err = s.store.ProSettings()
		if err == nil {
			if provider != "configured" {
				cfg.CardProvider = provider
			}
			pay, _, e := s.store.GPTPaySettings()
			if e == nil {
				_, cfg, e = s.manualProPurchase(r.Context(), cfg, pay, cfg.AutoCardProvider(), nil)
			}
			if e != nil {
				writeAPI(w, 409, nil, e.Error())
				return
			}
			// Filter before pagination so an earlier EJH/Other card cannot
			// become the default when full automation is configured for Nexovcc.
			items, err = s.automaticProCards(cfg)
			total = len(items)
			offset := min(p.Offset, total)
			items = items[offset:min(total, offset+p.Limit)]
		}
	} else {
		items, total, err = s.store.GPTPayCards(p.Limit, p.Offset, r.URL.Query().Get("enabled") == "true", true)
	}
	if err != nil {
		writeAPI(w, 500, nil, "读取银行卡失败")
		return
	}
	writeAPI(w, 200, paginatedData(items, total, p, nil), "")
}
func (s *Server) saveGPTPayCard(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MaxAccounts *int    `json:"max_accounts"`
		CardType    *string `json:"card_type"`
		Name        *string `json:"name"`
		Enabled     *bool   `json:"enabled"`
		Raw         string  `json:"raw"`
		Number      string  `json:"number"`
		CVV         string  `json:"cvv"`
		Month       int     `json:"exp_month"`
		Year        int     `json:"exp_year"`
	}
	if decodeJSON(w, r, &input, 16<<10) != nil {
		return
	}
	id := r.PathValue("id")
	unlock := s.lockFreeAccount("gptpay-card:" + id)
	defer unlock()
	v := gptpay.Card{ID: randomRegistrationID(), Enabled: true}
	secret := gptpay.CardSecret{}
	if id != "" {
		var err error
		v, secret, err = s.store.GPTPayCard(id)
		if err != nil {
			writeAPI(w, 404, nil, err.Error())
			return
		}
	}
	if input.Name != nil {
		v.Name = *input.Name
	}
	if input.Enabled != nil {
		v.Enabled = *input.Enabled
	}
	// Omitted on enable/disable/name-only updates; zero explicitly resets it.
	v.CardType = ""
	if input.CardType != nil {
		v.CardType = *input.CardType
		if v.CardType != "ejh" && v.CardType != "other" && v.CardType != "nexovcc" {
			writeAPI(w, 400, nil, "银行卡类型只能是 EJH、环球或其他")
			return
		}
		if v.CardType == "nexovcc" && v.Source != "nexovcc" {
			writeAPI(w, 400, nil, "环球卡请从环球卡台同步导入")
			return
		}
	}
	v.MaxAccounts = input.MaxAccounts
	if (v.Source == "ejh" || v.Source == "nexovcc") && (input.Raw != "" || input.Number != "" || input.CVV != "" || (input.Month != 0 && input.Month != secret.Month) || (input.Year != 0 && input.Year != secret.Year)) {
		writeAPI(w, 400, nil, "卡台卡密只能从对应卡台刷新，不允许手动替换")
		return
	}
	if input.Raw != "" {
		var err error
		secret, err = gptpay.ParseCard(input.Raw, time.Now())
		if err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
	} else {
		if input.Number != "" {
			secret.Number = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(input.Number), " ", ""), "-", "")
		}
		if input.CVV != "" {
			secret.CVV = strings.TrimSpace(input.CVV)
		}
		if input.Month != 0 {
			secret.Month = input.Month
		}
		if input.Year != 0 {
			secret.Year = input.Year
		}
	}
	// A disabled expired card can still be renamed or disabled without being used.
	checkDate := time.Now()
	if !v.Enabled {
		checkDate = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := gptpay.ValidateCard(secret, checkDate); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	updated, err := s.store.SaveGPTPayCard(v, secret)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, updated, "")
}

// The Pro card list retains associations for both types; the EJH dashboard
// deliberately returns EJH-only cases and must not serve this general list.
func (s *Server) listGPTPayCardCases(w http.ResponseWriter, r *http.Request) {
	cases, err := s.store.EJHCases()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	financialCases, err := s.proFinancialCases(cases)
	if err != nil {
		writeAPI(w, 500, nil, "读取环球关联账号失败")
		return
	}
	writeAPI(w, 200, financialCases, "")
}
func (s *Server) deleteGPTPayCard(w http.ResponseWriter, r *http.Request) {
	unlock := s.lockFreeAccount("gptpay-card:" + r.PathValue("id"))
	defer unlock()
	if err := s.store.DeleteGPTPayCard(r.PathValue("id")); err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"deleted": true}, "")
}
func (s *Server) listGPTPayOrders(w http.ResponseWriter, r *http.Request) {
	p := s.parsePagination(r)
	items, total, err := s.store.GPTPayOrders(p.Limit, p.Offset, r.URL.Query().Get("email"))
	if err != nil {
		writeAPI(w, 500, nil, "读取 GPTPay 订单失败")
		return
	}
	writeAPI(w, 200, paginatedData(items, total, p, nil), "")
}

func gptPaySession(p model.MailAccountProfile, c model.MailAccountCredentials) (gptpay.Session, error) {
	v := gptpay.Session{}
	v.AccessToken = strings.TrimSpace(c.AccessToken)
	v.User.Email = p.Email
	if v.AccessToken == "" {
		return v, fmt.Errorf("账号没有保存 AT，请先获取 AT")
	}
	if expiry, ok := workflow.AccessTokenExpiry(v.AccessToken); ok && !expiry.After(time.Now()) {
		return v, fmt.Errorf("保存的 AT 已过期，请先获取新的 AT")
	}
	if info, err := workflow.DecodeUserInfo(v.AccessToken); err == nil {
		if !strings.EqualFold(info.Email, p.Email) {
			return v, fmt.Errorf("AT 邮箱与当前账号不一致")
		}
		v.Account.ID = info.AccountID
		v.Account.PlanType = info.PlanType
	}
	if c.ChatGPTSession != "" {
		var saved gptpay.Session
		if json.Unmarshal([]byte(c.ChatGPTSession), &saved) == nil && saved.AccessToken == v.AccessToken && strings.EqualFold(saved.User.Email, p.Email) {
			v.Raw = saved.Raw
			if saved.Account.PlanType != "" {
				v.Account.PlanType = saved.Account.PlanType
			}
			if v.Account.ID == "" {
				v.Account.ID = saved.Account.ID
			}
		}
	}
	if v.Account.ID == "" {
		v.Account.ID = p.OAuthAccountID
	}
	if v.Account.ID == "" {
		return v, fmt.Errorf("缺少 Account ID，请在账号凭证中补充或保存完整 Session")
	}
	return v, nil
}

var gptPayRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`)

func validateGPTPaySession(provider string, session gptpay.Session) error {
	if provider == gptpay.XUPay && !gptpay.XUPayRefreshable(session) {
		return fmt.Errorf("序付订阅需要包含 sessionToken 或有效会话 Cookie 的完整 Session，请重新登录或使用全自动开通；仅 AT 无法办理")
	}
	if provider == gptpay.CMSNav && len(session.Raw) == 0 {
		return fmt.Errorf("CMSNav 需要与当前 AT 匹配的完整 Session，请先获取临时 AT 或使用全自动开通")
	}
	return nil
}

func (s *Server) createGPTPayOrder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CardID         string  `json:"card_id"`
		RequestID      string  `json:"request_id"`
		PlanCode       string  `json:"plan_code"`
		Provider       string  `json:"provider"`
		Country        *string `json:"country"`
		CreditQuantity *int    `json:"credit_quantity"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	if !gptPayRequestID.MatchString(input.RequestID) {
		writeAPI(w, 400, nil, "开通请求编号无效，请重新打开开通窗口")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	unlock := s.lockProAccount(email)
	defer unlock()
	p, c, err := s.store.MailAccountCredential(email)
	if err != nil || p.ManagementScope != "pro" {
		writeAPI(w, 404, nil, "Pro 账号不存在")
		return
	}
	if p.ProWorkflowRunning {
		writeAPI(w, 409, nil, "该账号正在合并空间，请完成后再开通 Pro")
		return
	}
	id := "pro-" + input.RequestID
	if existing, _, e := s.store.GPTPayOrder(id); e == nil {
		requestedCard := existing.CardID
		if existing.RequestedCardID != nil {
			requestedCard = *existing.RequestedCardID
		}
		if existing.Email != email || requestedCard != input.CardID || existing.PlanCode != input.PlanCode || gptpay.Provider(existing.Provider) != gptpay.Provider(input.Provider) ||
			(input.Country != nil && strings.ToUpper(strings.TrimSpace(*input.Country)) != existing.Country) ||
			(input.CreditQuantity != nil && *input.CreditQuantity != existing.CreditQuantity) {
			writeAPI(w, 409, nil, "请求编号已用于不同的开通参数")
			return
		}
		writeAPI(w, 200, existing, "")
		return
	} else if !errors.Is(e, sql.ErrNoRows) {
		writeAPI(w, 500, nil, "读取原订单失败")
		return
	}
	if existing, e := s.store.ActiveGPTPayOrder(email); e == nil {
		writeAPI(w, 200, existing, "")
		return
	} else if !errors.Is(e, sql.ErrNoRows) {
		writeAPI(w, 500, nil, "读取进行中的订单失败")
		return
	}
	w, finishStage := s.trackProStageResponse(w, email, "recharge")
	defer finishStage()
	v, key, err := s.store.GPTPaySettings()
	if err != nil || key == "" {
		writeAPI(w, 400, nil, "请先在 Pro 全自动配置中保存 GPTPay API Key")
		return
	}
	if input.PlanCode != v.PlanCode || gptpay.Provider(input.Provider) != v.Provider ||
		(input.Country != nil && strings.ToUpper(strings.TrimSpace(*input.Country)) != v.Country) ||
		(input.CreditQuantity != nil && *input.CreditQuantity != v.CreditQuantity) {
		writeAPI(w, 409, nil, "供应商、套餐、国家或点数配置已变化，请重新打开开通窗口确认")
		return
	}
	// Preserve validation responses for an explicitly selected invalid card.
	if err = s.validateGPTPayNewPurchase(email, v); err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	if input.CardID != "" {
		selected, secret, e := s.store.GPTPayCard(input.CardID)
		if e != nil || !selected.Enabled {
			writeAPI(w, 400, nil, "请选择已启用的银行卡")
			return
		}
		if e = gptpay.ValidateCard(secret, time.Now()); e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
	}
	session, err := gptPaySession(p, c)
	if err == nil {
		err = validateGPTPaySession(v.Provider, session)
	}
	if err == nil {
		err = gptpay.ValidatePlanSession(v, session)
	}
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	pro, _, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取自动补卡配置失败")
		return
	}
	if s.proAutomationRunning(email) || s.proLoginAlreadyRunning(email) {
		writeAPI(w, 409, nil, "账号正在执行登录或自动任务，未购买新卡")
		return
	}
	ctx, pro, err := s.manualProPurchase(r.Context(), pro, v, pro.AutoCardProvider(), nil)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	r = r.WithContext(ctx)
	card, secret, err := s.reserveProStartCard(r.Context(), email, input.CardID, pro.AutoCardEnabled(), false)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	defer s.store.ReleaseProCard(email)
	snapshot := gptpay.NewSnapshot(v, key, secret, session)
	order := gptpay.Order{Provider: v.Provider, Country: v.Country, CreditQuantity: v.CreditQuantity, ID: id, Email: email, CardID: card.ID, CardName: card.Name, CardLast4: card.Last4, PlanCode: v.PlanCode, Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	order.RequestedCardID = &input.CardID
	if err = s.store.CreateGPTPayOrder(order, snapshot); err != nil {
		writeAPI(w, 409, nil, "未提交开通订单："+err.Error())
		return
	}
	s.auditProEvent(p, "recharge", "running", "gptpay", "开始开通 Pro", map[string]any{"order_id": order.ID, "plan_code": order.PlanCode, "card_last4": order.CardLast4})
	order, err = s.submitGPTPayOrder(context.WithoutCancel(r.Context()), order, snapshot)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, order, "")
}
func (s *Server) submitGPTPayOrder(ctx context.Context, order gptpay.Order, snapshot gptpay.Snapshot) (gptpay.Order, error) {
	return s.submitGPTPayOrderWithAdmission(ctx, order, snapshot, nil)
}

func (s *Server) submitGPTPayOrderWithAdmission(ctx context.Context, order gptpay.Order, snapshot gptpay.Snapshot, admit func() error) (gptpay.Order, error) {
	previouslyUncertain := order.SubmissionUncertain || (order.SubmissionStarted && !order.RateLimited)
	if order.RateLimited {
		if !order.CanRetryRateLimit() || order.NextStatusCheckAt.After(time.Now()) {
			return order, nil
		}
		order.RateLimitRetries++
	}
	s.updateProPaymentStage(order)
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	if admit != nil {
		if err := admit(); err != nil {
			return order, err
		}
	}
	if snapshot.Provider == gptpay.XUPay {
		// A later replay can be rejected before XUPay reaches its idempotency
		// lookup (expired session, rate limit, changed key). Persist first entry
		// before I/O so that such a response cannot erase an earlier charge.
		order.SubmissionStarted = true
		order.SubmissionUncertain = true // A crash during this attempt leaves its outcome unknown.
		if err := s.store.UpdateGPTPayOrder(order); err != nil {
			return order, fmt.Errorf("保存序付提交标记失败，未发送付款请求")
		}
	}
	remote, rid, err := s.gptPayClient().Create(ctx, snapshot, order.ID)
	order.RequestID = rid
	order.UpdatedAt = time.Now()
	if err != nil {
		order.Status = "submission_unknown"
		var apiErr *gptpay.APIError
		if errors.As(err, &apiErr) && !apiErr.Uncertain && !(snapshot.Provider == gptpay.XUPay && previouslyUncertain) {
			order.Status = "failed"
		}
		order.Error = err.Error()
		order.SubmissionUncertain = previouslyUncertain || apiErr == nil || apiErr.Uncertain
		order.RateLimited = gptpay.XUPayRetryAfter(err) > 0
		order.NextStatusCheckAt = nil
		if order.RateLimited {
			order.Status = "submission_unknown"
			if order.RateLimitRetries < gptpay.XUPayRateLimitRetries {
				next := time.Now().UTC().Add(gptpay.XUPayRetryAfter(err))
				order.NextStatusCheckAt = &next
				order.Error = fmt.Sprintf("序付接口限流，等待后重试原订单（已自动重试 %d/%d 次）", order.RateLimitRetries, gptpay.XUPayRateLimitRetries)
			} else {
				order.Error = "序付接口限流，3 次自动重试已用尽；原任务已保留，未切换账号，请稍后核实"
			}
		}
		if apiErr != nil && (snapshot.Provider == gptpay.CMSNav || snapshot.Provider == gptpay.XUPay) {
			order.Remote.Retryable = apiErr.Retryable
			order.Remote.RetryHint = gptpay.Redact(apiErr.RetryHint, snapshot)
			order.Remote.ErrorCode = apiErr.ErrorCode
			if order.Remote.RetryHint != "" {
				order.Error += "；" + order.Remote.RetryHint
			}
		}
	} else {
		order.RateLimited, order.SubmissionUncertain, order.NextStatusCheckAt = false, false, nil
		gptpay.SanitizeXUPayRemote(&remote, snapshot)
		order.Remote = remote
		order.Status = remote.Status
		order.Error = remote.FailureReason
	}
	if err = s.store.UpdateGPTPayOrder(order); err != nil {
		order.Status, order.Error = "submission_unknown", "供应商已响应但本地订单保存失败，请查询原订单，勿新建订单"
		s.updateProPaymentStage(order)
		return order, fmt.Errorf("供应商已响应但本地订单保存失败，请查询原订单，勿新建订单")
	}
	s.auditGPTPayOrder(order)
	return order, nil
}
func (s *Server) auditGPTPayOrder(o gptpay.Order) {
	s.updateProPaymentStage(o)
	if p, _, err := s.store.MailAccountCredential(o.Email); err == nil {
		s.auditProEvent(p, "recharge", o.Status, "gptpay", "Pro 开通订单状态："+o.Status, map[string]any{"provider": gptpay.Provider(o.Provider), "order_id": o.ID, "supplier_order_id": o.Remote.ID, "plan_code": o.PlanCode, "request_id": o.RequestID, "error": o.Error, "settlement_status": o.Remote.SettlementStatus, "cancellation_status": o.Remote.CancellationStatus})
	}
}
func (s *Server) retryGPTPayOrder(w http.ResponseWriter, r *http.Request) {
	o, _, err := s.store.GPTPayOrder(r.PathValue("id"))
	if err != nil {
		writeAPI(w, 404, nil, "订单不存在")
		return
	}
	if s.proAutomationRunning(o.Email) {
		writeAPI(w, 409, nil, "全自动流程正在处理此账号订单")
		return
	}
	unlock := s.lockProAccount(o.Email)
	defer unlock()
	o, snapshot, err := s.store.GPTPayOrder(o.ID)
	if err != nil {
		writeAPI(w, 500, nil, "读取订单失败")
		return
	}
	if o.Status == "manual_review" || o.Remote.ID != "" || !o.Active() {
		writeAPI(w, 200, o, "")
		return
	}
	// Card/account settings may change after the first request. Only replay
	// the immutable encrypted snapshot with its original idempotency key.
	o, err = s.submitGPTPayOrder(context.WithoutCancel(r.Context()), o, snapshot)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, o, "")
}
func (s *Server) refreshGPTPayOrder(w http.ResponseWriter, r *http.Request) {
	o, snapshot, err := s.store.GPTPayOrder(r.PathValue("id"))
	if err != nil {
		writeAPI(w, 404, nil, "订单不存在")
		return
	}
	if s.proAutomationRunning(o.Email) {
		writeAPI(w, 200, o, "")
		return
	}
	if o.Remote.ID == "" {
		writeAPI(w, 200, o, "")
		return
	}
	unlock := s.lockProAccount(o.Email)
	defer unlock()
	// A concurrent refresh may have finished while this request waited.
	o, snapshot, err = s.store.GPTPayOrder(o.ID)
	if err != nil {
		writeAPI(w, 500, nil, "读取订单失败")
		return
	}
	o, err = s.syncGPTPayOrderStatus(r.Context(), o, snapshot)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errGPTPayRemoteNotFound) {
			status = http.StatusConflict
		}
		writeAPI(w, status, nil, err.Error())
		return
	}
	writeAPI(w, 200, o, "")
}
func (s *Server) queryGPTPayOrders(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string   `json:"provider"`
		IDs      []string `json:"order_ids"`
	}
	if decodeJSON(w, r, &input, 16<<10) != nil {
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range input.IDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if len(ids) < 1 || len(ids) > 50 {
		writeAPI(w, 400, nil, "请输入 1～50 个供应商订单 ID")
		return
	}
	v, key, err := s.store.GPTPayProviderSettings(input.Provider)
	if err != nil || key == "" {
		writeAPI(w, 400, nil, "请先保存 GPTPay API Key")
		return
	}
	items, err := s.gptPayClient().StatusForProvider(r.Context(), v.Provider, v.URL, key, ids)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	for i := range items {
		gptpay.SanitizeXUPayRemote(&items[i], gptpay.Snapshot{APIKey: key})
		items[i].FailureReason = gptpay.Redact(items[i].FailureReason, gptpay.Snapshot{APIKey: key})
		items[i].RetryHint = gptpay.Redact(items[i].RetryHint, gptpay.Snapshot{APIKey: key})
	}
	writeAPI(w, 200, map[string]any{"orders": items}, "")
}
