package workflow

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

const maxResponseBytes = 64 << 10

type Client struct {
	baseURL   string
	settings  model.Settings
	http      *http.Client
	deviceID  string
	sessionID string
	python    string
}

type Response struct {
	StatusCode int
	Message    string
}

func NewClient(settings model.Settings) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(settings.BaseURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "https" && !(base.Scheme == "http" && isLoopbackHost(base.Hostname()))) {
		return nil, errors.New("API 基址必须使用 HTTPS；仅本机测试地址允许 HTTP")
	}
	httpClient, err := newOpenAIHTTPClient(settings)
	if err != nil {
		return nil, err
	}
	pythonPath, _ := exec.LookPath("python")
	return &Client{
		baseURL: strings.TrimRight(settings.BaseURL, "/"), settings: settings,
		http:      httpClient,
		deviceID:  newDeviceID(),
		sessionID: newDeviceID(),
		python:    pythonPath,
	}, nil
}

// newOpenAIHTTPClient is the single transport constructor for OpenAI and
// ChatGPT requests. A configured global proxy is therefore never optional at
// individual call sites.
func newOpenAIHTTPClient(settings model.Settings) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(settings.ProxyURL) != "" {
		proxy, err := ValidateProxyURL(settings.ProxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	timeout := time.Duration(settings.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func newDeviceID() string {
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return ""
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func ValidateProxyURL(value string) (*url.URL, error) {
	proxy, err := url.Parse(strings.TrimSpace(value))
	if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
		return nil, errors.New("代理地址必须是 http、https、socks5 或 socks5h URL")
	}
	return proxy, nil
}

// NormalizeProxyAddress accepts both a normal proxy URL and the common
// provider line format host:port:username:password.
func NormalizeProxyAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("代理地址不能为空")
	}
	if strings.Contains(value, "://") {
		proxy, err := ValidateProxyURL(value)
		if err != nil {
			return "", err
		}
		return proxy.String(), nil
	}
	parts := strings.SplitN(value, ":", 4)
	if len(parts) != 4 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" || parts[3] == "" {
		return "", errors.New("代理线路格式应为 host:port:username:password")
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("代理端口必须是 1 到 65535 的数字")
	}
	host := strings.TrimSpace(parts[0])
	if strings.ContainsAny(host, "/?#@[]") {
		return "", errors.New("代理主机格式无效")
	}
	proxy := &url.URL{Scheme: "http", Host: host + ":" + strconv.Itoa(port), User: url.UserPassword(parts[2], parts[3])}
	return proxy.String(), nil
}

func TestProxy(ctx context.Context, proxyURL, targetBaseURL string, timeout time.Duration) model.ProxyTestResult {
	result := model.ProxyTestResult{CheckedAt: time.Now()}
	proxy, err := ValidateProxyURL(proxyURL)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	target, err := url.Parse(strings.TrimRight(targetBaseURL, "/"))
	if err != nil || target.Host == "" {
		result.Message = "测试目标地址无效"
		return result
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	client := &http.Client{Transport: transport, Timeout: timeout}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 Proxy Connectivity Check")
		resp, requestErr := client.Do(req)
		result.LatencyMS = time.Since(started).Milliseconds()
		if requestErr == nil {
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			result.HTTPStatus = resp.StatusCode
			if resp.StatusCode == http.StatusProxyAuthRequired {
				result.Message = "代理认证失败（HTTP 407）"
				return result
			}
			result.Reachable = true
			result.Message = fmt.Sprintf("链路可达（HTTP %d）", resp.StatusCode)
			return result
		}
		err = requestErr
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	result.Message = redactProxySecret(friendlyNetworkError(err).Error(), proxy)
	return result
}

func redactProxySecret(message string, proxy *url.URL) string {
	if proxy == nil || proxy.User == nil {
		return message
	}
	if password, ok := proxy.User.Password(); ok && password != "" {
		message = strings.ReplaceAll(message, password, "***")
	}
	return message
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (c *Client) Invite(ctx context.Context, adminToken, teamID, email, seatType string) (Response, error) {
	flowID := newDeviceID()
	submissionID := newDeviceID()
	body := map[string]any{
		"email_addresses": []string{email},
		"flow_id":         flowID,
		"resend_emails":   true,
		"role":            c.settings.Role,
		"seat_type":       seatType,
		"submission_id":   submissionID,
	}
	return c.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(teamID)+"/invites", adminToken, teamID, body)
}

func (c *Client) Accept(ctx context.Context, userToken, teamID, userID string) (Response, error) {
	return c.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(teamID)+"/invites/accept", userToken, teamID, map[string]any{})
}

