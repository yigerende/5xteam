package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

type Client struct{ http *http.Client }

type Group struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color,omitempty"`
}

func New() *Client { return &Client{http: &http.Client{Timeout: 65 * time.Second}} }

func (c *Client) request(ctx context.Context, settings model.CPASettings, key, method, path string, body io.Reader, contentType string) ([]byte, int, error) {
	base := strings.TrimRight(strings.TrimSpace(settings.URL), "/")
	if base == "" {
		return nil, 0, errors.New("CPA 地址不能为空")
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(key) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
		req.Header.Set("X-Management-Key", strings.TrimSpace(key))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("CPA 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("CPA %s HTTP %d: %s", path, resp.StatusCode, compact(data))
	}
	return data, resp.StatusCode, nil
}

func (c *Client) Upload(ctx context.Context, settings model.CPASettings, key, fileName string, payload []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(fileName))
	if err != nil {
		return err
	}
	if _, err = part.Write(payload); err != nil {
		return err
	}
	if err = w.WriteField("default_websockets", fmt.Sprintf("%t", settings.Websockets)); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	_, _, err = c.request(ctx, settings, key, http.MethodPost, "/v0/management/auth-files", &body, w.FormDataContentType())
	return err
}

// RestoreScheduling re-enables the existing auth file after a successful
// in-place OAuth update. CPA clears unavailable/error/retry state when a
// disabled credential is explicitly enabled through this endpoint.
func (c *Client) RestoreScheduling(ctx context.Context, settings model.CPASettings, key, fileName string) error {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return errors.New("CPA auth 文件名不能为空")
	}
	payload, err := json.Marshal(map[string]any{"name": fileName, "disabled": false})
	if err != nil {
		return err
	}
	if _, _, err = c.request(ctx, settings, key, http.MethodPatch, "/v0/management/auth-files/status", bytes.NewReader(payload), "application/json"); err != nil {
		return fmt.Errorf("恢复 CPA 调度失败: %w", err)
	}
	files, err := c.List(ctx, settings, key)
	if err != nil {
		return fmt.Errorf("校验 CPA 调度状态失败: %w", err)
	}
	for _, file := range files {
		if !strings.EqualFold(file.Name, fileName) {
			continue
		}
		if file.Disabled || file.Unavailable || strings.EqualFold(strings.TrimSpace(file.Status), "disabled") {
			return errors.New("CPA auth 文件仍处于禁止调度状态")
		}
		return nil
	}
	return errors.New("恢复调度后未找到 CPA auth 文件")
}

func (c *Client) Groups(ctx context.Context, settings model.CPASettings, key string) ([]Group, error) {
	data, _, err := c.request(ctx, settings, key, http.MethodGet, "/v0/management/account-groups", nil, "")
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Groups []Group `json:"groups"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("解析 CPA 分组失败: %w", err)
	}
	return envelope.Groups, nil
}

func (c *Client) Delete(ctx context.Context, settings model.CPASettings, key, fileName string) error {
	_, _, err := c.request(ctx, settings, key, http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil, "")
	return err
}

type authFile struct {
	Name        string `json:"name"`
	AuthIndex   string `json:"auth_index"`
	AccountID   string `json:"chatgpt_account_id"`
	AccountID2  string `json:"account_id"`
	Status      string `json:"status"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
	CodexQuota  any    `json:"codex_quota_snapshots"`
}

