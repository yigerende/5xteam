package workflow

import (
	"context"
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
	openAITokenURL     = "https://auth.openai.com/oauth/token"
	openAIClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIRefreshScope = "openid profile email"
)

type OAuthTokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func RefreshOAuthTokens(ctx context.Context, refreshToken string, settings model.Settings) (OAuthTokenSet, error) {
	return refreshOAuthTokensAt(ctx, openAITokenURL, refreshToken, settings)
}

func refreshOAuthTokensAt(ctx context.Context, endpoint, refreshToken string, settings model.Settings) (OAuthTokenSet, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return OAuthTokenSet{}, errors.New("母号未保存 Refresh Token")
	}
	client, err := newOpenAIHTTPClient(settings)
	if err != nil {
		return OAuthTokenSet{}, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {openAIClientID},
		"refresh_token": {refreshToken},
		"scope":         {openAIRefreshScope},
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
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return OAuthTokenSet{}, fmt.Errorf("OAuth 刷新响应不是有效 JSON（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			message = strings.TrimSpace(payload.Error)
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return OAuthTokenSet{}, fmt.Errorf("OAuth 刷新失败（HTTP %d）: %s", resp.StatusCode, limitText(message, 300))
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthTokenSet{}, errors.New("OAuth 刷新响应缺少 access_token")
	}
	expiresAt := time.Time{}
	if payload.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return OAuthTokenSet{
		AccessToken: strings.TrimSpace(payload.AccessToken), RefreshToken: strings.TrimSpace(payload.RefreshToken), ExpiresAt: expiresAt,
	}, nil
}