// Transfer moves the user's personal space into the team account. This is the
// dedicated transfer endpoint used by the original workflow script and is
// workspace-scoped through chatgpt-account-id.
func (c *Client) Transfer(ctx context.Context, userToken, teamID string) (Response, error) {
	body := map[string]any{
		"workspace_id":      teamID,
		"target_account_id": teamID,
		"transfer_personal": true,
	}
	return c.do(ctx, http.MethodPost, "/accounts/transfer", userToken, teamID, body)
}

func (c *Client) Kick(ctx context.Context, adminToken, teamID, userID string) (Response, error) {
	return c.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(teamID)+"/users/"+url.PathEscape(userID), adminToken, teamID, nil)
}

func (c *Client) CheckAccount(ctx context.Context, token, accountID string) (Response, error) {
	return c.do(ctx, http.MethodGet, "/me", token, accountID, nil)
}

// TeamSeatCapacity reads the Team member and invitation endpoints and
// normalizes the seat_type values used by ChatGPT (default/standard and
// prolite/premium). The endpoint response has varied between deployments, so
// totals/remaining are accepted from several common field names; when only
// records are returned, used is counted from those records.
func (c *Client) TeamSeatCapacity(ctx context.Context, token, teamID string) (model.AdminSeatCapacity, error) {
	var out model.AdminSeatCapacity
	var lastErr error
	// The Admin > Members page uses the subscription endpoint as the source of
	// truth for purchased and available seats. Its seat_capacity entries contain
	// the exact split between default (Standard) and prolite (Premium/5x).
	if subscription, err := c.getJSON(ctx, "/subscriptions?account_id="+url.QueryEscape(teamID), token, teamID); err == nil {
		parseSubscriptionCapacity(&out, subscription)
		if out.Standard.Total > 0 || out.Premium.Total > 0 || out.Standard.Remaining > 0 || out.Premium.Remaining > 0 {
			out.FetchedAt = time.Now()
			return out, nil
		}
	} else {
		lastErr = err
	}
	// Fallback for older workspaces/API responses that do not expose
	// subscriptions: derive used counts from members and pending invites.
	users, err := c.getJSON(ctx, "/accounts/"+url.PathEscape(teamID)+"/users", token, teamID)
	if err != nil {
		lastErr = err
		// The members list can be blocked independently of the seat summary
		// endpoint. Try the dedicated seat-type count endpoint before failing.
		if counts, countsErr := c.getJSON(ctx, "/accounts/"+url.PathEscape(teamID)+"/users/seat_type_counts", token, teamID); countsErr == nil {
			parseSeatTypeCounts(&out, counts)
			for _, b := range []*model.AdminSeatBucket{&out.Standard, &out.Premium} {
				if b.Total == 0 {
					b.Total = b.Used
				}
				if b.Remaining == 0 && b.Total > b.Used {
					b.Remaining = b.Total - b.Used
				}
			}
			out.FetchedAt = time.Now()
			return out, nil
		} else {
			lastErr = countsErr
		}
		return out, lastErr
	}
	invites, err := c.getJSON(ctx, "/accounts/"+url.PathEscape(teamID)+"/invites", token, teamID)
	if err != nil {
		lastErr = err
		// We can still return subscription-derived capacity when invitations
		// are unavailable; only the used count from pending invites is missing.
		if out.Standard.Total > 0 || out.Premium.Total > 0 {
			for _, b := range []*model.AdminSeatBucket{&out.Standard, &out.Premium} {
				if b.Remaining == 0 && b.Total > b.Used {
					b.Remaining = b.Total - b.Used
				}
			}
			out.FetchedAt = time.Now()
			return out, nil
		}
		return out, lastErr
	}
	userItems := jsonItems(users)
	missingRole := 0
	for _, item := range userItems {
		role := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["role"])))
		if role == "account-owner" || role == "account-admin" || role == "admin" || role == "owner" {
			continue
		}
		if role == "" {
			missingRole++
		}
		bucket := seatBucket(item)
		bucket.Used = 1
		setSeatBucket(&out, bucket.Type, bucket)
	}
	// The current users endpoint omits role and includes the workspace owner
	// in items. Preserve the established behavior of excluding that one admin.
	if missingRole > 0 && out.Standard.Used > 0 {
		out.Standard.Used--
	}
	for _, item := range jsonItems(invites) {
		bucket := seatBucket(item)
		bucket.Used = 1
		setSeatBucket(&out, bucket.Type, bucket)
	}
	applySeatTotals(&out, users)
	applySeatTotals(&out, invites)
	// The members page exposes assigned seat types through this official
	// endpoint. Use it only when the users/invites payload did not identify
	// seat types, avoiding double-counting normal responses.
	if out.Standard.Used == 0 && out.Premium.Used == 0 {
		if counts, countsErr := c.getJSON(ctx, "/accounts/"+url.PathEscape(teamID)+"/users/seat_type_counts", token, teamID); countsErr == nil {
			parseSeatTypeCounts(&out, counts)
		}
	}
	for _, b := range []*model.AdminSeatBucket{&out.Standard, &out.Premium} {
		if b.Remaining < 0 {
			b.Remaining = 0
		}
		if b.Total == 0 && b.Used > 0 {
			b.Total = b.Used
		}
		if b.Remaining == 0 && b.Total > b.Used {
			b.Remaining = b.Total - b.Used
		}
	}
	out.FetchedAt = time.Now()
	return out, nil
}

