package herosms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Service  string `json:"service"`
	Country  string `json:"country"`
	MaxPrice string `json:"max_price"`
}

type Activation struct {
	ID        json.Number `json:"id"`
	Phone     string      `json:"phone"`
	Status    int         `json:"status"`
	CreatedAt string      `json:"createdAt"`
	ExpiredAt string      `json:"expiredAt"`
	OTPList   []OTP       `json:"otpList"`
}

type OTP struct {
	Code string `json:"smsCode"`
}

type Error struct {
	Status int
	Detail string
	Method string
	Path   string
}

func (e *Error) Error() string { return fmt.Sprintf("Hero-SMS HTTP %d: %s", e.Status, e.Detail) }

var client = &http.Client{
	Timeout:       20 * time.Second,
	Transport:     &http.Transport{ForceAttemptHTTP2: false, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func Request(ctx context.Context, cfg Config, method, path string, payload any, result any) error {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = "https://hero-sms.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("Hero-SMS Base URL 无效")
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("Hero-SMS API Key 未配置")
	}
	// Both documented APIs share the same account/key. Never append REST paths to handler_api.php.
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "/api/v1"+strings.SplitN(path, "?", 2)[0], "", "", ""
	if parts := strings.SplitN(path, "?", 2); len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "ApiKey "+cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Hero-SMS 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.ReplaceAll(string(raw), cfg.APIKey, "[redacted]")
		if len(detail) > 1000 {
			detail = detail[:1000]
		}
		return &Error{Status: resp.StatusCode, Detail: detail, Method: method, Path: u.Path}
	}
	if result == nil && resp.StatusCode != http.StatusNoContent {
		return &Error{Status: resp.StatusCode, Detail: "取消/完成未返回预期的 HTTP 204，不能确认为成功", Method: method, Path: u.Path}
	}
	if result != nil && resp.StatusCode != http.StatusNoContent {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(result)
	}
	return nil
}

func Acquire(ctx context.Context, cfg Config) (Activation, error) {
	if cfg.BaseURL == "" || strings.Contains(cfg.BaseURL, "handler_api.php") {
		return acquireLegacy(ctx, cfg)
	}
	var result struct {
		Data []Activation `json:"data"`
	}
	price := cfg.MaxPrice
	if price == "" {
		price = "1"
	}
	err := Request(ctx, cfg, http.MethodPost, "/activations", map[string]any{"service": cfg.Service, "country": cfg.Country, "amount": 1, "maxPrice": price}, &result)
	if err != nil {
		return Activation{}, err
	}
	if len(result.Data) != 1 || result.Data[0].ID == "" || result.Data[0].Phone == "" {
		return Activation{}, fmt.Errorf("Hero-SMS 未返回唯一激活 ID 和号码")
	}
	return result.Data[0], nil
}

// Keep existing handler-based purchase settings working; lifecycle operations use v1.
func acquireLegacy(ctx context.Context, cfg Config) (Activation, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://hero-sms.com/stubs/handler_api.php"
	}
	u, err := url.Parse(base)
	if err != nil {
		return Activation{}, err
	}
	if cfg.APIKey == "" {
		return Activation{}, fmt.Errorf("Hero-SMS API Key 未配置")
	}
	q := u.Query()
	q.Set("api_key", cfg.APIKey)
	q.Set("action", "getNumber")
	q.Set("service", cfg.Service)
	q.Set("country", cfg.Country)
	price := cfg.MaxPrice
	if price == "" {
		price = "1"
	}
	q.Set("maxPrice", price)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Activation{}, err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return Activation{}, fmt.Errorf("Hero-SMS 申请请求失败: %s", strings.ReplaceAll(err.Error(), cfg.APIKey, "[redacted]"))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Activation{}, err
	}
	text := strings.TrimSpace(string(raw))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Activation{}, &Error{Status: resp.StatusCode, Detail: strings.ReplaceAll(text, cfg.APIKey, "[redacted]")}
	}
	parts := strings.Split(text, ":")
	if len(parts) >= 3 && parts[0] == "ACCESS_NUMBER" && parts[1] != "" && parts[2] != "" {
		return Activation{ID: json.Number(parts[1]), Phone: parts[2]}, nil
	}
	var obj struct {
		ID    json.Number `json:"activationId"`
		Phone string      `json:"phoneNumber"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.ID != "" && obj.Phone != "" {
		return Activation{ID: obj.ID, Phone: obj.Phone}, nil
	}
	if len(text) > 500 {
		text = text[:500]
	}
	return Activation{}, fmt.Errorf("Hero-SMS 申请失败: %s", strings.ReplaceAll(text, cfg.APIKey, "[redacted]"))
}

func Replace(ctx context.Context, cfg Config, id string) (Activation, error) {
	var result struct {
		Data []Activation `json:"data"`
	}
	err := Request(ctx, cfg, http.MethodPost, "/activations/"+url.PathEscape(id)+"/replace", nil, &result)
	if err != nil {
		return Activation{}, err
	}
	if len(result.Data) != 1 || result.Data[0].ID == "" || result.Data[0].Phone == "" {
		return Activation{}, fmt.Errorf("Hero-SMS 换号响应缺少激活 ID 或号码")
	}
	return result.Data[0], nil
}

func Fetch(ctx context.Context, cfg Config, id string) (string, error) {
	var result struct {
		Data *OTP `json:"data"`
	}
	err := Request(ctx, cfg, http.MethodGet, "/activations/"+url.PathEscape(id)+"/otp/last", nil, &result)
	if err != nil {
		return "", err
	}
	if result.Data == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Data.Code), nil
}

func Active(ctx context.Context, cfg Config, page int) (map[string]any, error) {
	result := map[string]any{}
	err := Request(ctx, cfg, http.MethodGet, fmt.Sprintf("/activations?size=25&page=%d", page), nil, &result)
	return result, err
}

func Release(ctx context.Context, cfg Config, id string, finish bool) error {
	method, path := http.MethodDelete, "/activations/"+url.PathEscape(id)
	if finish {
		method, path = http.MethodPost, path+"/finish"
	}
	return Request(ctx, cfg, method, path, nil, nil)
}
