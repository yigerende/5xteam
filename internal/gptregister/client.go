// Package gptregister implements the platform's documented customer API only.
package gptregister

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Settings struct {
	URL        string `json:"url"`
	KeyPresent bool   `json:"key_present"`
	EnterPro   bool   `json:"enter_pro"`
}
type Submission struct {
	Settings
	APIKey string `json:"api_key"`
}
type User struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Points int64  `json:"points"`
	Frozen int64  `json:"frozen"`
}
type Product struct {
	Name      string `json:"name"`
	NeedOAuth bool   `json:"need_oauth"`
	UnitPrice int64  `json:"unit_price"`
}
type Products struct {
	OrdersOpen  bool      `json:"orders_open"`
	MaxQuantity int       `json:"max_quantity"`
	Currency    string    `json:"currency"`
	Products    []Product `json:"products"`
}
type Order struct {
	ID        string `json:"id"`
	Quantity  int    `json:"quantity"`
	NeedOAuth bool   `json:"need_oauth"`
	Status    string `json:"status"`
	Delivered int    `json:"delivered"`
	UnitPrice int64  `json:"unit_price"`
	Charged   int64  `json:"charged"`
	Released  int64  `json:"released"`
}

func (o Order) Terminal() bool {
	return o.Status == "completed" || o.Status == "partial" || o.Status == "failed" || o.Status == "cancelled"
}

type Account struct {
	ID        string `json:"id"`
	OrderID   string `json:"order_id"`
	Email     string `json:"email"`
	NeedOAuth bool   `json:"need_oauth"`
}
type AccountPage struct {
	Items []Account `json:"items"`
	Total int       `json:"total"`
}
type Credentials struct {
	AssetID  string                     `json:"asset_id,omitempty"`
	Email    string                     `json:"email"`
	Password string                     `json:"password"`
	TOTP     string                     `json:"totp_secret"`
	OAuth    map[string]json.RawMessage `json:"oauth,omitempty"`
}

func (c Credentials) Token(name string) string {
	var value string
	_ = json.Unmarshal(c.OAuth[name], &value)
	return value
}

type APIError struct {
	Status     int
	RetryAfter int
}

func (e *APIError) Error() string {
	switch e.Status {
	case 401:
		return "注册平台密钥无效或已撤销，请检查配置"
	case 402:
		return "注册平台可用积分不足，请充值后重试原任务"
	case 403:
		return "注册平台权限不足或功能未开放，请检查密钥权限"
	case 404:
		return "注册平台记录不存在，请核对平台地址与订单归属"
	case 409:
		return "注册平台幂等请求冲突，请核对原任务"
	case 429:
		return "注册平台限流或未完成订单过多，稍后自动重试"
	default:
		return fmt.Sprintf("注册平台请求失败（HTTP %d）", e.Status)
	}
}
func NormalizeURL(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("请填写注册平台 HTTP/HTTPS 地址，不要包含账号、查询参数或片段")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	if strings.HasSuffix(u.Path, "/open/v1") {
		u.Path = strings.TrimSuffix(u.Path, "/open/v1")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

type Client struct {
	Base, Key     string
	HTTP          *http.Client
	BeforeRequest func(context.Context) error
}

func New(base, key string) (*Client, error) {
	base, e := NormalizeURL(base)
	if e != nil {
		return nil, e
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 8192 || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("请配置有效的注册平台 API Key")
	}
	return &Client{Base: base, Key: key, HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path, key string, body, out any) error {
	if c.BeforeRequest != nil {
		if e := c.BeforeRequest(ctx); e != nil {
			return e
		}
	}
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.Base+"/open/v1"+path, bytes.NewReader(raw))
	if e != nil {
		return errors.New("注册平台请求地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		return errors.New("注册平台网络请求超时或连接失败，将使用原任务重试")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		delay, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		if t, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			delay = int(time.Until(t).Seconds())
		}
		if delay < 60 {
			delay = 60
		}
		if delay > 3600 {
			delay = 3600
		}
		return &APIError{Status: res.StatusCode, RetryAfter: delay}
	}
	raw, e = io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if e != nil || len(raw) > 4<<20 {
		return errors.New("注册平台响应不完整或过大")
	}
	if e = json.Unmarshal(raw, out); e != nil {
		return errors.New("注册平台返回格式异常")
	}
	return nil
}
func (c *Client) Me(ctx context.Context) (v User, e error) {
	e = c.request(ctx, "GET", "/me", "", nil, &v)
	if e == nil && v.ID == "" {
		e = errors.New("注册平台账户响应缺少 ID")
	}
	return
}
func (c *Client) Products(ctx context.Context) (v Products, e error) {
	e = c.request(ctx, "GET", "/products", "", nil, &v)
	if e == nil && (v.Currency != "points" || v.MaxQuantity < 1 || v.MaxQuantity > 1000 || len(v.Products) != 2) {
		e = errors.New("注册平台服务与价格响应异常")
	}
	if e == nil && (v.Products[0].NeedOAuth == v.Products[1].NeedOAuth || v.Products[0].UnitPrice < 0 || v.Products[1].UnitPrice < 0 || v.Products[0].UnitPrice > 1000000000 || v.Products[1].UnitPrice > 1000000000) {
		e = errors.New("注册平台价格或交付模式异常")
	}
	return
}
func (c *Client) Create(ctx context.Context, key string, quantity int, oauth bool) (v Order, e error) {
	e = c.request(ctx, "POST", "/registration-orders", key, map[string]any{"quantity": quantity, "need_oauth": oauth}, &v)
	return
}
func (c *Client) Order(ctx context.Context, id string) (v Order, e error) {
	e = c.request(ctx, "GET", "/registration-orders/"+url.PathEscape(id), "", nil, &v)
	return
}
func (c *Client) Accounts(ctx context.Context, id string, offset int) (v AccountPage, e error) {
	e = c.request(ctx, "GET", "/accounts?"+url.Values{"order_id": {id}, "limit": {"100"}, "offset": {strconv.Itoa(offset)}}.Encode(), "", nil, &v)
	return
}
func (c *Client) Credentials(ctx context.Context, id string) (v Credentials, e error) {
	e = c.request(ctx, "GET", "/accounts/"+url.PathEscape(id)+"/credentials", "", nil, &v)
	return
}
