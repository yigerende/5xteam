package sub2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

type Group struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type Account struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type RateLimitWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type QuotaUsage struct {
	RateLimit *struct {
		PrimaryWindow   *RateLimitWindow `json:"primary_window,omitempty"`
		SecondaryWindow *RateLimitWindow `json:"secondary_window,omitempty"`
	} `json:"rate_limit,omitempty"`
}

func (q QuotaUsage) Windows() (window5H, window7D *model.FreeQuotaWindow) {
	if q.RateLimit == nil {
		return nil, nil
	}
	for _, window := range []*RateLimitWindow{q.RateLimit.PrimaryWindow, q.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		value := &model.FreeQuotaWindow{UsedPercent: window.UsedPercent, LimitWindowSeconds: window.LimitWindowSeconds, ResetAfterSeconds: window.ResetAfterSeconds, ResetAt: window.ResetAt}
		if window.LimitWindowSeconds >= 6*24*60*60 {
			window7D = value
		} else {
			window5H = value
		}
	}
	return window5H, window7D
}

type CreateAccountInput struct {
	Name        string
	Credentials map[string]any
	GroupIDs    []int64
	Models      []string
	Concurrency int
	Priority    int
	CpaWS       bool
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Reason  string          `json:"reason"`
	Data    json.RawMessage `json:"data"`
}

type Client struct {
	http      *http.Client
	mu        sync.Mutex
	key       string
	token     string
	expiresAt time.Time
}

func New() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 10
	return &Client{http: &http.Client{Transport: transport, Timeout: 65 * time.Second}}
}

