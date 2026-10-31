package gptpay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Share a supplier cooldown across orders using the same saved API credentials.
// Orders also persist their next check time so restarts cannot erase the delay.
var xupayCooldowns = struct {
	sync.Mutex
	until map[[32]byte]time.Time
}{until: map[[32]byte]time.Time{}}

func XUPayRetryAfter(err error) time.Duration {
	var e *APIError
	if errors.As(err, &e) && e.Provider == XUPay && e.HTTPStatus == 429 {
		if e.RetryAfter > 0 {
			return e.RetryAfter
		}
		return time.Minute
	}
	return 0
}

func xupayRetryDelay(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds >= 0 {
		return max(time.Second, time.Duration(seconds)*time.Second)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return time.Minute
}

func XUPayCountry(country string) bool {
	switch country {
	case "PH", "US", "JP", "SG", "GB", "EG", "TR":
		return true
	}
	return false
}

// New subscriptions need a web refresh credential. Check before reserving a
// card; immutable legacy order replays do not pass through this admission check.
func XUPayRefreshable(session Session) bool {
	snapshot := NewSnapshot(Settings{Provider: XUPay}, "", CardSecret{}, session)
	if token := snapshot.WebSessionToken; token != "" {
		return len(token) <= 32768 && (&http.Cookie{Name: "__Secure-next-auth.session-token", Value: token}).Valid() == nil
	}
	var raw struct {
		Cookies string `json:"cookies"`
	}
	if json.Unmarshal(session.Raw, &raw) != nil || len(raw.Cookies) > 65536 || strings.ContainsAny(raw.Cookies, "\r\n") {
		return false
	}
	for _, cookie := range (&http.Request{Header: http.Header{"Cookie": {raw.Cookies}}}).Cookies() {
		if xupaySessionCookie.MatchString(cookie.Name) && cookie.Value != "" && cookie.Valid() == nil {
			return true
		}
	}
	return false
}

var xupaySessionCookie = regexp.MustCompile(`^__Secure-(?:next-auth|platform)\.session-token(?:\.[0-9]{1,2})?$`)

func (e *APIError) xupayMessage() string {
	message := map[string]string{
		"API_KEY_INVALID": "API Key 无效或已过期", "RESOURCE_FORBIDDEN": "账户或接口权限不足",
		"REQUEST_REJECTED":     "请求未被受理，请检查 Session、银行卡、账单资料及服务费配置",
		"INSUFFICIENT_CREDITS": "可用服务费余额不足", "RATE_LIMITED": "请求过于频繁，请稍后查询原订单",
		"ORDER_CONFLICT":  "存在冲突或未结束的申请，请在序付支付全监控核实原申请",
		"ORDER_NOT_FOUND": "未找到订单，原订单已保留", "NETWORK_ERROR": "网络请求失败",
		"INVALID_RESPONSE": "返回数据不完整或状态不一致", "UNAVAILABLE": "服务暂不可用",
	}[e.ErrorCode]
	if message == "" {
		message = "请求失败，请在序付支付全监控核查"
	}
	if e.Uncertain {
		message += "；提交结果待确认，不得新建付款，只能查询或重试原订单"
	}
	return fmt.Sprintf("序付 HTTP %d：%s", e.HTTPStatus, message)
}

// XUPay uses a direct JSON body, not the GPTPay code/data envelope. Never
// propagate arbitrary response text: it may contain credentials or card data.
func (c *Client) xupayCall(ctx context.Context, base, key, method, path, idem string, input, output any) error {
	identity := sha256.Sum256([]byte(base + "\x00" + key))
	xupayCooldowns.Lock()
	now := time.Now()
	for id, at := range xupayCooldowns.until {
		if !at.After(now) {
			delete(xupayCooldowns.until, id)
		}
	}
	delay := time.Until(xupayCooldowns.until[identity])
	xupayCooldowns.Unlock()
	if delay > 0 {
		return &APIError{Provider: XUPay, HTTPStatus: 429, ErrorCode: "RATE_LIMITED", RetryAfter: delay}
	}
	delay = 0
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return &APIError{Provider: XUPay, ErrorCode: "REQUEST_REJECTED"}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return &APIError{Provider: XUPay, ErrorCode: "REQUEST_REJECTED"}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return &APIError{Provider: XUPay, ErrorCode: "NETWORK_ERROR", Uncertain: method == http.MethodPost}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		delay = xupayRetryDelay(res.Header.Get("Retry-After"), time.Now())
		xupayCooldowns.Lock()
		if until := time.Now().Add(delay); until.After(xupayCooldowns.until[identity]) {
			xupayCooldowns.until[identity] = until
		}
		xupayCooldowns.Unlock()
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	var object map[string]json.RawMessage
	valid := err == nil && len(raw) <= 1<<20 && json.Unmarshal(raw, &object) == nil && object != nil
	if !valid || res.StatusCode < 200 || res.StatusCode >= 300 || len(object["error"]) > 0 {
		code := map[int]string{400: "REQUEST_REJECTED", 401: "API_KEY_INVALID", 402: "INSUFFICIENT_CREDITS", 403: "RESOURCE_FORBIDDEN", 404: "ORDER_NOT_FOUND", 409: "ORDER_CONFLICT", 429: "RATE_LIMITED", 503: "UNAVAILABLE"}[res.StatusCode]
		if code == "" {
			code = "INVALID_RESPONSE"
		}
		// A conflict may refer to a previously accepted payment. An unreadable
		// error or a server/network failure must retain the card reservation.
		definite := valid && len(object["error"]) > 0 && (res.StatusCode == 400 || res.StatusCode == 401 || res.StatusCode == 402 || res.StatusCode == 403 || res.StatusCode == 429)
		return &APIError{Provider: XUPay, HTTPStatus: res.StatusCode, ErrorCode: code, Uncertain: method == http.MethodPost && !definite, RetryAfter: delay}
	}
	if err = json.Unmarshal(raw, output); err != nil {
		return &APIError{Provider: XUPay, HTTPStatus: res.StatusCode, ErrorCode: "INVALID_RESPONSE", Uncertain: method == http.MethodPost}
	}
	return nil
}

func (c *Client) createXUPay(ctx context.Context, s Snapshot, id string) (RemoteOrder, string, error) {
	product := map[string]string{"pro5": "pro5x", "pro20": "pro"}[s.Input.PlanCode]
	if product == "" || !XUPayCountry(s.Country) || s.TermsAccepted == nil || !*s.TermsAccepted {
		return RemoteOrder{}, "", &APIError{Provider: XUPay, ErrorCode: "REQUEST_REJECTED"}
	}
	s.Input.Session.SessionToken = s.WebSessionToken
	body := map[string]any{
		"product_id": product, "session": s.Input.Session, "country": s.Country, "seats": 1,
		"accepted": true, "workflow": "subscription", "keep_renewal": false,
		"card": map[string]string{"number": s.Input.Number, "month": strconv.Itoa(s.Input.Month), "year": strconv.Itoa(s.Input.Year), "cvc": s.Input.CVV},
	}
	var response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	err := c.xupayCall(ctx, s.URL, s.APIKey, "POST", "/orders", id, body, &response)
	if err != nil {
		return RemoteOrder{}, "", err
	}
	if !xupayOrderID.MatchString(response.ID) || response.Status == "" {
		return RemoteOrder{}, "", &APIError{Provider: XUPay, ErrorCode: "INVALID_RESPONSE", Uncertain: true}
	}
	// Even an idempotent replay reporting succeeded needs the detail endpoint
	// to distinguish payment success, activation and cancellation of renewal.
	return RemoteOrder{ID: response.ID, Status: "processing", PlanCode: s.Input.PlanCode, Country: s.Country, CreditUnit: "cent_R", CancellationStatus: "waiting"}, "", nil
}

var xupayOrderID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type xupayDetail struct {
	Events []struct {
		Stage     string `json:"stage"`
		Message   string `json:"message"`
		CreatedAt string `json:"created_at"`
	} `json:"events"`
	Order struct {
		ID         string `json:"id"`
		Product    string `json:"product_id"`
		Workflow   string `json:"workflow"`
		Status     string `json:"status"`
		Activation string `json:"activation"`
		Renewal    string `json:"renewal"`
		Country    string `json:"country"`
		Email      string `json:"email"`
		Fee        int64  `json:"fee"`
		CreatedAt  string `json:"created_at"`
		UpdatedAt  string `json:"updated_at"`
	} `json:"order"`
	Payment struct {
		AutomaticRunning bool   `json:"automatic_running"`
		CanCheck         *bool  `json:"can_check"`
		Message          string `json:"message"`
		State            string `json:"state"`
		FeeState         string `json:"fee_state"`
		Amount           *int64 `json:"amount"`
		Currency         string `json:"currency"`
		ChargedAmount    *int64 `json:"charged_amount"`
		ChargedCurrency  string `json:"charged_currency"`
		Last4            string `json:"last4"`
		ErrorCode        string `json:"error_code"`
	} `json:"payment"`
}

func (v xupayDetail) remote(id string) (RemoteOrder, error) {
	o, p := v.Order, v.Payment
	plan := map[string]string{"pro5x": "pro5", "pro": "pro20"}[o.Product]
	if o.ID != id || o.Workflow != "subscription" || plan == "" || o.Status == "" {
		return RemoteOrder{}, &APIError{Provider: XUPay, ErrorCode: "INVALID_RESPONSE"}
	}
	r := RemoteOrder{ID: id, Status: "processing", PlanCode: plan, Country: o.Country, TargetEmail: o.Email, CardLast4: p.Last4, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, PaymentState: p.State, ActivationStatus: o.Activation, CreditUnit: "cent_R"}
	r.AutomaticRunning, r.CanCheck = p.AutomaticRunning, p.CanCheck
	if regexp.MustCompile(`^[a-z0-9_]{1,80}$`).MatchString(p.ErrorCode) {
		r.ErrorCode = p.ErrorCode
	}
	r.Message = xupayScrubText(p.Message)
	for _, event := range v.Events[max(0, len(v.Events)-20):] {
		r.Events = append(r.Events, PaymentEvent{Stage: xupayScrubText(event.Stage), Message: xupayScrubText(event.Message), CreatedAt: xupayScrubText(event.CreatedAt)})
	}
	switch p.FeeState {
	case "held":
		r.SettlementStatus, r.ReservedCredits = "reserved", o.Fee
	case "charged":
		r.SettlementStatus, r.ChargedCredits = "settled", o.Fee
	case "released":
		r.SettlementStatus, r.ReleasedCredits = "released", o.Fee
	}
	r.CancellationStatus = map[string]string{"not_started": "waiting", "pending": "pending", "retrying": "pending", "disabled": "success", "kept": "not_applicable", "review": "failed"}[o.Renewal]
	if p.ChargedAmount != nil && p.ChargedCurrency != "" {
		r.PaymentAmountMinor, r.PaymentCurrency, r.PaymentAmountSource = p.ChargedAmount, p.ChargedCurrency, "charged"
	} else {
		r.PaymentAmountMinor, r.PaymentCurrency, r.PaymentAmountSource = p.Amount, p.Currency, "quoted"
	}
	r.PaymentCurrency = strings.ToUpper(strings.TrimSpace(r.PaymentCurrency))
	r.PaymentCountry = o.Country
	if r.PaymentAmountMinor != nil && *r.PaymentAmountMinor >= 0 && r.PaymentCurrency != "" {
		// Card ledgers store decimal amounts, including JPY (zero minor digits).
		// Preserve both forms so their reconciliation never treats yen as cents.
		if r.PaymentCurrency == "JPY" {
			r.PaymentAmount = strconv.FormatInt(*r.PaymentAmountMinor, 10)
		} else {
			r.PaymentAmount = fmt.Sprintf("%d.%02d", *r.PaymentAmountMinor/100, *r.PaymentAmountMinor%100)
		}
	}
	switch {
	case p.State == "succeeded":
		switch o.Activation {
		case "active":
			r.Status = "success"
		case "pending":
			r.Status = "processing"
		default:
			if !p.AutomaticRunning {
				r.Status, r.FailureReason = "manual_review", "序付已确认付款成功，订阅生效待核实；禁止再次付款，请核实原订单"
			}
		}
	case p.State == "review" || p.State == "action_required" || o.Status == "review" || o.Status == "succeeded" || p.FeeState == "charged":
		if !p.AutomaticRunning {
			r.Status, r.FailureReason = "manual_review", "序付付款结果或额外验证待核实；保留原订单，请核实原订单"
		}
	case p.State == "failed" || p.State == "cancelled" || ((o.Status == "failed" || o.Status == "cancelled") && p.State == ""):
		if !p.AutomaticRunning {
			r.Status, r.FailureReason, r.CancellationStatus = "failed", "序付确认本次办理失败，未确认付款成功；请在序付支付全监控查看原因", "not_applicable"
		}
	case p.State == "preparing" || p.State == "quoted" || p.State == "submitting" || p.State == "checking":
	case p.State == "" && (o.Status == "queued" || o.Status == "processing" || o.Status == "awaiting_payment"):
	default:
		return RemoteOrder{}, &APIError{Provider: XUPay, ErrorCode: "INVALID_RESPONSE"}
	}
	if p.AutomaticRunning && o.Renewal == "review" {
		r.CancellationStatus = "pending"
	}
	if r.Status == "failed" && r.ErrorCode != "" {
		r.FailureReason += "（" + r.ErrorCode + "）"
	}
	if r.Status == "success" && r.CancellationStatus == "failed" {
		r.RetryHint = "订阅已生效，取消自动续费失败；请在序付续费管理接续处理，不得重复付款"
	}
	retry := r.Status == "failed"
	r.Retryable = &retry
	if p.State == "succeeded" || r.Status == "manual_review" {
		r.FailureCategory = "payment_confirmation"
	}
	return r, nil
}

func (c *Client) CheckXUPay(ctx context.Context, base, key, id string) error {
	if !xupayOrderID.MatchString(id) {
		return fmt.Errorf("序付订单 ID 格式不正确")
	}
	var response struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.xupayCall(ctx, base, key, "POST", "/orders/"+id+"/payment/check", "", map[string]any{}, &response); err != nil {
		return err
	}
	if !response.Accepted {
		return &APIError{Provider: XUPay, ErrorCode: "INVALID_RESPONSE", Uncertain: true}
	}
	return nil
}

var xupayPublicSecrets = regexp.MustCompile(`(?i)(?:bearer\s+[^\s"'<>]+|(?:access[_-]?token|session[_-]?token|refresh[_-]?token|cookies?|authorization|api[_-]?key|cvc|cvv|password)["']?\s*[=:]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)|__Secure-(?:next-auth|platform)\.session-token(?:\.[0-9]+)?=[^\s;]+|eyJ[A-Za-z0-9_.-]{20,}|(?:\d[ -]?){12,19})`)

func xupayScrubText(value string) string {
	value = xupayPublicSecrets.ReplaceAllString(value, "[已隐藏]")
	value = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	return value
}

func xupayPublicText(value string) string {
	value = xupayScrubText(value)
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000]) + "…"
	}
	return value
}

