package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

var mailSecretFields = []string{
	"mail_password", "client_id", "mail_refresh_token", "gpt_password", "totp_secret",
	"pickup_url", "access_token", "refresh_token", "id_token", "session_token",
	"chatgpt_session",
}

func mailSecretPresent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case nil:
		return false
	default:
		encoded, err := json.Marshal(typed)
		return err == nil && string(encoded) != "null" && string(encoded) != "{}" && string(encoded) != "[]"
	}
}

func importedString(raw map[string]any, key string) string {
	v, ok := raw[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func findLocalMailAccount(s *store.Store, email string) (model.MailAccountProfile, bool) {
	for _, item := range s.MailAccounts() {
		if strings.EqualFold(item.Email, email) {
			return item, true
		}
	}
	return model.MailAccountProfile{}, false
}

func redactMailPayload(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range mailSecretFields {
			if raw, ok := typed[key]; ok {
				typed[key+"_present"] = mailSecretPresent(raw)
				delete(typed, key)
			}
		}
		for _, child := range typed {
			redactMailPayload(child)
		}
	case []any:
		for _, child := range typed {
			redactMailPayload(child)
		}
	}
}

func (s *Server) mailStatus(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeAPI(w, http.StatusOK, map[string]any{
		"available": true,
		"url":       "local",
		"error":     "",
	}, "")
}

func (s *Server) listMailAccounts(w http.ResponseWriter, r *http.Request) {
	// 邮箱管理属于 Space Console 自身数据，不再依赖外部管理器。
	if local := s.store.MailAccountsByManagementScope("mail"); local != nil {
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
		items := make([]any, 0, len(local))
		for _, item := range local {
			if query != "" && !strings.Contains(strings.ToLower(item.Email+" "+item.Label), query) {
				continue
			}
			// OAuth is owned by the Team pipeline after Codex authorization.
			// Backfill the mailbox projection for records created before the
			// synchronization hook was added, then return the fresh profile.
			for _, free := range s.store.FreeAccounts() {
				if !strings.EqualFold(strings.TrimSpace(free.Email), strings.TrimSpace(item.Email)) || !free.OAuthRefreshTokenPresent {
					continue
				}
				if _, oauth, oauthErr := s.store.FreeAccountCredential(free.ID); oauthErr == nil && strings.TrimSpace(oauth.OAuthRefreshToken) != "" {
					_ = s.store.SaveMailAccountOAuth(item.Email, oauth.OAuthAccessToken, oauth.OAuthRefreshToken)
					if refreshed, _, refreshedErr := s.store.MailAccountCredential(item.Email); refreshedErr == nil {
						item = refreshed
					}
				}
				break
			}
			items = append(items, item)
		}
		writeAPI(w, http.StatusOK, map[string]any{"success": true, "items": items, "total": len(items), "counts": map[string]int{"all": len(items)}}, "")
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"success": true, "items": []any{}, "total": 0, "counts": map[string]int{"all": 0}}, "")
}

func (s *Server) checkMailAccountAT(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PathValue("email"))
	_, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	if strings.TrimSpace(credentials.AccessToken) == "" {
		writeAPI(w, http.StatusBadRequest, nil, "该邮箱账号没有已保存的 AT")
		return
	}
	result := workflow.TestAdminAccount(r.Context(), credentials.AccessToken, "", s.store.Settings())
	updated, updateErr := s.store.UpdateMailAccountATStatus(email, result.CheckedAt, result.Valid, result.HTTPStatus, result.Message)
	if updateErr != nil {
		writeAPI(w, http.StatusInternalServerError, nil, updateErr.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"account": updated, "result": result}, "")
}