func (c *Client) Groups(ctx context.Context, settings model.Sub2Settings, password string) ([]Group, error) {
	data, err := c.doJSON(ctx, settings, password, http.MethodGet, "/api/v1/admin/groups/all", nil, nil)
	if err != nil {
		return nil, err
	}
	var groups []Group
	if err := unmarshalList(data, &groups); err != nil {
		return nil, fmt.Errorf("解析 Sub2 分组失败: %w", err)
	}
	result := make([]Group, 0, len(groups))
	for _, group := range groups {
		if group.ID > 0 && strings.TrimSpace(group.Name) != "" && (group.Platform == "" || strings.EqualFold(group.Platform, "openai")) {
			result = append(result, group)
		}
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, nil
}

func (c *Client) CreateAccount(ctx context.Context, settings model.Sub2Settings, password string, input CreateAccountInput, idempotencyKey string) (Account, error) {
	groupIDs := uniquePositiveIDs(input.GroupIDs)
	if len(groupIDs) == 0 {
		return Account{}, errors.New("请选择 Sub2 OpenAI 分组")
	}
	if input.Concurrency < 1 {
		input.Concurrency = 10
	}
	if input.Priority < 1 {
		input.Priority = 1
	}
	credentials := make(map[string]any, len(input.Credentials)+1)
	for key, value := range input.Credentials {
		credentials[key] = value
	}
	models := uniqueModelNames(input.Models)
	if len(models) > 0 {
		mapping := make(map[string]string, len(models))
		for _, modelName := range models {
			mapping[modelName] = modelName
		}
		credentials["model_mapping"] = mapping
	} else {
		delete(credentials, "model_mapping")
	}
	body := map[string]any{
		"name": strings.TrimSpace(input.Name), "platform": "openai", "type": "oauth",
		"credentials": credentials, "concurrency": input.Concurrency,
		"priority": input.Priority, "group_ids": groupIDs, "confirm_mixed_channel_risk": true,
	}
	if input.CpaWS {
		body["cpa_ws"] = 1
	}
	data, err := c.doJSON(ctx, settings, password, http.MethodPost, "/api/v1/admin/accounts", body, map[string]string{"Idempotency-Key": idempotencyKey})
	if err != nil {
		return Account{}, err
	}
	var account Account
	if err := json.Unmarshal(data, &account); err != nil {
		return account, fmt.Errorf("解析 Sub2 创建账号响应失败: %w", err)
	}
	if account.ID < 1 {
		return account, errors.New("Sub2 创建账号响应缺少账号 ID")
	}
	return account, nil
}

// DeleteAccount removes a downstream Sub2 account by its admin ID.
func (c *Client) DeleteAccount(ctx context.Context, settings model.Sub2Settings, password string, accountID int64) error {
	if accountID < 1 {
		return errors.New("Sub2 账号 ID 无效")
	}
	path := "/api/v1/admin/accounts/" + strconv.FormatInt(accountID, 10)
	_, err := c.doJSON(ctx, settings, password, http.MethodDelete, path, nil, nil)
	return err
}

func uniquePositiveIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value < 1 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueModelNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (c *Client) QueryQuota(ctx context.Context, settings model.Sub2Settings, password string, accountID int64) (QuotaUsage, error) {
	if accountID < 1 {
		return QuotaUsage{}, errors.New("Sub2 账号 ID 无效")
	}
	path := "/api/v1/admin/openai/accounts/" + strconv.FormatInt(accountID, 10) + "/quota"
	data, err := c.doJSON(ctx, settings, password, http.MethodGet, path, nil, nil)
	if err != nil {
		return QuotaUsage{}, err
	}
	if findHTTPStatus(data) == 401 {
		return QuotaUsage{}, errors.New("Sub2 账号状态 HTTP 401")
	}
	var quota QuotaUsage
	if err := json.Unmarshal(data, &quota); err != nil {
		return quota, fmt.Errorf("解析 Sub2 额度响应失败: %w", err)
	}
	return quota, nil
}

// AccountStatus reads the Sub2 account record when available. The returned
// status is used to detect an upstream OpenAI 401 and trigger reauthentication.
func (c *Client) AccountStatus(ctx context.Context, settings model.Sub2Settings, password string, accountID int64) (int, error) {
	if accountID < 1 {
		return 0, errors.New("Sub2 账号 ID 无效")
	}
	path := "/api/v1/admin/accounts/" + strconv.FormatInt(accountID, 10)
	data, err := c.doJSON(ctx, settings, password, http.MethodGet, path, nil, nil)
	if err != nil {
		return 0, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	return findHTTPStatus(value), nil
}

func findHTTPStatus(value any) int {
	switch item := value.(type) {
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(item, &decoded) == nil {
			return findHTTPStatus(decoded)
		}
	case []byte:
		var decoded any
		if json.Unmarshal(item, &decoded) == nil {
			return findHTTPStatus(decoded)
		}
	case map[string]any:
		for _, key := range []string{"http_status", "status_code", "status"} {
			if raw, ok := item[key]; ok {
				switch v := raw.(type) {
				case float64:
					if int(v) == 401 {
						return 401
					}
				case string:
					if strings.TrimSpace(v) == "401" || strings.EqualFold(strings.TrimSpace(v), "unauthorized") {
						return 401
					}
				}
			}
		}
		for _, child := range item {
			if status := findHTTPStatus(child); status != 0 {
				return status
			}
		}
	case []any:
		for _, child := range item {
			if status := findHTTPStatus(child); status != 0 {
				return status
			}
		}
	}
	return 0
}

func (c *Client) doJSON(ctx context.Context, settings model.Sub2Settings, password, method, path string, body any, headers map[string]string) (json.RawMessage, error) {
	response, err := c.request(ctx, settings, password, method, path, body, headers)
	if err != nil {
		return nil, err
	}
	data, readErr := readResponse(response)
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode == http.StatusUnauthorized {
		c.invalidate()
		response, err = c.request(ctx, settings, password, method, path, body, headers)
		if err != nil {
			return nil, err
		}
		data, readErr = readResponse(response)
		if readErr != nil {
			return nil, readErr
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Sub2 %s HTTP %d: %s", path, response.StatusCode, compact(data))
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("Sub2 %s 响应不是有效 JSON", path)
	}
	if envelope.Code != 0 {
		message := envelope.Message
		if message == "" {
			message = envelope.Reason
		}
		return nil, fmt.Errorf("Sub2 %s code %d 失败: %s", path, envelope.Code, message)
	}
	if len(envelope.Data) == 0 {
		return json.RawMessage("null"), nil
	}
	return envelope.Data, nil
}

func (c *Client) request(ctx context.Context, settings model.Sub2Settings, password, method, path string, body any, headers map[string]string) (*http.Response, error) {
	base, err := normalizeBaseURL(settings.URL)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	token, err := c.login(ctx, settings, password)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("Sub2 请求超时")
		}
		return nil, fmt.Errorf("Sub2 请求失败: %w", err)
	}
	return response, nil
}

func (c *Client) login(ctx context.Context, settings model.Sub2Settings, password string) (string, error) {
	keyBytes := sha256.Sum256([]byte(strings.TrimSpace(settings.URL) + "\x00" + strings.TrimSpace(settings.Email) + "\x00" + password))
	key := hex.EncodeToString(keyBytes[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == key && c.token != "" && c.expiresAt.After(time.Now()) {
		return c.token, nil
	}
	base, err := normalizeBaseURL(settings.URL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.Email) == "" || strings.TrimSpace(password) == "" {
		return "", errors.New("Sub2 管理员邮箱和密码不能为空")
	}
	payload, _ := json.Marshal(map[string]string{"email": strings.TrimSpace(settings.Email), "password": strings.TrimSpace(password)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/auth/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Sub2 登录失败: %w", err)
	}
	data, err := readResponse(response)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Sub2 登录 HTTP %d: %s", response.StatusCode, compact(data))
	}
	var envelope apiEnvelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Code != 0 {
		return "", errors.New("Sub2 登录响应无效: " + compact(data))
	}
	var result struct {
		AccessToken    string `json:"access_token"`
		AccessTokenAlt string `json:"accessToken"`
		Requires2FA    bool   `json:"requires_2fa"`
	}
	if json.Unmarshal(envelope.Data, &result) != nil {
		return "", errors.New("Sub2 登录数据格式错误")
	}
	if result.Requires2FA {
		return "", errors.New("Sub2 管理员启用了 2FA，第一版暂不支持自动登录")
	}
	token := strings.TrimSpace(result.AccessToken)
	if token == "" {
		token = strings.TrimSpace(result.AccessTokenAlt)
	}
	if token == "" {
		return "", errors.New("Sub2 登录响应缺少 access_token")
	}
	c.key, c.token, c.expiresAt = key, token, time.Now().Add(10*time.Minute)
	return token, nil
}

func (c *Client) invalidate() { c.mu.Lock(); c.token = ""; c.expiresAt = time.Time{}; c.mu.Unlock() }

func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("Sub2 地址必须是有效的 HTTP/HTTPS URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func readResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	return io.ReadAll(io.LimitReader(response.Body, 16<<20))
}

func compact(data []byte) string {
	value := strings.Join(strings.Fields(string(data)), " ")
	if len(value) > 500 {
		return value[:500] + "..."
	}
	return value
}

func unmarshalList(data json.RawMessage, target any) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '[' {
		return json.Unmarshal(data, target)
	}
	var wrapper struct {
		Items json.RawMessage `json:"items"`
		List  json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	if len(wrapper.Items) > 0 {
		return json.Unmarshal(wrapper.Items, target)
	}
	if len(wrapper.List) > 0 {
		return json.Unmarshal(wrapper.List, target)
	}
	return errors.New("响应中没有账号列表")
}
