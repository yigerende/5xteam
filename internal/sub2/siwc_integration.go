package sub2

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func SIWCAccountExtra(credentials map[string]any) map[string]any {
	if credentials["auth_mode"] != "siwc" && credentials["openai_auth_mode"] != "siwc" {
		return nil
	}
	return map[string]any{"auth_provider": "siwc", "import_source": "siwc", "codex_fingerprint_mode": "off"}
}

type SIWCAuthURL struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	HostID    string `json:"ext_agent_host_id"`
}

// Stored only in Space's encrypted pending push record, never in public progress.
type SIWCImportInput struct {
	SessionID      string `json:"session_id"`
	CallbackURL    string `json:"callback_url"`
	Email          string `json:"email"`
	HostID         string `json:"host_id"`
	WebAccessToken string `json:"web_access_token"`
	WebAccountID   string `json:"web_account_id"`
}

func safeSIWCError(err error) error {
	var response *HTTPError
	if errors.As(err, &response) {
		return fmt.Errorf("SIWC s2接口请求失败（HTTP %d），请检查目标站授权接口和导入状态", response.Status)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("SIWC s2接口连接或响应异常；保留原请求，重试时核对原导入结果")
}

func (c *Client) GenerateSIWCAuthURL(ctx context.Context, settings model.Sub2Settings, password, email, hostID, proxy string) (SIWCAuthURL, error) {
	raw, err := c.doJSON(ctx, settings, password, http.MethodPost, "/api/v1/admin/openai/siwc/integration/generate-auth-url", map[string]any{"email": email, "ext_agent_host_id": hostID, "proxy_url": proxy}, nil)
	if err != nil {
		return SIWCAuthURL{}, safeSIWCError(err)
	}
	var result SIWCAuthURL
	if json.Unmarshal(raw, &result) != nil || result.AuthURL == "" || result.SessionID == "" || result.HostID != hostID {
		return result, errors.New("SIWC s2接口授权链接响应不完整")
	}
	return result, nil
}

func (c *Client) ImportSIWCCallback(ctx context.Context, settings model.Sub2Settings, password string, input CreateAccountInput, key string) (Account, error) {
	request := input.SIWCImport
	if request == nil || len(uniquePositiveIDs(input.GroupIDs)) == 0 {
		return Account{}, errors.New("SIWC 回调或推送分组缺失")
	}
	body := map[string]any{"session_id": request.SessionID, "web_access_token": request.WebAccessToken, "web_account_id": request.WebAccountID, "callback_url": request.CallbackURL, "email": request.Email, "name": input.Name,
		"group_ids": uniquePositiveIDs(input.GroupIDs), "concurrency": max(1, input.Concurrency), "priority": max(1, input.Priority), "models": input.Models, "cpa_ws": input.CpaWS}
	if input.ProxyID != nil {
		body["proxy_id"] = *input.ProxyID
	}
	raw, err := c.doJSON(ctx, settings, password, http.MethodPost, "/api/v1/admin/openai/siwc/integration/callback", body, map[string]string{"Idempotency-Key": key})
	if err != nil {
		return Account{}, safeSIWCError(err)
	}
	var result struct {
		Account Account               `json:"account"`
		Tokens  model.SIWCCredentials `json:"tokens"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return Account{}, errors.New("SIWC s2接口回调响应无效")
	}
	t := &result.Tokens
	if result.Account.ID < 1 || result.Account.Extra["auth_provider"] != "siwc" || t.AuthMode != "siwc" || !strings.EqualFold(strings.TrimSpace(t.Email), strings.TrimSpace(request.Email)) || t.ExtAgentHostID != request.HostID || t.AccessToken == "" || t.RefreshToken == "" || t.IDToken == "" || t.Subject == "" || t.ClientID == "" || t.ClientID == "dynamic_agent_client" || t.ExpiresAt <= time.Now().Unix() || !t.PlanUseEnabled {
		return Account{}, errors.New("SIWC s2接口返回的账号标记或凭证校验失败；保留原导入请求")
	}
	t.WebAccessToken, t.WebAccountID = request.WebAccessToken, request.WebAccountID
	result.Account.SIWCTokens = t
	return result.Account, nil
}