func (s *Server) checkMailAccountsAT(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emails []string `json:"emails"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &input, 1<<20); err != nil {
			return
		}
	}
	emails := input.Emails
	if len(emails) == 0 {
		for _, account := range s.store.MailAccountsByManagementScope("mail") {
			emails = append(emails, account.Email)
		}
	}
	items := make([]any, 0, len(emails))
	errorsByEmail := map[string]string{}
	for _, email := range emails {
		profile, credentials, err := s.store.MailAccountCredential(email)
		if err != nil {
			errorsByEmail[email] = err.Error()
			continue
		}
		if strings.TrimSpace(credentials.AccessToken) == "" {
			errorsByEmail[email] = "没有已保存的 AT"
			continue
		}
		result := workflow.TestAdminAccount(r.Context(), credentials.AccessToken, "", s.store.Settings())
		updated, updateErr := s.store.UpdateMailAccountATStatus(profile.Email, result.CheckedAt, result.Valid, result.HTTPStatus, result.Message)
		if updateErr != nil {
			errorsByEmail[email] = updateErr.Error()
			continue
		}
		items = append(items, map[string]any{"account": updated, "result": result})
	}
	writeAPI(w, http.StatusOK, map[string]any{"items": items, "errors": errorsByEmail}, "")
}

func (s *Server) importMailAccounts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Accounts []map[string]any `json:"accounts"`
		Group    string           `json:"group"`
	}
	if err := decodeJSON(w, r, &input, 16<<20); err != nil {
		return
	}
	if len(input.Accounts) == 0 || len(input.Accounts) > 1000 {
		writeAPI(w, http.StatusBadRequest, nil, "单次请导入 1 到 1000 个邮件账号")
		return
	}
	imported, updated := 0, 0
	for _, raw := range input.Accounts {
		email := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw["email"])))
		if !strings.Contains(email, "@") {
			continue
		}
		creds := model.MailAccountCredentials{Email: email, MailPassword: importedString(raw, "mail_password"), ClientID: importedString(raw, "client_id"), MailRefreshToken: importedString(raw, "mail_refresh_token"), PickupURL: importedString(raw, "pickup_url"), GptPassword: importedString(raw, "gpt_password"), TotpSecret: importedString(raw, "totp_secret"), AccessToken: importedString(raw, "access_token")}
		_, existed := findLocalMailAccount(s.store, email)
		_, err := s.store.SaveMailAccount(model.MailAccountProfile{Email: email, Label: email, Group: strings.TrimSpace(input.Group)}, creds)
		if err != nil {
			writeAPI(w, http.StatusBadRequest, nil, err.Error())
			return
		}
		if existed {
			updated++
		} else {
			imported++
		}
	}
	writeAPI(w, http.StatusOK, map[string]any{"success": true, "imported": imported, "updated": updated, "skipped": 0}, "")
}

func (s *Server) deleteMailAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PathValue("email"))
	if !strings.Contains(email, "@") {
		writeAPI(w, http.StatusBadRequest, nil, "邮箱地址无效")
		return
	}
	if err := s.store.DeleteMailAccount(email); err == nil {
		writeAPI(w, http.StatusOK, map[string]any{"deleted": true, "pipeline_deleted": 0}, "")
		return
	} else if _, ok := findLocalMailAccount(s.store, email); ok {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusNotFound, nil, "邮箱账号不存在")
}

func (s *Server) startMailFetch(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	emails := []string{}
	if raw, ok := input["emails"].([]any); ok {
		for _, v := range raw {
			if e := strings.TrimSpace(fmt.Sprint(v)); e != "" {
				emails = append(emails, e)
			}
		}
	}
	if len(emails) == 0 {
		for _, a := range s.store.MailAccountsByManagementScope("mail") {
			emails = append(emails, a.Email)
		}
	}
	id := randomRegistrationID()
	job := map[string]any{"job_id": id, "status": "queued", "state": "queued", "total": len(emails), "processed": 0, "result": map[string]any{"summary": map[string]any{"ok": 0, "messages": 0}}}
	s.mailFetchMu.Lock()
	s.mailFetchJobs[id] = job
	s.mailFetchMu.Unlock()
	go s.runLocalMailFetch(id, emails)
	writeAPI(w, http.StatusAccepted, map[string]any{"job": cloneRegistrationJob(job)}, "")
}

func (s *Server) mailFetchStatus(w http.ResponseWriter, r *http.Request) {
	s.mailFetchMu.RLock()
	job, ok := s.mailFetchJobs[r.PathValue("id")]
	if ok {
		job = cloneRegistrationJob(job)
	}
	s.mailFetchMu.RUnlock()
	if !ok {
		writeAPI(w, http.StatusNotFound, nil, "收件任务不存在")
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"job": cloneRegistrationJob(job)}, "")
}

func (s *Server) listMailMessages(w http.ResponseWriter, r *http.Request) {
	items := s.store.MailMessages(r.URL.Query().Get("query"), r.URL.Query().Get("mail_type"))
	writeAPI(w, http.StatusOK, map[string]any{"success": true, "messages": items, "total": len(items)}, "")
}

func (s *Server) deleteMailMessage(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	if err := decodeJSON(w, r, &input, 4<<20); err != nil {
		return
	}
	ids := []string{}
	if raw, ok := input["ids"].([]any); ok {
		for _, v := range raw {
			ids = append(ids, fmt.Sprint(v))
		}
	}
	if err := s.store.DeleteMailMessages(ids); err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"deleted": len(ids)}, "")
}

func (s *Server) runLocalMailFetch(id string, emails []string) {
	s.updateMailFetch(id, "running", "开始收取邮箱")
	okCount, msgCount := 0, 0
	client := &http.Client{Timeout: 20 * time.Second}
	for i, email := range emails {
		_, creds, err := s.store.MailAccountCredential(email)
		if err == nil && creds.PickupURL != "" {
			u := creds.PickupURL
			if strings.Contains(u, "?") {
				u += "&json=1"
			} else {
				u += "?json=1"
			}
			if req, e := http.NewRequest(http.MethodGet, u, nil); e == nil {
				if resp, e := client.Do(req); e == nil {
					raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					resp.Body.Close()
					var payload map[string]any
					_ = json.Unmarshal(raw, &payload)
					code, subject, body := extractMailPayload(payload, string(raw))
					if code != "" || body != "" {
						m := model.MailMessage{ID: randomRegistrationID(), Account: email, Source: "directurl", Subject: subject, Text: body, Code: code, ReceivedAt: time.Now().UTC()}
						if s.store.SaveMailMessage(m) == nil {
							msgCount++
						}
					}
					okCount++
				}
			}
		}
		s.mailFetchMu.Lock()
		if j := s.mailFetchJobs[id]; j != nil {
			j["processed"] = i + 1
			j["current_email"] = email
		}
		s.mailFetchMu.Unlock()
	}
	s.mailFetchMu.Lock()
	if j := s.mailFetchJobs[id]; j != nil {
		j["status"], j["state"] = "success", "success"
		j["result"] = map[string]any{"summary": map[string]any{"ok": okCount, "messages": msgCount}}
	}
	s.mailFetchMu.Unlock()
}

func (s *Server) updateMailFetch(id, state, message string) {
	s.mailFetchMu.Lock()
	defer s.mailFetchMu.Unlock()
	if j := s.mailFetchJobs[id]; j != nil {
		j["status"], j["state"], j["message"] = state, state, message
	}
}

func extractMailPayload(payload map[string]any, raw string) (code, subject, body string) {
	code = extractOTP(payload, raw)
	for _, k := range []string{"subject", "title", "Subject"} {
		if x, ok := payload[k].(string); ok && strings.TrimSpace(x) != "" {
			subject = x
			break
		}
	}
	for _, k := range []string{"text", "body", "bodyPreview", "body_preview", "content", "html", "message"} {
		if x, ok := payload[k].(string); ok && strings.TrimSpace(x) != "" {
			body = x
			break
		}
	}
	if body == "" {
		if d, ok := payload["data"].(map[string]any); ok {
			for _, k := range []string{"text", "body", "bodyPreview", "body_preview", "content", "html", "message"} {
				if x, ok := d[k].(string); ok && strings.TrimSpace(x) != "" {
					body = x
					break
				}
			}
		}
	}
	return
}

func (s *Server) startMailAccountLogin(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PathValue("email"))
	if !strings.Contains(email, "@") {
		writeAPI(w, http.StatusBadRequest, nil, "邮箱地址无效")
		return
	}
	if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
		writeAPI(w, http.StatusBadRequest, nil, "请先在接口设置中配置全局代理；OpenAI 登录请求禁止直连")
		return
	}
	job := s.startLocalLogin(email)
	writeAPI(w, http.StatusAccepted, map[string]any{"success": true, "job": job}, "")
}

// startMailAccountOAuth runs Codex OAuth directly from Mail Management. It
// does not require or create a Team rotation record.
func (s *Server) startMailAccountOAuth(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if !strings.Contains(email, "@") {
		writeAPI(w, http.StatusBadRequest, nil, "邮箱地址无效")
		return
	}
	if _, _, err := s.store.MailAccountCredential(email); err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
		writeAPI(w, http.StatusBadRequest, nil, "请先在接口设置中配置全局代理；OpenAI OAuth 请求禁止直连")
		return
	}
	lockKey := "mail-oauth:" + email
	unlock := s.lockFreeAccount(lockKey)
	defer unlock()
	s.oauthMu.Lock()
	duplicate := false
	for _, existing := range s.oauthJobs {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(existing["email"])), email) && (fmt.Sprint(existing["status"]) == "queued" || fmt.Sprint(existing["status"]) == "running") {
			duplicate = true
			break
		}
	}
	if duplicate {
		s.oauthMu.Unlock()
		writeAPI(w, http.StatusConflict, nil, "该账号的 Codex OAuth 正在处理中")
		return
	}
	jobID := randomRegistrationID()
	job := map[string]any{"job_id": jobID, "email": email, "trigger": "mail_oauth", "mail_only": true, "status": "queued", "state": "queued", "logs": []any{}, "error": "", "result": nil}
	s.oauthJobs[jobID] = job
	s.oauthMu.Unlock()
	go s.runMailAccountOAuth(jobID, email)
	writeAPI(w, http.StatusAccepted, map[string]any{"success": true, "job": cloneRegistrationJob(job)}, "")
}

func (s *Server) runMailAccountOAuth(jobID, email string) {
	unlock := s.lockFreeAccount("mail-oauth:" + email)
	defer unlock()
	result, err := s.executeCodexOAuth(email, func(message string) {
		s.updateOAuthJob(jobID, "running", message)
	}, nil)
	s.finishMailAccountOAuthJob(jobID, email, result, err)
}

func (s *Server) finishMailAccountOAuthJob(jobID, email string, result map[string]any, runErr error) {
	status, message := "success", ""
	if runErr != nil || result == nil || result["success"] != true {
		status = "failed"
		if runErr != nil {
			message = runErr.Error()
		} else {
			message, _ = result["error"].(string)
		}
	}
	if status == "success" {
		accessToken, _ := result["access_token"].(string)
		refreshToken, _ := result["refresh_token"].(string)
		if strings.TrimSpace(accessToken) == "" || strings.TrimSpace(refreshToken) == "" {
			status, message = "failed", "OAuth 结果缺少 Access Token 或 Refresh Token"
		} else if err := s.store.SaveMailAccountOAuth(email, accessToken, refreshToken); err != nil {
			status, message = "failed", err.Error()
		}
	}
	if status != "success" && isDeadOAuthResult(result, message) {
		if strings.TrimSpace(message) == "" {
			message = "OpenAI 返回账号已删除或停用"
		}
		_ = s.store.MarkMailAccountDead(email, message)
	}
	s.oauthMu.Lock()
	if job := s.oauthJobs[jobID]; job != nil {
		job["status"], job["state"], job["error"], job["result"] = status, status, message, result
	}
	s.oauthMu.Unlock()
}

func (s *Server) startMailAccountRegistration(w http.ResponseWriter, r *http.Request) {
	s.startMailAccountRegistrationMode(w, r, false)
}

func (s *Server) startMailAccountRegistrationWithAT(w http.ResponseWriter, r *http.Request) {
	s.startMailAccountRegistrationMode(w, r, true)
}

func (s *Server) startMailAccountRegistrationMode(w http.ResponseWriter, r *http.Request, fetchAT bool) {
	email := strings.TrimSpace(r.PathValue("email"))
	if !strings.Contains(email, "@") {
		writeAPI(w, http.StatusBadRequest, nil, "邮箱地址无效")
		return
	}
	proxyURL := strings.TrimSpace(s.store.Settings().ProxyURL)
	if proxyURL == "" {
		writeAPI(w, http.StatusBadRequest, nil, "请先在接口设置中配置全局代理；OpenAI 注册请求禁止直连")
		return
	}
	if !fetchAT {
		job := s.startLocalRegistration(email)
		writeAPI(w, http.StatusAccepted, map[string]any{"success": true, "job": job}, "")
		return
	}
	writeAPI(w, http.StatusNotImplemented, nil, "注册并获取 AT 尚未迁移到本地协议")
}

func (s *Server) exportMailAccountCredentials(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if !strings.Contains(email, "@") {
		writeAPI(w, http.StatusBadRequest, nil, "邮箱地址无效")
		return
	}
	credentials, expiresAt, err := s.prepareMailGPTCredentials(r.Context(), email)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	exportedAt := time.Now().UTC()
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "", "raw":
		writeAPI(w, http.StatusOK, map[string]any{
			"email":              credentials.Email,
			"gpt_password":       credentials.GPTPassword,
			"access_token":       credentials.AccessToken,
			"refresh_token":      credentials.RefreshToken,
			"chatgpt_account_id": credentials.AccountID,
			"chatgpt_session":    chatGPTSessionValue(credentials.ChatGPTSession),
		}, "")
	case "cpa":
		if credentials.RefreshToken == "" {
			writeAPI(w, http.StatusConflict, nil, "该账号尚未获取 Codex RT，不能导出 CPA JSON")
			return
		}
		writeAPI(w, http.StatusOK, buildCPACredentialExport(credentials, expiresAt, exportedAt), "")
	case "sub2":
		if credentials.RefreshToken == "" {
			writeAPI(w, http.StatusConflict, nil, "该账号尚未获取 Codex RT，不能导出 Sub2 JSON")
			return
		}
		writeAPI(w, http.StatusOK, buildSub2CredentialExport(credentials, expiresAt, exportedAt), "")
	default:
		writeAPI(w, http.StatusBadRequest, nil, "凭证格式只能是 raw、cpa 或 sub2")
	}
}

type batchMailCredentialExportInput struct {
	Emails []string `json:"emails"`
	Format string   `json:"format"`
}

type preparedMailCredentialExport struct {
	Credentials mailGPTCredentials
	ExpiresAt   time.Time
}

func (s *Server) exportMailAccountCredentialsBatch(w http.ResponseWriter, r *http.Request) {
	var input batchMailCredentialExportInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	emails, err := normalizeBatchCredentialEmails(input.Emails)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format != "cpa" && format != "sub2" {
		writeAPI(w, http.StatusBadRequest, nil, "批量导出格式只能是 cpa 或 sub2")
		return
	}

	items := make([]preparedMailCredentialExport, 0, len(emails))
	failures := make([]string, 0)
	for _, email := range emails {
		credentials, expiresAt, loadErr := s.prepareMailGPTCredentials(r.Context(), email)
		if loadErr != nil {
			failures = append(failures, fmt.Sprintf("%s：%s", email, loadErr.Error()))
			continue
		}
		if strings.TrimSpace(credentials.RefreshToken) == "" {
			failures = append(failures, email+"：尚未获取 Codex RT")
			continue
		}
		items = append(items, preparedMailCredentialExport{Credentials: credentials, ExpiresAt: expiresAt})
	}
	if len(failures) > 0 {
		writeAPI(w, http.StatusConflict, map[string]any{"failures": failures}, "批量导出失败："+strings.Join(failures, "；"))
		return
	}

	exportedAt := time.Now().UTC()
	timestamp := beijingNow().Format("20060102-150405")
	if format == "cpa" {
		archive, archiveErr := buildCPABatchCredentialArchive(items, exportedAt)
		if archiveErr != nil {
			writeAPI(w, http.StatusInternalServerError, nil, archiveErr.Error())
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cpa-accounts-%s.zip"`, timestamp))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
		return
	}

	payload := buildSub2BatchCredentialExport(items, exportedAt)
	encoded, encodeErr := json.MarshalIndent(payload, "", "  ")
	if encodeErr != nil {
		writeAPI(w, http.StatusInternalServerError, nil, encodeErr.Error())
		return
	}
	encoded = append(encoded, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sub2-accounts-%s.json"`, timestamp))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func normalizeBatchCredentialEmails(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("请先勾选要导出的邮件账号")
	}
	if len(values) > 500 {
		return nil, errors.New("每次最多批量导出 500 个邮件账号")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		email := strings.ToLower(strings.TrimSpace(value))
		parsed, err := mail.ParseAddress(email)
		if err != nil || !strings.EqualFold(parsed.Address, email) || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("邮箱地址无效：%s", value)
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		result = append(result, email)
	}
	return result, nil
}

