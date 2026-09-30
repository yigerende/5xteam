package gptpay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

func (e *APIError) cmsnavMessage() string {
	messages := map[string]string{
		"API_KEY_INVALID": "API Key 无效或已过期", "RESOURCE_FORBIDDEN": "订单不属于当前 API 用户",
		"INSUFFICIENT_CREDITS": "可用积分不足", "REQUEST_REJECTED": "请求字段或业务参数错误",
		"SESSION_INVALID": "Session 无效或已过期，请重新获取临时 AT", "CHANNEL_UNAVAILABLE": "支付通道暂时不可用",
		"ORDER_NOT_FOUND": "订单不存在，请核对任务 ID", "CORS_ORIGIN_NOT_ALLOWED": "来源不被允许",
	}
	message := messages[e.ErrorCode]
	if message == "" {
		message = "供应商响应异常"
	}
	if e.Uncertain {
		message += "，提交结果待确认，请查询订单或重试原订单"
	}
	return fmt.Sprintf("GPTPay CMSNav HTTP %d：%s", e.HTTPStatus, message)
}

func (c *Client) cmsnavCall(ctx context.Context, base, key, method, path, idem string, input, output any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return fmt.Errorf("无法编码 GPTPay 请求")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("GPTPay 请求地址无效")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return &APIError{ErrorCode: "NETWORK_ERROR", Uncertain: idem != ""}
	}
	defer res.Body.Close()
	var env struct {
		Code      *int            `json:"code"`
		Data      json.RawMessage `json:"data"`
		ErrorCode string          `json:"errorCode"`
		Error     string          `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&env)
	if err != nil || res.StatusCode < 200 || res.StatusCode >= 300 || env.ErrorCode != "" || env.Error != "" || env.Code == nil || *env.Code != 0 {
		code := env.ErrorCode
		if code == "" {
			code = "INVALID_RESPONSE"
		}
		// Only an explicit documented rejection can release a payment reservation.
		definite := map[string]bool{"API_KEY_INVALID": true, "RESOURCE_FORBIDDEN": true, "INSUFFICIENT_CREDITS": true, "REQUEST_REJECTED": true, "SESSION_INVALID": true, "CHANNEL_UNAVAILABLE": true, "CORS_ORIGIN_NOT_ALLOWED": true}[code]
		return &APIError{HTTPStatus: res.StatusCode, ErrorCode: code, Uncertain: idem != "" && (err != nil || res.StatusCode >= 500 || !definite)}
	}
	if err = json.Unmarshal(env.Data, output); err != nil {
		return &APIError{HTTPStatus: res.StatusCode, ErrorCode: "INVALID_RESPONSE", Uncertain: idem != ""}
	}
	return nil
}

type cmsnavOrder struct {
	TaskID       string `json:"taskId"`
	OrderID      string `json:"orderId"`
	LocalOrderID string `json:"localOrderId"`
	Status       string `json:"status"`
	Order        struct {
		RemoteOrder
		LocalOrderID string `json:"localOrderId"`
		Error        string `json:"error"`
		RetryHint    string `json:"retryHint"`
		CreditStatus string `json:"creditStatus"`
		CreditCost   int64  `json:"creditCost"`
	} `json:"order"`
	Progress struct {
		Status         string `json:"status"`
		RechargeStatus string `json:"rechargeStatus"`
		RenewalStatus  string `json:"renewalStatus"`
		RetryHint      string `json:"retryHint"`
	} `json:"progress"`
}

func (v cmsnavOrder) remote(id string) RemoteOrder {
	r := v.Order.RemoteOrder
	for _, candidate := range []string{v.TaskID, v.OrderID, v.LocalOrderID, v.Order.LocalOrderID, r.ID, id} {
		if candidate != "" {
			r.ID = candidate
			break
		}
	}
	if r.Status == "" {
		r.Status = v.Progress.Status
	}
	if r.Status == "" {
		r.Status = v.Status
	}
	if v.Progress.RechargeStatus == "success" {
		r.Status = "success"
	}
	if r.CancellationStatus == "" {
		r.CancellationStatus = v.Progress.RenewalStatus
	}
	if r.CancellationStatus == "processing" {
		r.CancellationStatus = "pending"
	}
	switch r.Status {
	case "reserved", "pending", "queued", "running":
		r.Status = "processing"
	}
	if r.SettlementStatus == "" {
		r.SettlementStatus = v.Order.CreditStatus
	}
	switch v.Order.CreditStatus {
	case "held", "reserved":
		r.ReservedCredits = v.Order.CreditCost
	case "charged", "settled", "consumed":
		r.ChargedCredits = v.Order.CreditCost
	case "released", "refunded":
		r.ReleasedCredits = v.Order.CreditCost
	}
	if r.FailureReason == "" && r.Status == "failed" {
		r.FailureReason = v.Order.Error
		if r.FailureReason == "" {
			r.FailureReason = v.Order.RetryHint
		}
	}
	return r
}

func (c *Client) createCMSNav(ctx context.Context, s Snapshot, id string) (RemoteOrder, string, error) {
	product := map[string]string{"pro5": "pro5x", "pro20": "pro20x", "pro50": "pro50x"}[s.Input.PlanCode]
	if product == "" {
		return RemoteOrder{}, "", &APIError{ErrorCode: "REQUEST_REJECTED"}
	}
	body := struct {
		ProductCode   string     `json:"productCode"`
		Country       string     `json:"country"`
		CancelRenewal bool       `json:"cancelRenewal"`
		Session       Session    `json:"session"`
		Card          CardSecret `json:"card"`
	}{product, s.Country, true, s.Input.Session, s.Input.CardSecret}
	var response cmsnavOrder
	err := c.cmsnavCall(ctx, s.URL, s.APIKey, "POST", "/customer/recharges", id, body, &response)
	r := response.remote("")
	if err == nil && (r.ID == "" || !ValidStatus(r.Status)) {
		err = &APIError{ErrorCode: "INVALID_RESPONSE", Uncertain: true}
	}
	r.FailureReason = Redact(r.FailureReason, s)
	return r, "", err
}

func (c *Client) StatusForProvider(ctx context.Context, provider, base, key string, ids []string) ([]RemoteOrder, error) {
	if Provider(provider) == Tokenseek {
		return c.Status(ctx, base, key, ids)
	}
	if provider != CMSNav {
		return nil, fmt.Errorf("未知供应商")
	}
	if len(ids) < 1 || len(ids) > 50 {
		return nil, fmt.Errorf("每次查询 1～50 个订单")
	}
	// CMSNav has no bulk endpoint; cap concurrent requests for manual lookups.
	results := make([]RemoteOrder, len(ids))
	errs := make([]error, len(ids))
	jobs := make(chan int, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for worker := 0; worker < min(4, len(ids)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var response cmsnavOrder
				err := c.cmsnavCall(ctx, base, key, "GET", "/customer/orders/"+url.PathEscape(ids[i]), "", nil, &response)
				var apiErr *APIError
				if errors.As(err, &apiErr) && apiErr.ErrorCode == "ORDER_NOT_FOUND" {
					results[i] = RemoteOrder{ID: ids[i], Status: "not_found"}
					continue
				}
				r := response.remote(ids[i])
				if err == nil && !ValidStatus(r.Status) {
					err = &APIError{ErrorCode: "INVALID_RESPONSE"}
				}
				results[i], errs[i] = r, err
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (c *Client) AccountForProvider(ctx context.Context, provider, base, key string) (Account, error) {
	if Provider(provider) == Tokenseek {
		return c.Account(ctx, base, key)
	}
	if provider != CMSNav {
		return Account{}, fmt.Errorf("未知供应商")
	}
	var data struct {
		Account
		Balance    int64            `json:"balance"`
		Held       int64            `json:"held"`
		Prices     map[string]int64 `json:"prices"`
		Membership struct {
			Level string `json:"level"`
			Name  string `json:"name"`
		} `json:"membership"`
	}
	err := c.cmsnavCall(ctx, base, key, "GET", "/customer/wallet", "", nil, &data)
	a := data.Account
	a.Wallet.AvailableCredits, a.Wallet.ReservedCredits = data.Balance, data.Held
	a.Level.Code, a.Level.Name = data.Membership.Level, data.Membership.Name
	for _, code := range []string{"plus", "pro5x", "pro20x", "pro50x", "team"} {
		if price, ok := data.Prices[code]; ok {
			a.Plans = append(a.Plans, struct {
				PlanCode       string `json:"planCode"`
				SuccessCredits int64  `json:"successCredits"`
				FailureCredits int64  `json:"failureCredits"`
			}{code, price, 0})
		}
	}
	return a, err
}