func (c *Client) List(ctx context.Context, settings model.CPASettings, key string) ([]authFile, error) {
	data, _, err := c.request(ctx, settings, key, http.MethodGet, "/v0/management/auth-files", nil, "")
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Files []authFile `json:"files"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("解析 CPA auth-files 失败: %w", err)
	}
	return envelope.Files, nil
}

func (c *Client) Status(ctx context.Context, settings model.CPASettings, key, fileName string) (int, error) {
	files, err := c.List(ctx, settings, key)
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		if strings.EqualFold(f.Name, fileName) {
			if strings.TrimSpace(f.AuthIndex) == "" {
				return http.StatusOK, nil
			}
			accountID := f.AccountID
			if accountID == "" {
				accountID = f.AccountID2
			}
			// Only an actual proxied OpenAI response with HTTP 401 is a 401
			// signal.  CPA's cached disabled/unavailable flags can also represent
			// administrative or quota states and must not trigger relogin.
			_, status, callErr := c.apiCallWithStatus(ctx, settings, key, f.AuthIndex, accountID)
			if callErr != nil {
				if status > 0 {
					return status, nil
				}
				return 0, callErr
			}
			return status, nil
		}
	}
	return http.StatusNotFound, nil
}

func (c *Client) Quota(ctx context.Context, settings model.CPASettings, key, fileName string) (model.FreeQuotaWindow, model.FreeQuotaWindow, error) {
	files, err := c.List(ctx, settings, key)
	if err != nil {
		return model.FreeQuotaWindow{}, model.FreeQuotaWindow{}, err
	}
	for _, f := range files {
		if strings.EqualFold(f.Name, fileName) {
			// auth-files only contains cached tail-burst snapshots. Ask CPA to
			// proxy the official wham/usage request first so quota checks are live.
			if strings.TrimSpace(f.AuthIndex) != "" {
				accountID := f.AccountID
				if accountID == "" {
					accountID = f.AccountID2
				}
				if body, callErr := c.apiCall(ctx, settings, key, f.AuthIndex, accountID); callErr == nil {
					if five, seven, ok := parseRateLimitWindows(body); ok {
						return five, seven, nil
					}
				}
			}
			var five, seven model.FreeQuotaWindow
			walkQuota(f.CodexQuota, &five, &seven)
			// The CPA collector publishes {model: {used_ratio, window, ...}}
			// snapshots.  They have no window length, so primary/secondary are
			// mapped to the conventional 5h/7d windows and get stable defaults.
			walkCPASnapshots(f.CodexQuota, &five, &seven)
			return five, seven, nil
		}
	}
	return model.FreeQuotaWindow{}, model.FreeQuotaWindow{}, errors.New("CPA auth 文件不存在")
}

func (c *Client) apiCall(ctx context.Context, settings model.CPASettings, key, authIndex, accountID string) ([]byte, error) {
	body, _, err := c.apiCallWithStatus(ctx, settings, key, authIndex, accountID)
	return body, err
}

func (c *Client) apiCallWithStatus(ctx context.Context, settings model.CPASettings, key, authIndex, accountID string) ([]byte, int, error) {
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0 (Windows; x86_64) WindowsTerminal",
		"OpenAI-Beta":   "codex-1",
		"Originator":    "Codex Desktop",
	}
	if strings.TrimSpace(accountID) != "" {
		headers["Chatgpt-Account-Id"] = strings.TrimSpace(accountID)
	}
	payload := map[string]any{
		"authIndex":        strings.TrimSpace(authIndex),
		"ensureFreshToken": true,
		"method":           http.MethodGet,
		"url":              "https://chatgpt.com/backend-api/wham/usage",
		"header":           headers,
		"data":             "",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	response, _, err := c.request(ctx, settings, key, http.MethodPost, "/v0/management/api-call", bytes.NewReader(data), "application/json")
	if err != nil {
		return nil, 0, err
	}
	var envelope struct {
		StatusCode  int             `json:"status_code"`
		StatusCode2 int             `json:"statusCode"`
		Body        json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, 0, fmt.Errorf("解析 CPA api-call 响应失败: %w", err)
	}
	statusCode := envelope.StatusCode
	if statusCode == 0 {
		statusCode = envelope.StatusCode2
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, statusCode, fmt.Errorf("CPA usage HTTP %d", statusCode)
	}
	body := bytes.TrimSpace(envelope.Body)
	if len(body) > 0 && body[0] == '"' {
		var text string
		if err := json.Unmarshal(body, &text); err == nil {
			body = []byte(text)
		}
	}
	if !json.Valid(body) {
		return nil, statusCode, errors.New("CPA api-call 额度 body 不是有效 JSON")
	}
	return body, statusCode, nil
}

func parseRateLimitWindows(data []byte) (model.FreeQuotaWindow, model.FreeQuotaWindow, bool) {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return model.FreeQuotaWindow{}, model.FreeQuotaWindow{}, false
	}
	rateLimit := mapValue(payload, "rate_limit", "rateLimit")
	if rateLimit == nil {
		return model.FreeQuotaWindow{}, model.FreeQuotaWindow{}, false
	}
	// Do not rely on primary/secondary names for the window type. CPA can
	// expose a weekly allowance in either position. The duration (or reset
	// interval when duration is omitted) is authoritative.
	var five, seven model.FreeQuotaWindow
	for _, key := range []string{"primary_window", "primaryWindow", "secondary_window", "secondaryWindow"} {
		window, ok := parseRateLimitWindow(rateLimit[key])
		if !ok {
			continue
		}
		if rateLimitWindowIsWeekly(window) {
			seven = window
		} else {
			five = window
		}
	}
	return five, seven, five.LimitWindowSeconds > 0 || seven.LimitWindowSeconds > 0
}

func mapValue(value map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if child, ok := value[key].(map[string]any); ok {
			return child
		}
	}
	return nil
}

func parseRateLimitWindow(value any) (model.FreeQuotaWindow, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return model.FreeQuotaWindow{}, false
	}
	used := 0.0
	if raw := firstValue(object, "used_percent", "usedPercent"); raw != nil {
		used, _ = numberValue(raw)
	} else if raw := firstValue(object, "used_ratio", "usedRatio"); raw != nil {
		used, _ = numberValue(raw)
		// Snapshot-style ratios are represented in [0,1].
		if used >= 0 && used <= 1 {
			used *= 100
		}
	}
	limit := int64(numberOrZero(firstValue(object, "limit_window_seconds", "limitWindowSeconds")))
	resetAfter := int64(numberOrZero(firstValue(object, "reset_after_seconds", "resetAfterSeconds")))
	resetAt := parseUnixValue(firstValue(object, "reset_at", "resetAt"))
	if resetAfter <= 0 && resetAt > 0 {
		resetAfter = maxInt64(0, resetAt-time.Now().Unix())
	}
	if limit <= 0 {
		// Some CPA versions omit limit_window_seconds but return the reset
		// interval. Preserve that duration so the caller can classify 7D vs 5H.
		limit = resetAfter
	}
	if limit <= 0 {
		return model.FreeQuotaWindow{}, false
	}
	return model.FreeQuotaWindow{UsedPercent: used, LimitWindowSeconds: limit, ResetAfterSeconds: resetAfter, ResetAt: resetAt}, true
}

func firstValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

func numberOrZero(value any) float64 {
	n, _ := numberValue(value)
	return n
}

func rateLimitWindowIsWeekly(window model.FreeQuotaWindow) bool {
	return window.LimitWindowSeconds >= 6*24*60*60 || window.ResetAfterSeconds >= 24*60*60
}

func walkCPASnapshots(value any, five, seven *model.FreeQuotaWindow) {
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			walkCPASnapshots(child, five, seven)
		}
	case map[string]any:
		if used, ok := cpaSnapshotWindow(v); ok {
			if cpaSnapshotIsWeekly(v, used) {
				used.LimitWindowSeconds = 7 * 24 * 60 * 60
				*seven = used
			} else {
				used.LimitWindowSeconds = 5 * 60 * 60
				*five = used
			}
			return
		}
		for _, child := range v {
			walkCPASnapshots(child, five, seven)
		}
	}
}

func cpaSnapshotIsWeekly(v map[string]any, window model.FreeQuotaWindow) bool {
	name := strings.ToLower(strings.TrimSpace(stringValue(v["window"])))
	// The collector's primary/secondary names are positional and are not a
	// reliable duration classification: a weekly window can legitimately be
	// published as "primary" after CPA selects the weekly allowance.
	if resetAt := window.ResetAt; resetAt > 0 {
		remaining := resetAt - time.Now().Unix()
		if remaining >= 24*60*60 {
			return true
		}
		if remaining > 0 {
			return false
		}
	}
	if strings.Contains(name, "week") || name == "secondary" || name == "7d" || name == "weekly" {
		return true
	}
	// CPA's tail-burst snapshot is a single selected window and prefers weekly
	// over monthly. With no reset metadata, treating it as weekly preserves the
	// information CPA actually chose instead of incorrectly labeling it 5h.
	return name == "primary" || name == ""
}

func cpaSnapshotWindow(v map[string]any) (model.FreeQuotaWindow, bool) {
	raw, hasUsed := v["used_ratio"]
	if !hasUsed {
		return model.FreeQuotaWindow{}, false
	}
	usedRatio, ok := numberValue(raw)
	if !ok || usedRatio < 0 || usedRatio > 1 {
		return model.FreeQuotaWindow{}, false
	}
	resetAt := parseUnixValue(v["reset_at"])
	return model.FreeQuotaWindow{
		UsedPercent:        usedRatio * 100,
		LimitWindowSeconds: 0,
		ResetAt:            resetAt,
		ResetAfterSeconds:  maxInt64(0, resetAt-time.Now().Unix()),
	}, true
}

func numberValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func parseUnixValue(value any) int64 {
	if n, ok := numberValue(value); ok {
		if n > 1e12 {
			n /= 1000
		}
		if n > 0 {
			return int64(n)
		}
	}
	if s, ok := value.(string); ok {
		if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s)); err == nil {
			return ts.Unix()
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			if n > 1e12 {
				n /= 1000
			}
			return n
		}
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func walkQuota(value any, five, seven *model.FreeQuotaWindow) {
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			walkQuota(child, five, seven)
		}
	case map[string]any:
		if raw, ok := v["limit_window_seconds"]; ok {
			seconds, _ := raw.(float64)
			used, _ := v["used_percent"].(float64)
			resetAfter, _ := v["reset_after_seconds"].(float64)
			resetAt, _ := v["reset_at"].(float64)
			window := model.FreeQuotaWindow{UsedPercent: used, LimitWindowSeconds: int64(seconds), ResetAfterSeconds: int64(resetAfter), ResetAt: int64(resetAt)}
			if window.LimitWindowSeconds >= 6*24*60*60 {
				*seven = window
			} else if window.LimitWindowSeconds > 0 {
				*five = window
			}
		}
		for _, child := range v {
			walkQuota(child, five, seven)
		}
	}
}

func compact(data []byte) string {
	s := strings.Join(strings.Fields(string(data)), " ")
	if len(s) > 500 {
		return s[:500] + "..."
	}
	return s
}