func (s *Server) prepareMailGPTCredentials(ctx context.Context, email string) (mailGPTCredentials, time.Time, error) {
	credentials, err := s.loadMailGPTCredentials(ctx, email)
	if err != nil {
		return mailGPTCredentials{}, time.Time{}, err
	}
	if credentials.Email == "" {
		credentials.Email = email
	}
	var tokenInfo model.UserInfo
	if credentials.AccountID == "" {
		if info, decodeErr := workflow.DecodeUserInfo(credentials.AccessToken); decodeErr == nil {
			tokenInfo = info
			credentials.AccountID = strings.TrimSpace(info.AccountID)
		}
	}
	if credentials.AccountID == "" {
		for _, profile := range s.store.FreeAccounts() {
			if strings.EqualFold(strings.TrimSpace(profile.Email), email) {
				credentials.AccountID = strings.TrimSpace(profile.OAuthAccountID)
				break
			}
		}
	}
	if tokenInfo.Email == "" {
		tokenInfo, _ = workflow.DecodeUserInfo(credentials.AccessToken)
	}
	if credentials.Name == "" {
		credentials.Name = firstNonEmpty(tokenInfo.Name, credentials.Email)
	}
	if credentials.PlanType == "" {
		credentials.PlanType = tokenInfo.PlanType
	}
	expiresAt, _ := workflow.AccessTokenExpiry(credentials.AccessToken)
	return credentials, expiresAt, nil
}