func parseSeatTypeCounts(out *model.AdminSeatCapacity, value map[string]any) {
	counts, _ := value["seat_type_counts"].(map[string]any)
	if counts == nil {
		return
	}
	out.Standard.Used = number(counts, "default")
	out.Premium.Used = number(counts, "prolite")
}

func parseSubscriptionCapacity(out *model.AdminSeatCapacity, value map[string]any) {
	entries, ok := value["seat_capacity"].([]any)
	if !ok {
		return
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(entry["type"])))
		if typ == "prolite" || typ == "premium" || typ == "5x" {
			typ = "premium"
		} else if typ == "default" || typ == "standard" {
			typ = "standard"
		} else {
			continue
		}
		bucket := &out.Standard
		if typ == "premium" {
			bucket = &out.Premium
		}
		bucket.Total = number(entry, "paid") + number(entry, "held")
		bucket.Remaining = number(entry, "available")
	}
	if assigned, ok := value["assigned"].(map[string]any); ok {
		out.Standard.Used = number(assigned, "default")
		out.Premium.Used = number(assigned, "prolite")
	}
}

func (c *Client) getJSON(ctx context.Context, path, token, accountID string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/admin/members?tab=members")
	req.Header.Set("oai-client-build-number", "10352280")
	req.Header.Set("oai-client-version", "prod-b3b60750c6e8740ba45b7a02b1a450a27240d14f")
	req.Header.Set("oai-language", "en-US")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Priority", "u=1, i")
	req.Header.Set("Sec-CH-UA", `"Not=A?Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
	if c.sessionID != "" {
		req.Header.Set("oai-session-id", c.sessionID)
	}
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("chatgpt-account-id", accountID)
	// ChatGPT's Team admin endpoints reject the abbreviated `Mozilla/5.0`
	// user agent with an HTML 403 page.  Use the same complete browser UA as
	// the protocol client (and as the Chrome-imprinted curl_cffi transport).
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	setOpenAITargetHeaders(req, path)
	if c.deviceID != "" {
		req.Header.Set("oai-device-id", c.deviceID)
	}
	var status int
	var data []byte
	if c.python != "" && strings.HasPrefix(c.baseURL, "https://") && strings.TrimSpace(c.settings.ProxyURL) != "" {
		status, data, err = c.browserDo(ctx, req, nil)
	} else {
		var resp *http.Response
		resp, err = c.http.Do(req)
		if err == nil {
			defer resp.Body.Close()
			status = resp.StatusCode
			data, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		}
	}
	if err != nil {
		return nil, friendlyNetworkError(err)
	}
	if len(data) > maxResponseBytes {
		data = data[:maxResponseBytes]
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, limitText(strings.TrimSpace(string(data)), 300))
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

type seatItem struct {
	Type                   string
	Used, Total, Remaining int
}

func jsonItems(value map[string]any) []map[string]any {
	for _, key := range []string{"items", "users", "members", "invites", "account_invites"} {
		if list, ok := value[key].([]any); ok {
			out := make([]map[string]any, 0, len(list))
			for _, raw := range list {
				if item, ok := raw.(map[string]any); ok {
					out = append(out, item)
				}
			}
			return out
		}
	}
	return nil
}

func seatBucket(item map[string]any) seatItem {
	t := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["seat_type"])))
	if t == "" {
		t = strings.ToLower(strings.TrimSpace(fmt.Sprint(item["seatType"])))
	}
	if t == "prolite" || t == "premium" || t == "5x" {
		t = "premium"
	} else {
		t = "standard"
	}
	return seatItem{Type: t, Total: number(item, "total", "total_seats", "max_seats", "seat_count"), Remaining: number(item, "remaining", "remaining_seats", "available_seats")}
}

func setSeatBucket(out *model.AdminSeatCapacity, typ string, item seatItem) {
	b := &out.Standard
	if typ == "premium" {
		b = &out.Premium
	}
	b.Used += item.Used
	if item.Total > b.Total {
		b.Total = item.Total
	}
	if item.Remaining > b.Remaining {
		b.Remaining = item.Remaining
	}
}
func applySeatTotals(out *model.AdminSeatCapacity, value map[string]any) {
	for _, key := range []string{"standard", "default", "premium", "prolite", "standard_seats", "default_seats", "premium_seats", "prolite_seats", "seat_limits", "seat_capacity", "capacity", "seats"} {
		if nested, ok := value[key].(map[string]any); ok {
			if key == "seat_limits" || key == "seat_capacity" || key == "capacity" || key == "seats" {
				applySeatTotals(out, nested)
				continue
			}
			typ := key
			if typ == "default" || typ == "standard_seats" || typ == "default_seats" {
				typ = "standard"
			}
			if typ == "prolite" || typ == "premium_seats" || typ == "prolite_seats" {
				typ = "premium"
			}
			setSeatBucket(out, typ, seatItem{Type: typ, Total: number(nested, "total", "total_seats", "max_seats", "seat_count"), Remaining: number(nested, "remaining", "remaining_seats", "available_seats")})
		}
	}
}
func number(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
	}
	return 0
}

func TestAdminAccount(ctx context.Context, token, teamAccountID string, settings model.Settings) model.AdminAccountTestResult {
	result := model.AdminAccountTestResult{CheckedAt: time.Now()}
	info, err := DecodeUserInfo(token)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	result.User = info
	if strings.TrimSpace(teamAccountID) == "" {
		teamAccountID = info.AccountID
	}
	client, err := NewClient(settings)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	started := time.Now()
	response, err := client.CheckAccount(ctx, token, teamAccountID)
	result.LatencyMS, result.HTTPStatus = time.Since(started).Milliseconds(), response.StatusCode
	if err != nil {
		result.Message = err.Error()
		return result
	}
	result.Valid, result.Message = true, response.Message
	return result
}

func (c *Client) do(ctx context.Context, method, path, token, accountID string, payload any) (Response, error) {
	var body io.Reader
	var bodyBytes []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return Response{}, err
		}
		bodyBytes = encoded
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("oai-language", "zh-CN")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("sec-ch-ua", `"Google Chrome";v="136", "Not.A/Brand";v="8", "Chromium";v="136"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/136.0.0.0 Safari/537.36")
	if c.deviceID != "" {
		req.Header.Set("oai-device-id", c.deviceID)
	}
	if strings.HasSuffix(path, "/invites") {
		req.Header.Set("Referer", "https://chatgpt.com/admin")
	}
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	if path == "/me" {
		req.Header.Set("x-openai-target-path", "/backend-api/me")
		req.Header.Set("x-openai-target-route", "/backend-api/me")
	}
	setOpenAITargetHeaders(req, path)
	var statusCode int
	var data []byte
	var requestErr error
	if c.python != "" && strings.HasPrefix(c.baseURL, "https://") && strings.TrimSpace(c.settings.ProxyURL) != "" {
		statusCode, data, requestErr = c.browserDo(ctx, req, bodyBytes)
	} else {
		var resp *http.Response
		resp, requestErr = c.http.Do(req)
		if requestErr == nil {
			defer resp.Body.Close()
			statusCode = resp.StatusCode
			data, requestErr = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		}
	}
	if requestErr != nil {
		return Response{StatusCode: statusCode}, friendlyNetworkError(requestErr)
	}
	if len(data) > maxResponseBytes {
		data = data[:maxResponseBytes]
	}
	message := responseMessage(data)
	result := Response{StatusCode: statusCode, Message: message}
	if statusCode < 200 || statusCode >= 300 {
		if message == "" || message == "请求成功" {
			message = limitText(strings.TrimSpace(string(data)), 500)
		}
		if message == "" {
			message = http.StatusText(statusCode)
		}
		return result, fmt.Errorf("HTTP %d: %s", statusCode, message)
	}
	if result.Message == "" {
		result.Message = "请求成功"
	}
	return result, nil
}

// ChatGPT's browser client marks backend-api calls with the target route. In
// particular, the Team admin endpoints (subscriptions, users and invites)
// return a generic 403 page when these headers are omitted, even with a valid
// bearer token and the correct proxy出口.
func setOpenAITargetHeaders(req *http.Request, path string) {
	route := path
	if index := strings.IndexByte(route, '?'); index >= 0 {
		route = route[:index]
	}
	if route == "" || route == "/me" {
		return
	}
	target := route
	if !strings.HasPrefix(target, "/backend-api/") {
		target = "/backend-api" + target
	}
	req.Header.Set("x-openai-target-path", target)
	req.Header.Set("x-openai-target-route", target)
	if strings.HasPrefix(route, "/accounts/") && strings.HasSuffix(route, "/invites") {
		req.Header.Set("Referer", "https://chatgpt.com/admin")
	}
}

// browserDo uses curl_cffi when a real HTTPS proxy is configured. ChatGPT's
// team endpoints apply stricter browser/TLS checks than /me, and the Chrome
// impersonation keeps those requests consistent with the web client.
func (c *Client) browserDo(ctx context.Context, req *http.Request, body []byte) (int, []byte, error) {
	headers := make(map[string]string, len(req.Header))
	for key, values := range req.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	input := map[string]any{
		"method": req.Method, "url": req.URL.String(), "headers": headers,
		"body": string(body), "proxy": c.settings.ProxyURL,
		"timeout": c.settings.RequestTimeoutSeconds,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	script := `import sys,json,base64
from curl_cffi import requests
p=json.load(sys.stdin)
s=requests.Session(impersonate="chrome136")
proxy=p.get("proxy","")
if proxy:
    s.proxies={"http":proxy,"https":proxy}
r=s.request(p["method"],p["url"],headers=p.get("headers",{}),data=p.get("body","") or None,timeout=p.get("timeout",45),allow_redirects=True)
print(json.dumps({"status":r.status_code,"body":base64.b64encode(r.content).decode("ascii")}))`
	command := exec.CommandContext(ctx, c.python, "-c", script)
	command.Stdin = bytes.NewReader(encoded)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, fmt.Errorf("浏览器请求失败: %s", limitText(strings.TrimSpace(string(output)), 1000))
	}
	var result struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, nil, fmt.Errorf("解析浏览器响应失败: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(result.Body)
	if err != nil {
		return result.Status, nil, fmt.Errorf("解码浏览器响应失败: %w", err)
	}
	return result.Status, data, nil
}

func responseMessage(data []byte) string {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") || strings.HasPrefix(lower, "<head") {
		return "上游返回 HTML 拒绝页，可能是代理出口或 ChatGPT 风控拦截"
	}
	var value map[string]any
	if json.Unmarshal(data, &value) == nil {
		for _, key := range []string{"message", "detail", "error"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				return limitText(text, 500)
			}
			if nested, ok := value[key].(map[string]any); ok {
				if text, ok := nested["message"].(string); ok {
					return limitText(text, 500)
				}
			}
		}
		// Preserve otherwise-structured validation errors (for example HTTP 422)
		// instead of reporting them as a misleading success message.
		compact, _ := json.Marshal(value)
		return limitText(string(compact), 500)
	}
	return limitText(trimmed, 500)
}

func limitText(value string, length int) string {
	runes := []rune(value)
	if len(runes) > length {
		return string(runes[:length]) + "..."
	}
	return value
}

func friendlyNetworkError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("任务已停止")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("请求超时")
	}
	return fmt.Errorf("网络请求失败: %w", err)
}

func isRetryableConnectionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "curl: (56)") || strings.Contains(message, "connection closed abruptly")
}
