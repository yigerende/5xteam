package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

func (s *Server) updateMailAccountCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	var input store.MailCredentialsPatch
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	for _, value := range []*string{input.TOTPSecret, input.AccessToken, input.RefreshToken, input.ChatGPTSession, input.AccountID} {
		if value != nil {
			*value = strings.TrimSpace(*value)
		}
	}
	if input.TOTPSecret != nil && *input.TOTPSecret != "" {
		if _, err := generateMailTOTP(*input.TOTPSecret, time.Now()); err != nil {
			writeAPI(w, http.StatusBadRequest, nil, "OpenAI 2FA 密钥格式无效")
			return
		}
	}
	if input.ChatGPTSession != nil && *input.ChatGPTSession != "" {
		var session map[string]json.RawMessage
		if err := json.Unmarshal([]byte(*input.ChatGPTSession), &session); err != nil || session == nil {
			writeAPI(w, http.StatusBadRequest, nil, "ChatGPT Session 必须是有效的 JSON 对象")
			return
		}
		if input.AccessToken == nil {
			for _, key := range []string{"accessToken", "access_token"} {
				var token string
				if json.Unmarshal(session[key], &token) == nil && strings.TrimSpace(token) != "" {
					token = strings.TrimSpace(token)
					input.AccessToken = &token
					break
				}
			}
		}
	}
	if input.AccessToken != nil && *input.AccessToken != "" {
		if info, err := workflow.DecodeUserInfo(*input.AccessToken); err == nil {
			if info.Email != "" && !strings.EqualFold(info.Email, email) {
				writeAPI(w, http.StatusBadRequest, nil, "AT 所属邮箱与当前账号不一致")
				return
			}
			if input.AccountID == nil {
				input.AccountID = &info.AccountID
			}
		}
	}
	profile, err := s.store.PatchMailCredentials(email, input)
	if err != nil {
		if errors.Is(err, store.ErrMailCredentialsChanged) {
			writeAPI(w, http.StatusConflict, nil, err.Error())
		} else {
			writeAPI(w, http.StatusBadRequest, nil, "保存账号凭证失败")
		}
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}