func SanitizeXUPayRemote(r *RemoteOrder, snapshot Snapshot) {
	// Remove known raw session credentials even if the supplier echoes only a
	// cookie value without its field name. Never expose the raw session itself.
	secrets := []string{}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(snapshot.Input.Session.Raw, &raw)
	for key, encoded := range raw {
		lower := strings.ToLower(key)
		if !strings.Contains(lower, "token") && !strings.Contains(lower, "cookie") {
			continue
		}
		var value string
		if json.Unmarshal(encoded, &value) != nil || value == "" {
			continue
		}
		secrets = append(secrets, value)
		if lower == "cookies" {
			for _, cookie := range (&http.Request{Header: http.Header{"Cookie": {value}}}).Cookies() {
				if cookie.Value != "" {
					secrets = append(secrets, cookie.Value)
				}
			}
		}
	}
	clean := func(value string) string {
		for _, secret := range secrets {
			value = strings.ReplaceAll(value, secret, "[已隐藏]")
		}
		return xupayPublicText(Redact(value, snapshot))
	}
	r.Message = clean(r.Message)
	for i := range r.Events {
		r.Events[i].Message = clean(r.Events[i].Message)
		r.Events[i].Stage = clean(r.Events[i].Stage)
		r.Events[i].CreatedAt = clean(r.Events[i].CreatedAt)
	}
}

