package workflow

import (
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
	return requestOAuthTokenAt(ctx, endpoint, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {openAIClientID}, "code": {strings.TrimSpace(code)},
		"redirect_uri": {OpenAIOAuthRedirectURI}, "code_verifier": {strings.TrimSpace(verifier)},
	}, settings, "OAuth 授权码交换")
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
	client, err := newOpenAIHTTPClient(settings)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthTokenSet{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "codex_cli_rs")
	resp, err := client.Do(req)
	if err != nil {
		return OAuthTokenSet{}, friendlyNetworkError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return OAuthTokenSet{}, fmt.Errorf("读取 OAuth 刷新响应失败: %w", err)
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
		return OAuthTokenSet{}, fmt.Errorf("%s响应不是有效 JSON（HTTP %d）", operation, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			message = strings.TrimSpace(payload.Error)
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return OAuthTokenSet{}, fmt.Errorf("%s失败（HTTP %d）: %s", operation, resp.StatusCode, limitText(message, 300))
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthTokenSet{}, errors.New("OAuth 刷新响应缺少 access_token")
	}
	expiresAt := time.Time{}
	if payload.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return OAuthTokenSet{
		AccessToken: strings.TrimSpace(payload.AccessToken), RefreshToken: strings.TrimSpace(payload.RefreshToken), IDToken: strings.TrimSpace(payload.IDToken), ExpiresAt: expiresAt,
	}, nil
}