func buildCPABatchCredentialArchive(items []preparedMailCredentialExport, exportedAt time.Time) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, item := range items {
		payload := buildCPACredentialExport(item.Credentials, item.ExpiresAt, exportedAt)
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("生成 %s 的 CPA JSON 失败：%w", item.Credentials.Email, err)
		}
		entry, err := archive.Create(safeCredentialExportFilename(item.Credentials.Email) + "-cpa-auth.json")
		if err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("创建 CPA 压缩包失败：%w", err)
		}
		if _, err = entry.Write(append(encoded, '\n')); err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("写入 CPA 压缩包失败：%w", err)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("完成 CPA 压缩包失败：%w", err)
	}
	return buffer.Bytes(), nil
}

func buildSub2BatchCredentialExport(items []preparedMailCredentialExport, exportedAt time.Time) map[string]any {
	accounts := make([]any, 0, len(items))
	for _, item := range items {
		payload := buildSub2CredentialExport(item.Credentials, item.ExpiresAt, exportedAt)
		if exported, ok := payload["accounts"].([]any); ok {
			accounts = append(accounts, exported...)
		}
	}
	return map[string]any{
		"exported_at": exportedAt.Format(time.RFC3339),
		"proxies":     []any{},
		"accounts":    accounts,
	}
}