func (c *Client) statusXUPay(ctx context.Context, base, key string, ids []string) ([]RemoteOrder, error) {
	if len(ids) < 1 || len(ids) > 50 {
		return nil, fmt.Errorf("每次查询 1～50 个订单")
	}
	for _, id := range ids {
		if !xupayOrderID.MatchString(id) {
			return nil, fmt.Errorf("序付订单 ID 格式不正确")
		}
	}
	results := make([]RemoteOrder, 0, len(ids))
	// Serial detail requests avoid multiplying the platform's per-user limit.
	for _, id := range ids {
		var v xupayDetail
		if err := c.xupayCall(ctx, base, key, "GET", "/orders/"+id, "", nil, &v); err != nil {
			return nil, err
		}
		r, err := v.remote(id)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (c *Client) XUPayCatalog(ctx context.Context, base, key string) (Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var data struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Fee  *int64 `json:"member_fee"`
		} `json:"items"`
		Countries []CatalogCountry `json:"countries"`
		Unit      string           `json:"unit"`
		Connected bool             `json:"payment_connected"`
	}
	if err := c.xupayCall(ctx, base, key, "GET", "/catalog", "", nil, &data); err != nil {
		return Catalog{}, err
	}
	if data.Unit != "cent_R" || len(data.Countries) == 0 {
		return Catalog{}, &APIError{Provider: XUPay, ErrorCode: "INVALID_RESPONSE"}
	}
	catalog := Catalog{CreditUnit: data.Unit, Countries: []CatalogCountry{}, Products: []CatalogProduct{}}
	for _, country := range data.Countries {
		if XUPayCountry(country.Code) {
			catalog.Countries = append(catalog.Countries, country)
		}
	}
	for _, item := range data.Items {
		plan := map[string]string{"pro5x": "pro5", "pro": "pro20"}[item.ID]
		if plan != "" && item.Fee != nil && *item.Fee >= 0 {
			enabled := data.Connected
			catalog.Products = append(catalog.Products, CatalogProduct{Code: plan, Label: item.Name, CreditPrice: item.Fee, Enabled: &enabled})
		}
	}
	return catalog, nil
}

