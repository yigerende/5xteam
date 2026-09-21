package gptpay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultURL = "https://gptpay.tokenseek.app/api/v1"

type Settings struct {
	CardAccountLimit      int    `json:"card_account_limit"`
	URL                   string `json:"url"`
	PlanCode              string `json:"plan_code"`
	KeyPresent            bool   `json:"key_present"`
	SessionTimeoutMinutes int    `json:"session_timeout_minutes"`
}

type Card struct {
	OpenedAccounts    int       `json:"opened_accounts"`
	PendingAccounts   int       `json:"pending_accounts"`
	RemainingAccounts int       `json:"remaining_accounts"`
	AccountLimit      int       `json:"account_limit"`
	Available         bool      `json:"available"`
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Last4             string    `json:"last4"`
	ExpMonth          int       `json:"exp_month"`
	ExpYear           int       `json:"exp_year"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type CardSecret struct {
	Number string `json:"cardNumber"`
	Month  int    `json:"expMonth"`
	Year   int    `json:"expYear"`
	CVV    string `json:"cvv"`
}

type Session struct {
	User struct {
		Email string `json:"email"`
	} `json:"user"`
	Account struct {
		ID string `json:"id"`
	} `json:"account"`
	AccessToken string `json:"accessToken"`
}

type CreateInput struct {
	PlanCode string `json:"planCode"`
	CardSecret
	Session Session `json:"session"`
}

// Snapshot is encrypted at rest and never returned by list/detail APIs. Keeping
// it makes an uncertain submission replayable with exactly the original input.
type Snapshot struct {
	URL    string      `json:"url"`
	APIKey string      `json:"api_key"`
	Input  CreateInput `json:"input"`
}

type RemoteOrder struct {
	ID                 string `json:"id"`
	OrderNo            string `json:"orderNo"`
	Status             string `json:"status"`
	SettlementStatus   string `json:"settlementStatus"`
	CancellationStatus string `json:"cancellationStatus"`
	PlanCode           string `json:"planCode"`
	ReservedCredits    int64  `json:"reservedCredits"`
	ChargedCredits     int64  `json:"chargedCredits"`
	ReleasedCredits    int64  `json:"releasedCredits"`
	TargetEmail        string `json:"targetEmail"`
	CardLast4          string `json:"cardLast4"`
	FailureReason      string `json:"failureReason"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type Order struct {
	LastStatusCheckAt *time.Time  `json:"last_status_check_at,omitempty"`
	NextStatusCheckAt *time.Time  `json:"next_status_check_at,omitempty"`
	StatusCheckError  string      `json:"status_check_error,omitempty"`
	ID                string      `json:"id"`
	Email             string      `json:"email"`
	CardID            string      `json:"card_id"`
	CardName          string      `json:"card_name"`
	CardLast4         string      `json:"card_last4"`
	PlanCode          string      `json:"plan_code"`
	Status            string      `json:"status"`
	Error             string      `json:"error,omitempty"`
	RequestID         string      `json:"request_id,omitempty"`
	Remote            RemoteOrder `json:"remote"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

func (o Order) Active() bool { return o.Status != "success" && o.Status != "failed" }

func (o Order) NeedsStatusPoll() bool {
	return o.Remote.ID != "" && (o.Active() || o.Remote.CancellationStatus == "waiting" || o.Remote.CancellationStatus == "pending")
}

type Account struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	Wallet struct {
		AvailableCredits int64 `json:"availableCredits"`
		ReservedCredits  int64 `json:"reservedCredits"`
	} `json:"wallet"`
	Level struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"level"`
	Plans []struct {
		PlanCode       string `json:"planCode"`
		SuccessCredits int64  `json:"successCredits"`
		FailureCredits int64  `json:"failureCredits"`
	} `json:"plans"`
	APIKey struct {
		ExpiresAt  *string        `json:"expiresAt"`
		RateLimits map[string]int `json:"rateLimits"`
	} `json:"apiKey"`
}

func NormalizeSettings(v Settings) (Settings, error) {
	if v.CardAccountLimit == 0 {
		v.CardAccountLimit = 3
	}
	if v.CardAccountLimit < 1 || v.CardAccountLimit > 10000 {
		return v, fmt.Errorf("单卡开通账号上限须为 1～10000")
	}
	v.URL = strings.TrimRight(strings.TrimSpace(v.URL), "/")
	if v.URL == "" {
		v.URL = DefaultURL
	}
	u, err := url.Parse(v.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return v, fmt.Errorf("GPTPay 地址无效")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return v, fmt.Errorf("GPTPay 地址需要 HTTPS")
	}
	if u.Path == "" {
		v.URL += "/api/v1"
	}
	if v.PlanCode == "" {
		v.PlanCode = "pro5"
	}
	if v.PlanCode != "pro5" && v.PlanCode != "pro20" {
		return v, fmt.Errorf("请选择 Pro 5x 或 Pro 20x")
	}
	if v.SessionTimeoutMinutes == 0 {
		v.SessionTimeoutMinutes = 30
	}
	if v.SessionTimeoutMinutes < 5 || v.SessionTimeoutMinutes > 60 {
		return v, fmt.Errorf("登录及开通会话最长保留 5～60 分钟")
	}
	return v, nil
}

var digits = regexp.MustCompile(`^\d+$`)

func ValidateCard(v CardSecret, now time.Time) error {
	if !digits.MatchString(v.Number) || len(v.Number) < 12 || len(v.Number) > 19 {
		return fmt.Errorf("银行卡号需为 12～19 位数字")
	}
	if !digits.MatchString(v.CVV) || len(v.CVV) < 3 || len(v.CVV) > 4 {
		return fmt.Errorf("CVV 需为 3～4 位数字")
	}
	if v.Month < 1 || v.Month > 12 || v.Year < 2000 || v.Year > 9999 {
		return fmt.Errorf("有效期格式错误")
	}
	if v.Year < now.Year() || (v.Year == now.Year() && v.Month < int(now.Month())) {
		return fmt.Errorf("银行卡已过期")
	}
	return nil
}

// YYYYMM / YYYY-MM / YYYY/MM are year first. MM/YY and MM/YYYY
// are also accepted; a four-digit compact date is explicitly YYMM.
func ParseCard(line string, now time.Time) (CardSecret, error) {
	parts := strings.Split(strings.TrimSpace(line), "----")
	if len(parts) != 3 {
		return CardSecret{}, fmt.Errorf("格式应为：银行卡号----年月----CVV")
	}
	v := CardSecret{Number: strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(parts[0]), " ", ""), "-", ""), CVV: strings.TrimSpace(parts[2])}
	d := strings.TrimSpace(parts[1])
	p := strings.FieldsFunc(d, func(r rune) bool { return r == '/' || r == '-' || r == ' ' })
	var year, month string
	if len(p) == 2 && len(p[0]) == 4 {
		year, month = p[0], p[1]
	} else if len(p) == 2 {
		month, year = p[0], p[1]
	} else if len(p) == 1 && len(d) == 6 {
		year, month = d[:4], d[4:]
	} else if len(p) == 1 && len(d) == 4 {
		year, month = d[:2], d[2:]
	}
	if !digits.MatchString(year) || !digits.MatchString(month) {
		return v, fmt.Errorf("年月使用 YYYYMM（如 202812）或 YYYY-MM；也支持 MM/YY")
	}
	v.Year, _ = strconv.Atoi(year)
	v.Month, _ = strconv.Atoi(month)
	if len(year) == 2 {
		v.Year += 2000
	}
	return v, ValidateCard(v, now)
}

