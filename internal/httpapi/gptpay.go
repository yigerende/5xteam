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
	v, _, err := s.store.GPTPaySettings()
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
	v, key, err := s.store.GPTPaySettings()
	if err != nil || key == "" {
		writeAPI(w, 400, nil, "请先保存 GPTPay API Key")
		return
	}
	account, err := s.gptPayClient().Account(r.Context(), v.URL, key)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	writeAPI(w, 200, account, "")
}
func (s *Server) listGPTPayCards(w http.ResponseWriter, r *http.Request) {
	p := s.parsePagination(r)
	items, total, err := s.store.GPTPayCards(p.Limit, p.Offset, r.URL.Query().Get("enabled") == "true")
	if err != nil {
		writeAPI(w, 500, nil, "读取银行卡失败")
		return
	}
	writeAPI(w, 200, paginatedData(items, total, p, nil), "")
}
func (s *Server) saveGPTPayCard(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		Raw     string  `json:"raw"`
		Number  string  `json:"number"`
		CVV     string  `json:"cvv"`
		Month   int     `json:"exp_month"`
		Year    int     `json:"exp_year"`
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
	}
	if v.Account.ID == "" && c.ChatGPTSession != "" {
		var saved gptpay.Session
		if json.Unmarshal([]byte(c.ChatGPTSession), &saved) == nil && saved.AccessToken == v.AccessToken && strings.EqualFold(saved.User.Email, p.Email) {
			v.Account.ID = saved.Account.ID
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

func (s *Server) createGPTPayOrder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CardID    string `json:"card_id"`
		RequestID string `json:"request_id"`
		PlanCode  string `json:"plan_code"`
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
		if existing.Email != email || existing.CardID != input.CardID || existing.PlanCode != input.PlanCode {
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
	if input.PlanCode != v.PlanCode {
		writeAPI(w, 409, nil, "开通套餐配置已变化，请重新打开开通窗口确认")
		return
	}
	card, secret, err := s.store.GPTPayCard(input.CardID)
	if err != nil || !card.Enabled {
		writeAPI(w, 400, nil, "请选择已启用的银行卡")
		return
	}
	if err = gptpay.ValidateCard(secret, time.Now()); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	session, err := gptPaySession(p, c)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	snapshot := gptpay.Snapshot{URL: v.URL, APIKey: key, Input: gptpay.CreateInput{PlanCode: v.PlanCode, CardSecret: secret, Session: session}}
	order := gptpay.Order{ID: id, Email: email, CardID: card.ID, CardName: card.Name, CardLast4: card.Last4, PlanCode: v.PlanCode, Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
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
	s.updateProPaymentStage(order)
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	remote, rid, err := s.gptPayClient().Create(ctx, snapshot, order.ID)
	order.RequestID = rid
	order.UpdatedAt = time.Now()
	if err != nil {
		order.Status = "submission_unknown"
		var apiErr *gptpay.APIError
		if errors.As(err, &apiErr) && !apiErr.Uncertain {
			order.Status = "failed"
		}
		order.Error = err.Error()
	} else {
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
		s.auditProEvent(p, "recharge", o.Status, "gptpay", "Pro 开通订单状态："+o.Status, map[string]any{"order_id": o.ID, "supplier_order_id": o.Remote.ID, "plan_code": o.PlanCode, "request_id": o.RequestID, "error": o.Error, "settlement_status": o.Remote.SettlementStatus, "cancellation_status": o.Remote.CancellationStatus})
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
	if o.Remote.ID != "" || !o.Active() {
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
		IDs []string `json:"order_ids"`
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
	v, key, err := s.store.GPTPaySettings()
	if err != nil || key == "" {
		writeAPI(w, 400, nil, "请先保存 GPTPay API Key")
		return
	}
	items, err := s.gptPayClient().Status(r.Context(), v.URL, key, ids)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	for i := range items {
		items[i].FailureReason = gptpay.Redact(items[i].FailureReason, gptpay.Snapshot{APIKey: key})
	}
	writeAPI(w, 200, map[string]any{"orders": items}, "")
}