func safeCredentialExportFilename(email string) string {
	var builder strings.Builder
	for _, char := range strings.TrimSpace(email) {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', strings.ContainsRune("@._-", char):
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	name := strings.Trim(builder.String(), ".")
	if name == "" {
		return "account"
	}
	return name
}

func buildCPACredentialExport(credentials mailGPTCredentials, expiresAt, exportedAt time.Time) map[string]any {
	idToken, synthetic := credentials.IDToken, false
	if idToken == "" {
		idToken, synthetic = buildSyntheticIDToken(credentials.Email, credentials.AccountID, credentials.PlanType, expiresAt, exportedAt), true
	}
	return compactStringMap(map[string]any{
		"type":               "codex",
		"account_id":         credentials.AccountID,
		"chatgpt_account_id": credentials.AccountID,
		"email":              credentials.Email,
		"name":               credentials.Email,
		"plan_type":          credentials.PlanType,
		"chatgpt_plan_type":  credentials.PlanType,
		"id_token":           idToken,
		"id_token_synthetic": synthetic,
		"access_token":       credentials.AccessToken,
		"refresh_token":      credentials.RefreshToken,
		"session_token":      credentials.SessionToken,
		"last_refresh":       exportedAt.Format(time.RFC3339),
		"expired":            formatOptionalTime(expiresAt),
	})
}

func buildSub2CredentialExport(credentials mailGPTCredentials, expiresAt, exportedAt time.Time) map[string]any {
	expiresUnix := int64(0)
	if !expiresAt.IsZero() {
		expiresUnix = expiresAt.Unix()
	}
	authFile := buildCPACredentialExport(credentials, expiresAt, exportedAt)
	credentialFields := compactStringMap(map[string]any{
		"access_token":       authFile["access_token"],
		"refresh_token":      authFile["refresh_token"],
		"id_token":           authFile["id_token"],
		"session_token":      authFile["session_token"],
		"chatgpt_account_id": credentials.AccountID,
		"email":              credentials.Email,
		"expires_at":         expiresUnix,
		"plan_type":          credentials.PlanType,
	})
	account := compactStringMap(map[string]any{
		"name":                  credentials.Email,
		"platform":              "openai",
		"type":                  "oauth",
		"expires_at":            expiresUnix,
		"auto_pause_on_expired": true,
		"concurrency":           10,
		"priority":              1,
		"credentials":           credentialFields,
		"extra": compactStringMap(map[string]any{
			"email":        credentials.Email,
			"name":         credentials.Email,
			"source":       "gpt_account_manager_refresh",
			"last_refresh": exportedAt.Format(time.RFC3339),
		}),
	})
	return map[string]any{
		"exported_at": exportedAt.Format(time.RFC3339),
		"proxies":     []any{},
		"accounts":    []any{account},
	}
}

func compactStringMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		if number, ok := value.(int64); ok && number == 0 {
			continue
		}
		result[key] = value
	}
	return result
}

