package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

func (s *Server) startMailAccountSIWC(w http.ResponseWriter, r *http.Request) {
	s.startMailAccountOAuthMode(w, r, "siwc")
}

func (s *Server) runMailAccountSIWC(jobID, email string) {
	unlock := s.lockFreeAccount("mail-oauth:" + email)
	defer unlock()
	s.updateOAuthJob(jobID, "running", "开始登录并获取 SIWC RT / AT")
	result, err := s.executeOpenAILogin(email, "siwc", func(message string) {
		s.updateOAuthJob(jobID, "running", message)
	}, nil)
	s.finishMailAccountSIWCJob(jobID, email, result, err)
}

func (s *Server) finishMailAccountSIWCJob(jobID, email string, result map[string]any, runErr error) {
	status, message := "failed", "SIWC 授权未完成"
	if runErr != nil {
		message = runErr.Error()
	} else if result != nil && result["success"] == true {
		var tokens model.SIWCCredentials
		raw, err := json.Marshal(result)
		if err == nil {
			err = json.Unmarshal(raw, &tokens)
		}
		if err == nil {
			err = s.store.SaveMailAccountSIWC(email, tokens)
		}
		if err != nil {
			message = err.Error()
		} else {
			status, message = "success", ""
		}
	} else if text, ok := result["error"].(string); ok && text != "" {
		message = text
	}
	if status != "success" && isDeadOAuthResult(result, message) {
		_ = s.store.MarkMailAccountDead(email, message)
	}
	if status == "success" {
		s.updateOAuthJob(jobID, "running", "SIWC RT / AT 已加密保存，可在查看 / 复制 / 导出 AT 中查看")
	}
	// Job polling exposes status only. Tokens are read through the credential endpoint.
	safe := map[string]any{"success": status == "success", "auth_mode": "siwc"}
	for _, key := range []string{"login_method", "login_method_label", "login_auth_status", "configured_login_mode", "selected_login_mode", "plan_use_enabled", "expires_at"} {
		if value, ok := result[key]; ok {
			safe[key] = value
		}
	}
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if job := s.oauthJobs[jobID]; job != nil {
		job["status"], job["state"], job["error"], job["result"] = status, status, message, safe
	}
}

func (s *Server) exportMailSIWCCredentials(w http.ResponseWriter, r *http.Request, email string) {
	_, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, "无法读取该邮件账号的凭证")
		return
	}
	c := credentials.SIWC
	if c == nil || c.AccessToken == "" || c.RefreshToken == "" || c.ClientID == "" {
		writeAPI(w, http.StatusConflict, nil, "该账号尚未获取完整 SIWC RT / AT")
		return
	}
	writeAPI(w, http.StatusOK, buildSIWCCredentialExport(*c), "")
}

// Matches sub2api SIWC BuildAccountCredentials and its account import format.
func buildSIWCCredentialExport(c model.SIWCCredentials) map[string]any {
	credentials := map[string]any{
		"access_token": c.AccessToken, "refresh_token": c.RefreshToken, "id_token": c.IDToken,
		"client_id": c.ClientID, "auth_mode": "siwc", "openai_auth_mode": "siwc", "token_type": "Bearer",
		"ext_agent_host_id": c.ExtAgentHostID, "subject": c.Subject, "email": c.Email,
		"expires_at": c.ExpiresAt, "granted_scopes": c.GrantedScopes, "plan_use_enabled": c.PlanUseEnabled,
		"siwc_web_access_token": c.WebAccessToken, "siwc_web_account_id": c.WebAccountID, "siwc_app_id": c.ClientID,
	}
	extra := sub2.SIWCAccountExtra(credentials)
	extra["email"], extra["source"], extra["last_refresh"] = c.Email, "space_siwc", c.SavedAt.UTC().Format(time.RFC3339)
	return map[string]any{"exported_at": time.Now().UTC().Format(time.RFC3339), "proxies": []any{}, "accounts": []any{
		map[string]any{"name": c.Email, "platform": "openai", "type": "oauth", "concurrency": 10, "priority": 1,
			"credentials": credentials, "extra": extra},
	}}
}
