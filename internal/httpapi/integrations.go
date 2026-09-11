package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

// importTurbRegistration receives only a completed registration result. The
// caller authenticates with the same admin credentials used by the local UI;
// the token is encrypted by Store before it is persisted.
func (s *Server) importTurbRegistration(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email            string          `json:"email"`
		Name             string          `json:"name"`
		AccessToken      string          `json:"access_token"`
		AccessTokenAlt   string          `json:"accessToken"`
		UserID           string          `json:"user_id"`
		AccountID        json.RawMessage `json:"account_id"`
		PlanType         string          `json:"plan_type"`
		RefreshToken     string          `json:"refresh_token"`
		OAuthAccessToken string          `json:"oauth_access_token"`
		OAuthAccountID   string          `json:"oauth_account_id"`
		MailPassword     string          `json:"mail_password"`
		GptPassword      string          `json:"gpt_password"`
		ClientID         string          `json:"client_id"`
		MailRefreshToken string          `json:"mail_refresh_token"`
		PickupURL        string          `json:"pickup_url"`
		TotpSecret       string          `json:"totp_secret"`
		ChatGPTSession   json.RawMessage `json:"chatgpt_session"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	token := workflow.ExtractAccessToken(input.AccessToken)
	if token == "" {
		token = workflow.ExtractAccessToken(input.AccessTokenAlt)
	}
	if token == "" {
		writeAPI(w, http.StatusBadRequest, nil, "注册结果缺少 access_token")
		return
	}
	info, err := workflow.DecodeUserInfo(token)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, "access_token 无效: "+err.Error())
		return
	}
	// This integration is deliberately not a Free-pipeline import. Store the
	// registration in the mailbox/OpenAI credential stores only; creating a
	// FreeAccountProfile here would put it in Team rotation.
	email := firstImportValue(strings.TrimSpace(input.Email), info.Email)
	userID := firstImportValue(strings.TrimSpace(input.UserID), info.UserID)
	accountID := firstImportValue(stringValue(input.AccountID), info.AccountID)
	chatgptSession, sessionErr := normalizeChatGPTSession(input.ChatGPTSession)
	if sessionErr != nil {
		writeAPI(w, http.StatusBadRequest, nil, "chatgpt_session 无效: "+sessionErr.Error())
		return
	}
	mailSaved := false
	if email != "" {
		mailCreds := model.MailAccountCredentials{Email: email, MailPassword: strings.TrimSpace(input.MailPassword), ClientID: strings.TrimSpace(input.ClientID), MailRefreshToken: strings.TrimSpace(input.MailRefreshToken), PickupURL: strings.TrimSpace(input.PickupURL), GptPassword: strings.TrimSpace(input.GptPassword), TotpSecret: strings.TrimSpace(input.TotpSecret), AccessToken: token, ChatGPTSession: chatgptSession}
		if _, old, oldErr := s.store.MailAccountCredential(email); oldErr == nil {
			if mailCreds.MailPassword == "" {
				mailCreds.MailPassword = old.MailPassword
			}
			if mailCreds.ClientID == "" {
				mailCreds.ClientID = old.ClientID
			}
			if mailCreds.MailRefreshToken == "" {
				mailCreds.MailRefreshToken = old.MailRefreshToken
			}
			if mailCreds.PickupURL == "" {
				mailCreds.PickupURL = old.PickupURL
			}
			if mailCreds.GptPassword == "" {
				mailCreds.GptPassword = old.GptPassword
			}
			if mailCreds.TotpSecret == "" {
				mailCreds.TotpSecret = old.TotpSecret
			}
			if mailCreds.ChatGPTSession == "" {
				mailCreds.ChatGPTSession = old.ChatGPTSession
			}
		}
		_, mailErr := s.store.SaveMailAccount(model.MailAccountProfile{Email: email, Label: email, Group: "free"}, mailCreds)
		if mailErr != nil {
			writeAPI(w, http.StatusBadRequest, nil, "邮箱资料保存失败: "+mailErr.Error())
			return
		}
		mailSaved = true
	}
	oauthAttached := false
	// Keep ChatGPT AT/RT in the standalone OpenAI account store. This is also
	// useful when RT is absent: the source AT is still available in Space.
	openAIID := ""
	for _, existing := range s.store.OpenAIAccounts() {
		if (userID != "" && existing.UserID == userID) || (accountID != "" && existing.AccountID == accountID) || (email != "" && strings.EqualFold(existing.Email, email)) {
			openAIID = existing.ID
			break
		}
	}
	openAIToken := token
	if strings.TrimSpace(input.OAuthAccessToken) != "" {
		openAIToken = strings.TrimSpace(input.OAuthAccessToken)
	}
	openAIProfile, openAIErr := s.store.SaveOpenAIAccount(model.OpenAIAccountProfile{
		ID: openAIID, Label: firstImportValue(email, accountID), Email: email,
		Name: firstImportValue(strings.TrimSpace(input.Name), info.Name), UserID: userID,
		AccountID: accountID, PlanType: firstImportValue(strings.TrimSpace(input.PlanType), info.PlanType),
	}, openAIToken, input.RefreshToken)
	if openAIErr != nil {
		writeAPI(w, http.StatusBadRequest, nil, "OpenAI 凭据保存失败: "+openAIErr.Error())
		return
	}
	oauthAttached = strings.TrimSpace(input.RefreshToken) != ""
	// Remove a legacy pure-import pipeline row, if one exists. Joined/team
	// records are protected by the store and are never deleted here.
	if userID != "" {
		if cleanupErr := s.store.DeletePureImportedFreeAccount(userID); cleanupErr != nil {
			writeAPI(w, http.StatusInternalServerError, nil, "清理旧的 Team 轮转记录失败: "+cleanupErr.Error())
			return
		}
	}
	writeAPI(w, http.StatusOK, map[string]any{"account": openAIProfile, "created": openAIID == "", "mail_account_saved": mailSaved, "oauth_attached": oauthAttached, "message": "注册完成，账号资料已保存（未进入 Team 轮转）"}, "")
}

func normalizeChatGPTSession(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	var session map[string]any
	if err := json.Unmarshal(raw, &session); err != nil {
		return "", err
	}
	if len(session) == 0 {
		return "", nil
	}
	canonical, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func stringValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func firstImportValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
