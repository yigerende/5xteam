package workflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

const (
	openAIAuthorizeURL     = "https://auth.openai.com/oauth/authorize"
	openAITokenURL         = "https://auth.openai.com/oauth/token"
	openAIClientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIRefreshScope     = "openid profile email"
	OpenAIOAuthRedirectURI = "http://localhost:1455/auth/callback"
)

type OAuthTokenSet struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
}

type PKCEAuthorization struct {
	State, SessionID, CodeVerifier, AuthorizationURL string
}

func GenerateOpenAIPKCEAuthorization() (PKCEAuthorization, error) {
	randomHex := func(size int) (string, error) {
		b := make([]byte, size)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b), nil
	}
	state, err := randomHex(32)
	if err != nil {
		return PKCEAuthorization{}, err
	}
	sessionID, err := randomHex(16)
	if err != nil {
		return PKCEAuthorization{}, err
	}
	verifier, err := randomHex(64)
	if err != nil {
		return PKCEAuthorization{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	params := url.Values{
		"response_type": {"code"}, "client_id": {openAIClientID}, "redirect_uri": {OpenAIOAuthRedirectURI},
		"scope": {"openid profile email offline_access"}, "state": {state}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"},
	}
	return PKCEAuthorization{State: state, SessionID: sessionID, CodeVerifier: verifier, AuthorizationURL: openAIAuthorizeURL + "?" + params.Encode()}, nil
}

func ExchangeOpenAIOAuthCode(ctx context.Context, code, verifier string, settings model.Settings) (OAuthTokenSet, error) {
	if strings.TrimSpace(settings.ProxyURL) == "" {
		return OAuthTokenSet{}, errors.New("请先配置全局代理；OpenAI OAuth 禁止直连")
	}
	return exchangeOpenAIOAuthCodeAt(ctx, openAITokenURL, code, verifier, settings)
}

func exchangeOpenAIOAuthCodeAt(ctx context.Context, endpoint, code, verifier string, settings model.Settings) (OAuthTokenSet, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {openAIClientID}, "code": {strings.TrimSpace(code)},
		"redirect_uri": {OpenAIOAuthRedirectURI}, "code_verifier": {strings.TrimSpace(verifier)},
	}
	// The browser-link flow already owns a PKCE session. Like the protocol
	// manager, token exchange retries network/5xx only, on the same proxy.
	for attempt := 1; ; attempt++ {
		result, err := requestOAuthTokenOnce(ctx, endpoint, form, settings, "OAuth 授权码交换")
		if err == nil || attempt == 3 || !isRetryableOAuthTokenError(err) {
			return result, err
		}
		var nonRetryable nonRetryableOAuthError
		if errors.As(err, &nonRetryable) || strings.Contains(err.Error(), "HTTP 429") {
			return result, err
		}
		select {
		case <-ctx.Done():
			return OAuthTokenSet{}, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

func RefreshOAuthTokens(ctx context.Context, refreshToken string, settings model.Settings) (OAuthTokenSet, error) {
	return refreshOAuthTokensAt(ctx, openAITokenURL, refreshToken, settings)
}

func refreshOAuthTokensAt(ctx context.Context, endpoint, refreshToken string, settings model.Settings) (OAuthTokenSet, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return OAuthTokenSet{}, errors.New("账号未保存 Refresh Token")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {openAIClientID},
		"refresh_token": {refreshToken},
		"scope":         {openAIRefreshScope},
	}
	return requestOAuthTokenAt(ctx, endpoint, form, settings, "OAuth 刷新")
}

func requestOAuthToken(ctx context.Context, form url.Values, settings model.Settings, operation string) (OAuthTokenSet, error) {
	return requestOAuthTokenAt(ctx, openAITokenURL, form, settings, operation)
}

func requestOAuthTokenAt(ctx context.Context, endpoint string, form url.Values, settings model.Settings, operation string) (OAuthTokenSet, error) {
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil {
		return OAuthTokenSet{}, parseErr
	}
	completeRounds := 1
	if strings.EqualFold(parsed.Hostname(), "auth.openai.com") {
		completeRounds = 3
		if strings.TrimSpace(settings.ProxyURL) == "" {
			return OAuthTokenSet{}, errors.New("请先配置全局代理；OpenAI OAuth 禁止直连")
		}
	}
	var lastErr error
	for round := 1; round <= completeRounds; round++ {
		qualityAttempts := 1
		if strings.EqualFold(parsed.Hostname(), "auth.openai.com") {
			qualityAttempts = 4
		}
		for quality := 1; quality <= qualityAttempts; quality++ {
			roundSettings := settings
			if round > 1 || quality > 1 {
				roundSettings.ProxyURL = rotateOAuthProxyURL(settings.ProxyURL, (round-1)*4+quality)
			}
			if strings.EqualFold(parsed.Hostname(), "auth.openai.com") {
				if probeErr := probeOAuthProxyQuality(ctx, roundSettings.ProxyURL); probeErr != nil {
					lastErr = probeErr
					continue
				}
			}
			for attempt := 1; attempt <= 3; attempt++ {
				result, err := requestOAuthTokenOnce(ctx, endpoint, form, roundSettings, operation)
				if err == nil {
					return result, nil
				}
				lastErr = err
				if !isRetryableOAuthTokenError(err) || attempt >= 3 {
					break
				}
			}
			if !isRetryableOAuthTokenError(lastErr) {
				return OAuthTokenSet{}, lastErr
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("OAuth 请求失败")
	}
	return OAuthTokenSet{}, lastErr
}

func requestOAuthTokenOnce(ctx context.Context, endpoint string, form url.Values, settings model.Settings, operation string) (OAuthTokenSet, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	var status int
	var data []byte
	if strings.EqualFold(parsed.Hostname(), "auth.openai.com") {
		status, data, err = requestOAuthTokenViaCurl(ctx, endpoint, form, settings.ProxyURL)
	} else {
		client, clientErr := newOpenAIHTTPClient(settings)
		if clientErr != nil {
			return OAuthTokenSet{}, clientErr
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if requestErr != nil {
			return OAuthTokenSet{}, requestErr
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "codex_cli_rs")
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			return OAuthTokenSet{}, friendlyNetworkError(requestErr)
		}
		status = resp.StatusCode
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		_ = resp.Body.Close()
	}
	if err != nil {
		return OAuthTokenSet{}, err
	}
	if len(data) > maxResponseBytes {
		return OAuthTokenSet{}, errors.New("OAuth 刷新响应过大")
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return OAuthTokenSet{}, fmt.Errorf("%s响应不是有效 JSON（HTTP %d）", operation, status)
	}
	if status < 200 || status >= 300 {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			message = strings.TrimSpace(payload.Error)
		}
		if message == "" {
			message = http.StatusText(status)
		}
		err := fmt.Errorf("%s失败（HTTP %d）: %s", operation, status, limitText(message, 300))
		if status >= 500 || status == http.StatusTooManyRequests {
			return OAuthTokenSet{}, err
		}
		return OAuthTokenSet{}, nonRetryableOAuthError{err}
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthTokenSet{}, nonRetryableOAuthError{errors.New("OAuth 刷新响应缺少 access_token")}
	}
	expiresAt := time.Time{}
	if payload.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return OAuthTokenSet{
		AccessToken: strings.TrimSpace(payload.AccessToken), RefreshToken: strings.TrimSpace(payload.RefreshToken), IDToken: strings.TrimSpace(payload.IDToken), ExpiresAt: expiresAt,
	}, nil
}

func requestOAuthTokenViaCurl(ctx context.Context, endpoint string, form url.Values, proxyURL string) (int, []byte, error) {
	python := FindPython()
	if python == "" {
		return 0, nil, errors.New("未找到 Python，无法执行 curl_cffi OAuth Token 请求")
	}
	script := FindInternalScript("protocol_oauth_token.py")
	if script == "" {
		return 0, nil, errors.New("未找到 curl_cffi OAuth Token 脚本")
	}
	values := make(map[string]string, len(form))
	for key, items := range form {
		if len(items) > 0 {
			values[key] = items[0]
		}
	}
	input, err := json.Marshal(map[string]any{
		"endpoint": endpoint,
		"proxy":    proxyURL,
		"form":     values,
		"timeout":  60,
	})
	if err != nil {
		return 0, nil, err
	}
	command := exec.CommandContext(ctx, python, script)
	command.Stdin = bytes.NewReader(input)
	command.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, fmt.Errorf("curl_cffi OAuth Token 请求失败: %w", err)
	}
	var result struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		return 0, nil, fmt.Errorf("解析 curl_cffi OAuth Token 响应失败: %w", err)
	}
	if result.Status == 0 {
		if strings.TrimSpace(result.Error) == "" {
			result.Error = "curl_cffi OAuth Token 请求失败"
		}
		return 0, nil, errors.New(result.Error)
	}
	body, err := base64.StdEncoding.DecodeString(result.Body)
	if err != nil {
		return result.Status, nil, fmt.Errorf("解码 curl_cffi OAuth Token 响应失败: %w", err)
	}
	return result.Status, body, nil
}

func FindInternalScript(name string) string {
	cwd, _ := os.Getwd()
	candidates := []string{
		filepath.Join(cwd, "internal", name),
		filepath.Join(cwd, name),
		filepath.Join(cwd, "..", name),
		filepath.Join(cwd, "..", "..", name),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

type nonRetryableOAuthError struct{ error }

func isRetryableOAuthTokenError(err error) bool {
	if err == nil {
		return false
	}
	var nonRetryable nonRetryableOAuthError
	if errors.As(err, &nonRetryable) {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"could not resolve proxy", "resolve proxy", "proxy connect", "proxy connection",
		"connection reset", "connection aborted", "connection refused", "connection closed",
		"timed out", "timeout", "empty reply", "recv failure", "send failure",
		"network error", "temporarily unavailable", "http 429", "http 500", "http 502",
		"http 503", "http 504", "代理出口", "代理质检",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func rotateOAuthProxyURL(proxyURL string, round int) string {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.User == nil {
		return proxyURL
	}
	username := parsed.User.Username()
	lower := strings.ToLower(username)
	index := strings.Index(lower, "session-")
	if index < 0 {
		return proxyURL
	}
	start := index + len("session-")
	end := start
	for end < len(username) {
		ch := username[end]
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			break
		}
		end++
	}
	if end == start {
		return proxyURL
	}
	token := fmt.Sprintf("%08d", (time.Now().UnixNano()/1e6+int64(round*7919))%100000000)
	username = username[:start] + token + username[end:]
	if password, ok := parsed.User.Password(); ok {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = url.User(username)
	}
	return parsed.String()
}

func probeOAuthProxyQuality(ctx context.Context, proxyURL string) error {
	python := FindPython()
	if python == "" {
		return errors.New("未找到 Python，无法进行 OAuth 代理质检")
	}
	script := FindInternalScript("protocol_proxy_probe.py")
	if script == "" {
		return errors.New("未找到 OAuth 代理质检脚本")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, python, script, proxyURL)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	output, err := cmd.Output()
	if err != nil {
		if probeCtx.Err() != nil {
			return probeCtx.Err()
		}
		return fmt.Errorf("OAuth 代理质检执行失败: %w", err)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		return fmt.Errorf("解析 OAuth 代理质检结果失败: %w", err)
	}
	if !result.OK {
		if strings.TrimSpace(result.Error) == "" {
			result.Error = "代理出口质检未通过"
		}
		return errors.New(result.Error)
	}
	return nil
}