func (c *Client) accountXUPay(ctx context.Context, base, key string) (Account, error) {
	var data struct {
		Balance    *int64 `json:"balance"`
		Frozen     *int64 `json:"frozen"`
		Unit       string `json:"unit"`
		Membership struct {
			Current struct {
				Code string `json:"code"`
				Name string `json:"name"`
			} `json:"current"`
		} `json:"membership"`
	}
	if err := c.xupayCall(ctx, base, key, "GET", "/wallet", "", nil, &data); err != nil {
		return Account{}, err
	}
	if data.Unit != "cent_R" || data.Balance == nil || data.Frozen == nil {
		return Account{}, &APIError{Provider: XUPay, ErrorCode: "INVALID_RESPONSE"}
	}
	a := Account{CreditUnit: data.Unit}
	a.Wallet.AvailableCredits, a.Wallet.ReservedCredits = *data.Balance, *data.Frozen
	a.Level.Code, a.Level.Name = data.Membership.Current.Code, data.Membership.Current.Name
	catalog, err := c.XUPayCatalog(ctx, base, key)
	if err != nil {
		return Account{}, err
	}
	for _, product := range catalog.Products {
		a.Plans = append(a.Plans, AccountPlan{PlanCode: product.Code, SuccessCredits: *product.CreditPrice})
	}
	return a, nil
}