func buildSyntheticIDToken(email, accountID, planType string, expiresAt, now time.Time) string {
	expiresUnix := now.Add(time.Hour).Unix()
	if !expiresAt.IsZero() {
		expiresUnix = expiresAt.Unix()
	}
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT", "cpa_synthetic": true})
	payload, _ := json.Marshal(map[string]any{
		"iss": "ctgptm-mail-assistant", "aud": "chatgpt", "email": email,
		"chatgpt_account_id": accountID, "account_id": accountID, "chatgpt_plan_type": planType,
		"iat": now.Unix(), "exp": expiresUnix,
	})
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func mailAccountLoginInput(email, proxyURL string) map[string]any {
	return map[string]any{
		"email": email, "account_email": email, "mode": "login", "login_only": true,
		"use_proxy": true, "proxy_pick_mode": "manual", "proxy_url": proxyURL,
		"use_stored_mail_credentials": true, "force_email_code": true, "email_code_login": true,
		"credential_mode":         "chatgpt_at",
		"skip_phone_verification": true,
	}
}

func mailAccountRegistrationInput(email, proxyURL string) map[string]any {
	return map[string]any{
		"email": email, "account_email": email, "mode": "signup", "login_only": true,
		"use_proxy": true, "proxy_pick_mode": "manual", "proxy_url": proxyURL,
		"use_stored_mail_credentials": true,
		"credential_mode":             "register_only",
		"skip_phone_verification":     true,
	}
}

func mailAccountRegistrationWithATInput(email, proxyURL string) map[string]any {
	return map[string]any{
		"email": email, "account_email": email, "mode": "signup_at", "login_only": true,
		"use_proxy": true, "proxy_pick_mode": "manual", "proxy_url": proxyURL,
		"use_stored_mail_credentials": true,
		"credential_mode":             "chatgpt_at",
		"skip_phone_verification":     true,
	}
}

func codexOAuthLoginInput(email, proxyURL, smsProvider string, allowSMS bool) map[string]any {
	return map[string]any{
		"email": email, "account_email": email, "mode": "login", "login_only": true,
		"use_proxy": true, "proxy_pick_mode": "manual", "proxy_url": proxyURL,
		"use_stored_mail_credentials": true, "force_email_code": true, "email_code_login": true,
		"credential_mode": "codex_oauth",
		// Do not request a phone up front. The upstream flow only acquires a
		// number if OpenAI actually returns add_phone.
		"allow_sms":               allowSMS,
		"skip_phone_verification": false,
		"sms_realtime_provider":   strings.TrimSpace(smsProvider),
	}
}

func (s *Server) mailAccountLoginStatus(w http.ResponseWriter, r *http.Request) {
	if job, ok := s.localRegistrationStatus(r.PathValue("id")); ok {
		writeAPI(w, http.StatusOK, map[string]any{"success": true, "job": job}, "")
		return
	}
	writeAPI(w, http.StatusNotFound, nil, "本地任务不存在")
}

func (s *Server) mailAccountToTeam(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	profile, err := s.importMailAccountToTeam(r.Context(), email)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func (s *Server) importMailAccountToTeam(ctx context.Context, email string) (model.FreeAccountProfile, error) {
	if !strings.Contains(email, "@") {
		return model.FreeAccountProfile{}, errors.New("邮箱地址无效")
	}
	credentials, err := s.loadMailGPTCredentials(ctx, email)
	if err != nil {
		return model.FreeAccountProfile{}, err
	}
	info, err := workflow.DecodeUserInfo(credentials.AccessToken)
	if err != nil {
		return model.FreeAccountProfile{}, err
	}
	profile, _, err := s.store.SaveImportedFreeAccount(model.FreeAccountProfile{
		Label: info.Email, Email: info.Email, Name: info.Name, UserID: info.UserID,
		PersonalAccountID: info.AccountID, PlanType: info.PlanType,
	}, credentials.AccessToken)
	if err == nil {
		_, _ = s.store.EnsureFreeAccountLifecycleTask(profile)
		s.auditAccountEvent(ctx, profile.ID, "lifecycle", "rotation", "manual_single", "", "账号已从邮件管理进入 Team 轮转", map[string]any{"email": profile.Email})
	}
	return profile, err
}

type mailGPTCredentials struct {
	Email            string `json:"email"`
	Name             string `json:"name"`
	GPTPassword      string `json:"gpt_password"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	SessionToken     string `json:"session_token"`
	PlanType         string `json:"plan_type"`
	AccountID        string `json:"account_id"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	OpenAIAccountID  string `json:"openai_account_id"`
	ChatGPTSession   string `json:"chatgpt_session"`
}

func chatGPTSessionValue(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		return value
	}
	return raw
}

func (s *Server) loadMailGPTCredentials(ctx context.Context, email string) (mailGPTCredentials, error) {
	// Prefer credentials owned by this application. This keeps Team import and
	// credential export functional when the standalone mail manager is absent.
	if _, local, localErr := s.store.MailAccountCredential(email); localErr == nil && strings.TrimSpace(local.AccessToken) != "" {
		result := mailGPTCredentials{Email: strings.TrimSpace(local.Email), GPTPassword: strings.TrimSpace(local.GptPassword), AccessToken: strings.TrimSpace(local.AccessToken), RefreshToken: strings.TrimSpace(local.RefreshToken), ChatGPTSession: strings.TrimSpace(local.ChatGPTSession)}
		// Older records may have OAuth RT only in free_accounts. Fall back to
		// that projection so export remains usable after an upgrade.
		if result.RefreshToken == "" {
			for _, profile := range s.store.FreeAccounts() {
				if strings.EqualFold(strings.TrimSpace(profile.Email), email) {
					if _, oauth, oauthErr := s.store.FreeAccountCredential(profile.ID); oauthErr == nil {
						if result.AccessToken == "" {
							result.AccessToken = strings.TrimSpace(oauth.OAuthAccessToken)
						}
						result.RefreshToken = strings.TrimSpace(oauth.OAuthRefreshToken)
					}
					break
				}
			}
		}
		return result, nil
	}
	return mailGPTCredentials{}, errors.New("该邮件账号还没有 ChatGPT AT，请先执行本地注册")
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