type APIError struct {
	HTTPStatus, Code int
	RequestID        string
	Uncertain        bool
}

func (e *APIError) Error() string {
	message := map[int]string{40001: "请求、银行卡或 Session 无效", 40101: "API Key 无效或已过期", 40201: "Credits 不足", 40301: "账号或服务不可用", 40901: "幂等请求参数冲突", 40902: "该账号已有未完成订单", 41301: "请求过大", 42901: "供应商限流", 50301: "供应商暂时不可用"}[e.Code]
	if message == "" {
		message = "供应商响应异常"
	}
	if e.Uncertain {
		message += "，提交结果待确认，请查询订单或重试原订单"
	}
	return fmt.Sprintf("GPTPay HTTP %d / %d：%s（requestId: %s）", e.HTTPStatus, e.Code, message, e.RequestID)
}

type Client struct{ HTTP *http.Client }

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) call(ctx context.Context, base, key, method, path, idem string, input, output any) (string, error) {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return "", fmt.Errorf("无法编码 GPTPay 请求")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("GPTPay 请求地址无效")
	}
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", &APIError{Uncertain: idem != ""}
	}
	defer res.Body.Close()
	var env struct {
		Code      *int            `json:"code"`
		Data      json.RawMessage `json:"data"`
		RequestID string          `json:"requestId"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&env)
	requestID := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(env.RequestID, "")
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	code := 0
	if env.Code != nil {
		code = *env.Code
	}
	if err != nil || env.Code == nil || res.StatusCode < 200 || res.StatusCode >= 300 || code != 0 {
		uncertain := idem != "" && (err != nil || env.Code == nil || res.StatusCode >= 500 || res.StatusCode < 200 || res.StatusCode >= 300 && res.StatusCode < 400 || code == 40901 || code == 40902)
		return requestID, &APIError{HTTPStatus: res.StatusCode, Code: code, RequestID: requestID, Uncertain: uncertain}
	}
	if err = json.Unmarshal(env.Data, output); err != nil {
		return requestID, &APIError{HTTPStatus: res.StatusCode, RequestID: requestID, Uncertain: idem != ""}
	}
	return requestID, nil
}
func (c *Client) Account(ctx context.Context, base, key string) (Account, error) {
	var v Account
	_, err := c.call(ctx, base, key, "GET", "/user", "", nil, &v)
	return v, err
}
func (c *Client) Create(ctx context.Context, snapshot Snapshot, id string) (RemoteOrder, string, error) {
	var v RemoteOrder
	rid, err := c.call(ctx, snapshot.URL, snapshot.APIKey, "POST", "/gpt-recharge/orders", id, snapshot.Input, &v)
	if err == nil && (v.ID == "" || !ValidStatus(v.Status)) {
		err = &APIError{Uncertain: true, RequestID: rid}
	}
	v.FailureReason = Redact(v.FailureReason, snapshot)
	return v, rid, err
}
func (c *Client) Status(ctx context.Context, base, key string, ids []string) ([]RemoteOrder, error) {
	if len(ids) < 1 || len(ids) > 50 {
		return nil, fmt.Errorf("每次查询 1～50 个订单")
	}
	var v struct {
		Orders []RemoteOrder `json:"orders"`
	}
	_, err := c.call(ctx, base, key, "POST", "/gpt-recharge/orders/status", "", map[string]any{"orderIds": ids}, &v)
	return v.Orders, err
}
func ValidStatus(s string) bool {
	switch s {
	case "created", "submitting", "processing", "success", "failed", "submission_unknown", "manual_review":
		return true
	}
	return false
}

var sensitiveText = regexp.MustCompile(`(?:\b\d{12,19}\b|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)`)

func Redact(s string, snapshot Snapshot) string {
	for _, secret := range []string{snapshot.Input.Session.AccessToken, snapshot.Input.Number, snapshot.Input.CVV, snapshot.APIKey} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	s = sensitiveText.ReplaceAllString(s, "[redacted]")
	if len(s) > 1000 {
		s = s[:1000]
	}
	return s
}
