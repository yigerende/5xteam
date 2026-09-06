package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
)

func (s *Server) listFreeAccounts(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, http.StatusOK, s.store.FreeAccounts(), "")
}

type freeAccountImportInput struct {
	AccessTokens []string `json:"access_tokens"`
}

func (s *Server) importFreeAccounts(w http.ResponseWriter, r *http.Request) {
	var input freeAccountImportInput
	if err := decodeJSON(w, r, &input, 16<<20); err != nil {
		return
	}
	if len(input.AccessTokens) == 0 {
		writeAPI(w, http.StatusBadRequest, nil, "请提供至少一个 Free 账号 Access Token")
		return
	}
	if len(input.AccessTokens) > 500 {
		writeAPI(w, http.StatusBadRequest, nil, "单次最多导入 500 个 Free 账号")
		return
	}

	items := make([]model.FreeAccountProfile, 0, len(input.AccessTokens))
	errorsByIndex := make(map[string]string)
	created, updated := 0, 0
	seen := make(map[string]struct{})
	for index, raw := range input.AccessTokens {
		token := workflow.ExtractAccessToken(raw)
		if token == "" {
			errorsByIndex[strconv.Itoa(index+1)] = "Access Token 为空"
			continue
		}
		info, err := workflow.DecodeUserInfo(token)
		if err != nil {
			errorsByIndex[strconv.Itoa(index+1)] = err.Error()
			continue
		}
		if _, duplicate := seen[info.UserID]; duplicate {
			continue
		}
		seen[info.UserID] = struct{}{}
		profile, wasCreated, err := s.store.SaveImportedFreeAccount(model.FreeAccountProfile{
			Label: info.Email, Email: info.Email, Name: info.Name, UserID: info.UserID,
			PersonalAccountID: info.AccountID, PlanType: info.PlanType,
		}, token)
		if err != nil {
			errorsByIndex[strconv.Itoa(index+1)] = err.Error()
			continue
		}
		if wasCreated {
			created++
		} else {
			updated++
		}
		items = append(items, profile)
	}
	writeAPI(w, http.StatusOK, map[string]any{"items": items, "created": created, "updated": updated, "errors": errorsByIndex}, "")
}

func (s *Server) deleteFreeAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	if err := s.store.DeleteFreeAccount(id); err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]bool{"deleted": true}, "")
}

type freeAccountJoinInput struct {
	AdminAccountID string `json:"admin_account_id"`
	SeatType       string `json:"seat_type"`
}

func (s *Server) joinFreeAccount(w http.ResponseWriter, r *http.Request) {
	var input freeAccountJoinInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	input.AdminAccountID = strings.TrimSpace(input.AdminAccountID)
	input.SeatType = strings.TrimSpace(input.SeatType)
	if input.AdminAccountID == "" {
		writeAPI(w, http.StatusBadRequest, nil, "请选择邀请母号")
		return
	}
	if input.SeatType != "default" && input.SeatType != "prolite" {
		writeAPI(w, http.StatusBadRequest, nil, "邀请席位只能选择 Standard 或 Premium（5x）")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, sourceCredentials, err := s.store.FreeAccountCredential(id)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	admin, adminCredentials, err := s.currentAdminCredential(r.Context(), input.AdminAccountID)
	if err != nil {
		s.failFreeAccount(profile.ID, "invite", err)
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	if strings.TrimSpace(admin.TeamAccountID) == "" {
		writeAPI(w, http.StatusBadRequest, nil, "母号缺少团队 Account ID")
		return
	}
	runInvite, runAccept := freeAccountJoinSteps(profile)
	profile, err = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID, item.AdminEmail = admin.ID, admin.Email
		item.TeamAccountID, item.SeatType = admin.TeamAccountID, input.SeatType
		item.LastError = ""
		if runAccept {
			item.Status = "joining"
			if runInvite {
				item.InviteStatus, item.AcceptStatus = "running", "pending"
			} else {
				item.InviteStatus, item.AcceptStatus = "completed", "running"
			}
			item.RemoveStatus, item.RemovedAt = "pending", nil
		}
	})
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	if !runAccept {
		writeAPI(w, http.StatusOK, profile, "")
		return
	}
	client, err := workflow.NewClient(s.store.Settings())
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	if runInvite {
		if _, err = client.Invite(r.Context(), adminCredentials.AccessToken, admin.TeamAccountID, profile.Email, input.SeatType); err != nil {
			s.failFreeAccount(profile.ID, "invite", err)
			writeAPI(w, http.StatusBadRequest, nil, err.Error())
			return
		}
		_, _ = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
			item.InviteStatus, item.AcceptStatus = "completed", "running"
		})
	}
	if _, err = client.Accept(r.Context(), sourceCredentials.SourceAccessToken, admin.TeamAccountID, profile.UserID); err != nil {
		s.failFreeAccount(profile.ID, "accept", err)
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	now := time.Now()
	profile, err = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Status, item.InviteStatus, item.AcceptStatus = "joined", "completed", "completed"
		item.LastError, item.JoinedAt = "", &now
	})
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func freeAccountJoinSteps(profile model.FreeAccountProfile) (runInvite, runAccept bool) {
	runAccept = profile.AcceptStatus != "completed"
	runInvite = runAccept && profile.InviteStatus != "completed"
	return runInvite, runAccept
}

type freeAccountOAuthInput struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	AccountID    string          `json:"chatgpt_account_id"`
	OAuthJSON    json.RawMessage `json:"oauth_json"`
}

func (s *Server) attachFreeAccountOAuth(w http.ResponseWriter, r *http.Request) {
	var input freeAccountOAuthInput
	if err := decodeJSON(w, r, &input, 4<<20); err != nil {
		return
	}
	if len(input.OAuthJSON) > 0 && string(input.OAuthJSON) != "null" {
		raw := string(input.OAuthJSON)
		if strings.TrimSpace(input.AccessToken) == "" {
			input.AccessToken = workflow.ExtractAccessToken(raw)
		}
		if strings.TrimSpace(input.RefreshToken) == "" {
			input.RefreshToken = workflow.ExtractRefreshToken(raw)
		}
		if strings.TrimSpace(input.AccountID) == "" {
			input.AccountID = findJSONString(input.OAuthJSON, "chatgpt_account_id", "account_id")
		}
	}
	input.AccessToken, input.RefreshToken = strings.TrimSpace(input.AccessToken), strings.TrimSpace(input.RefreshToken)
	input.AccountID = strings.TrimSpace(input.AccountID)

	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, credentials, err := s.store.FreeAccountCredential(id)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	if profile.AcceptStatus != "completed" {
		writeAPI(w, http.StatusConflict, nil, "请先完成邀请并进入空间")
		return
	}
	if input.AccessToken == "" && credentials.OAuthAccessToken == "" {
		writeAPI(w, http.StatusBadRequest, nil, "OAuth Access Token 不能为空")
		return
	}
	if input.AccessToken != "" {
		info, decodeErr := workflow.DecodeUserInfo(input.AccessToken)
		if decodeErr != nil {
			writeAPI(w, http.StatusBadRequest, nil, "OAuth Access Token 无效: "+decodeErr.Error())
			return
		}
		if input.AccountID == "" {
			input.AccountID = info.AccountID
		}
	}
	if input.AccountID == "" {
		input.AccountID = profile.OAuthAccountID
	}
	if input.AccountID == "" {
		writeAPI(w, http.StatusBadRequest, nil, "OAuth 凭据缺少 chatgpt_account_id")
		return
	}
	profile, err = s.store.SaveFreeAccountOAuth(profile.ID, input.AccessToken, input.RefreshToken, input.AccountID)
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	if strings.TrimSpace(input.AccessToken) != "" || strings.TrimSpace(input.RefreshToken) != "" {
		if syncErr := s.store.SaveMailAccountOAuth(profile.Email, input.AccessToken, input.RefreshToken); syncErr != nil {
			writeAPI(w, http.StatusInternalServerError, nil, "OAuth 已保存，但同步邮件账号凭据失败: "+syncErr.Error())
			return
		}
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func (s *Server) startFreeAccountOAuth(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	// Reserve this account while checking and enqueueing the job so two
	// simultaneous clicks cannot pass the duplicate-job check together.
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, _, err := s.store.FreeAccountCredential(id)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	if profile.AcceptStatus != "completed" {
		writeAPI(w, http.StatusConflict, nil, "请先完成邀请并进入空间")
		return
	}
	// Prevent duplicate clicks/retries for one account from launching two
	// OAuth processes. Other account IDs remain fully concurrent.
	s.oauthMu.RLock()
	duplicate := false
	for _, existing := range s.oauthJobs {
		if fmt.Sprint(existing["account_id"]) == id && (fmt.Sprint(existing["status"]) == "queued" || fmt.Sprint(existing["status"]) == "running") {
			duplicate = true
			break
		}
	}
	s.oauthMu.RUnlock()
	if profile.OAuthStatus == "running" || duplicate {
		writeAPI(w, http.StatusConflict, nil, "该账号的 Codex OAuth 正在处理中")
		return
	}
	jobID := randomRegistrationID()
	job := map[string]any{"job_id": jobID, "account_id": id, "email": profile.Email, "status": "queued", "state": "queued", "logs": []any{}, "error": "", "result": nil}
	s.oauthMu.Lock()
	s.oauthJobs[jobID] = job
	s.oauthMu.Unlock()
	_, _ = s.store.UpdateFreeAccount(id, func(item *model.FreeAccountProfile) {
		item.OAuthStatus = "running"
		item.Status = "oauthing"
		item.LastError = ""
	})
	go s.runFreeAccountOAuth(jobID, id, profile.Email)
	writeAPI(w, http.StatusAccepted, map[string]any{"job": cloneRegistrationJob(job)}, "")
}

func (s *Server) freeAccountOAuthStatus(w http.ResponseWriter, r *http.Request) {
	s.oauthMu.RLock()
	job, ok := s.oauthJobs[r.PathValue("job_id")]
	if ok {
		job = cloneRegistrationJob(job)
	}
	s.oauthMu.RUnlock()
	if !ok {
		writeAPI(w, http.StatusNotFound, nil, "Codex OAuth 任务不存在")
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"job": job}, "")
}

func (s *Server) updateOAuthJob(id, status, message string) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if j := s.oauthJobs[id]; j != nil {
		j["status"], j["state"] = status, status
		logs, _ := j["logs"].([]any)
		logs = append(logs, map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "level": "info", "step": "oauth", "message": message})
		j["logs"] = logs
	}
}

func (s *Server) finishOAuthJob(jobID, accountID string, result map[string]any, runErr error) {
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
		at, _ := result["access_token"].(string)
		rt, _ := result["refresh_token"].(string)
		aid, _ := result["account_id"].(string)
		if strings.TrimSpace(at) == "" || strings.TrimSpace(rt) == "" {
			status, message = "failed", "OAuth 结果缺少 Access Token 或 Refresh Token"
		} else if _, err := s.store.SaveFreeAccountOAuth(accountID, at, rt, aid); err != nil {
			status, message = "failed", err.Error()
		} else if profile, _, err := s.store.FreeAccountCredential(accountID); err == nil {
			// Keep the mail-management projection synchronized with the Team
			// rotation projection so its RT status and export dialog are current.
			if syncErr := s.store.SaveMailAccountOAuth(profile.Email, at, rt); syncErr != nil {
				status, message = "failed", fmt.Sprintf("OAuth 已保存，但同步邮件账号凭据失败: %v", syncErr)
			}
		}
	}
	s.oauthMu.Lock()
	if j := s.oauthJobs[jobID]; j != nil {
		j["status"], j["state"], j["error"], j["result"] = status, status, message, result
	}
	s.oauthMu.Unlock()
	if status != "success" {
		s.failFreeAccount(accountID, "oauth", errors.New(message))
	}
}

func (s *Server) runFreeAccountOAuth(jobID, accountID, email string) {
	// Serialize retries/clicks for the same account. Different account IDs use
	// different mutexes and continue to run concurrently; each OAuth process
	// creates its own isolated Python BrowserSession and cookie jar.
	unlock := s.lockFreeAccount(accountID)
	defer unlock()
	s.runFreeAccountOAuthUnlocked(jobID, accountID, email)
}

// runFreeAccountOAuthUnlocked is used by the quota recovery path, which
// already holds the account mutex while deciding whether a Sub2 401 requires
// reauthentication.
func (s *Server) runFreeAccountOAuthUnlocked(jobID, accountID, email string) {

	profile, creds, err := s.store.FreeAccountCredential(accountID)
	if err != nil {
		s.finishOAuthJob(jobID, accountID, nil, err)
		return
	}
	if strings.TrimSpace(creds.SourceAccessToken) == "" {
		s.finishOAuthJob(jobID, accountID, nil, errors.New("账号缺少源 Access Token"))
		return
	}
	mailProfile, mailCreds, err := s.store.MailAccountCredential(email)
	_ = mailProfile
	if err != nil || strings.TrimSpace(mailCreds.PickupURL) == "" {
		s.finishOAuthJob(jobID, accountID, nil, errors.New("邮箱未配置取件链接"))
		return
	}
	settings := s.store.Settings()
	provider := strings.TrimSpace(settings.SMSProvider)
	smsCfg := map[string]any{}
	if provider != "" {
		smsCfg, _ = s.store.SMSConfigRaw(provider)
	} else {
		// Match the mail-manager behaviour: use the first enabled realtime
		// provider from this application's own SMS configuration.
		for _, candidate := range []string{"hero_sms", "nextpro", "congou", "chatai"} {
			if cfg, cfgErr := s.store.SMSConfigRaw(candidate); cfgErr == nil {
				enabled, _ := cfg["enabled"].(bool)
				if enabled {
					provider, smsCfg = candidate, cfg
					break
				}
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"email": email, "pickup_url": mailCreds.PickupURL, "gpt_password": mailCreds.GptPassword, "proxy": settings.ProxyURL, "sms_provider": provider, "sms_config": smsCfg})
	python, err := exec.LookPath("python")
	if err != nil {
		s.finishOAuthJob(jobID, accountID, nil, errors.New("未找到 Python 运行环境"))
		return
	}
	script := filepath.Join("internal", "protocol_codex_oauth.py")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, script)
	cmd.Dir, _ = os.Getwd()
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1", "PYTHONPATH="+filepath.Join(cmd.Dir, "internal", "codex_runtime"))
	cmd.Stdin = bytes.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout = &out
	errPipe, e := cmd.StderrPipe()
	if e != nil {
		s.finishOAuthJob(jobID, accountID, nil, e)
		return
	}
	if e = cmd.Start(); e != nil {
		s.finishOAuthJob(jobID, accountID, nil, e)
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(errPipe)
		for sc.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "[protocol]"))
			if strings.HasPrefix(line, "<") || strings.Contains(strings.ToLower(line), "<style") {
				line = "OpenAI 返回 HTML 拒绝页（HTTP 403），请更换代理出口后重试"
			}
			if len([]rune(line)) > 300 {
				line = string([]rune(line)[:300]) + "..."
			}
			if line != "" {
				s.updateOAuthJob(jobID, "running", line)
			}
		}
	}()
	waitErr := cmd.Wait()
	<-done
	if ctx.Err() != nil {
		waitErr = ctx.Err()
	}
	if waitErr != nil {
		s.finishOAuthJob(jobID, accountID, nil, fmt.Errorf("Codex OAuth 执行失败: %w", waitErr))
		return
	}
	var result map[string]any
	if e := json.Unmarshal(out.Bytes(), &result); e != nil {
		s.finishOAuthJob(jobID, accountID, nil, fmt.Errorf("解析 Codex OAuth 结果失败: %w", e))
		return
	}
	s.finishOAuthJob(jobID, accountID, result, nil)
	_ = profile
}

func (s *Server) pushFreeAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, credentials, err := s.store.FreeAccountCredential(id)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	if profile.Sub2AccountID > 0 {
		writeAPI(w, http.StatusOK, profile, "")
		return
	}
	if profile.OAuthStatus != "completed" || credentials.OAuthAccessToken == "" || credentials.OAuthRefreshToken == "" {
		writeAPI(w, http.StatusConflict, nil, "请先绑定完整的 Codex OAuth Access Token 和 Refresh Token")
		return
	}
	settings, password, err := s.store.Sub2Settings()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	if len(settings.GroupIDs) == 0 {
		writeAPI(w, http.StatusBadRequest, nil, "请先配置 Sub2 OpenAI 分组")
		return
	}
	_, _ = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Status, item.PushStatus, item.LastError = "pushing", "running", ""
	})
	accountName := profile.Email
	if strings.TrimSpace(profile.Label) != "" {
		accountName = profile.Label
	}
	accountName += "--" + time.Now().Format("15:04")
	createInput := sub2.CreateAccountInput{
		Name: accountName,
		Credentials: map[string]any{
			"access_token": credentials.OAuthAccessToken, "refresh_token": credentials.OAuthRefreshToken,
			"chatgpt_account_id": profile.OAuthAccountID, "email": profile.Email,
		},
		GroupIDs: settings.GroupIDs, Models: settings.Models, Concurrency: settings.AccountConcurrency,
		Priority: settings.Priority,
	}
	// Sub2 persists idempotency keys even after an account is deleted. Include
	// the current OAuth credential fingerprint so a newly authorized/recreated
	// account never reuses the old payload's key. If the API still reports an
	// idempotency conflict (for example after a manual delete), retry once with
	// a fresh key while keeping the same request body.
	fingerprint := sha256.Sum256([]byte(profile.ID + "|" + accountName + "|" + credentials.OAuthAccessToken + "|" + credentials.OAuthRefreshToken + "|" + fmt.Sprint(settings.GroupIDs) + "|" + fmt.Sprint(settings.Models)))
	idempotencyKey := "free-pipeline-" + profile.ID + "-" + fmt.Sprintf("%x", fingerprint[:8])
	created, err := s.sub2.CreateAccount(r.Context(), settings, password, createInput, idempotencyKey)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "idempotency") {
		created, err = s.sub2.CreateAccount(r.Context(), settings, password, createInput, "free-pipeline-"+profile.ID+"-"+randomRegistrationID())
	}
	if err != nil {
		s.failFreeAccount(profile.ID, "push", err)
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	now := time.Now()
	profile, err = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Status, item.PushStatus, item.LastError = "monitoring", "completed", ""
		item.Sub2AccountID, item.Sub2AccountName = created.ID, created.Name
		item.StatusCheckedAt = nil
		item.Sub2GroupIDs, item.Sub2GroupNames = append([]int64(nil), settings.GroupIDs...), append([]string(nil), settings.GroupNames...)
		if len(settings.GroupIDs) > 0 {
			item.Sub2GroupID = settings.GroupIDs[0]
		}
		if len(settings.GroupNames) > 0 {
			item.Sub2GroupName = settings.GroupNames[0]
		}
		item.PushedAt = &now
	})
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func (s *Server) checkFreeAccountQuota(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, removed, err := s.performFreeAccountQuota(r.Context(), id, true)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"account": profile, "auto_removed": removed}, "")
}

func (s *Server) performFreeAccountQuota(ctx context.Context, id string, allowAutoRemove bool) (model.FreeAccountProfile, bool, error) {
	settings, _, err := s.store.Sub2Settings()
	if err != nil {
		return model.FreeAccountProfile{}, false, err
	}
	return s.performFreeAccountQuotaInternal(ctx, id, allowAutoRemove, settings.Enable401Check)
}

func (s *Server) performFreeAccountQuotaInternal(ctx context.Context, id string, allowAutoRemove, allowRelogin bool) (model.FreeAccountProfile, bool, error) {
	profile, _, err := s.store.FreeAccountCredential(id)
	if err != nil {
		return profile, false, err
	}
	if profile.Sub2AccountID < 1 {
		return profile, false, errors.New("账号尚未推送到 Sub2")
	}
	settings, password, err := s.store.Sub2Settings()
	if err != nil {
		return profile, false, err
	}
	_, _ = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.QuotaStatus, item.LastError = "running", ""
	})
	quota, err := s.sub2.QueryQuota(ctx, settings, password, profile.Sub2AccountID)
	if err != nil {
		if isSub2Unauthorized(err) {
			// Record the probe time even on a 401 so a failed recovery does not
			// hammer the same Sub2 account every monitor tick.
			checkedAt := time.Now()
			_, _ = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
				item.QuotaCheckedAt = &checkedAt
			})
		}
		if allowRelogin && isSub2Unauthorized(err) && profile.AcceptStatus == "completed" && profile.RemoveStatus != "completed" {
			if reloginErr := s.reloginAndRepush(ctx, profile.ID); reloginErr == nil {
				return s.performFreeAccountQuotaInternal(ctx, id, allowAutoRemove, false)
			} else {
				err = fmt.Errorf("Sub2 返回 401，重登并重新推送失败: %w", reloginErr)
			}
		}
		s.failFreeAccount(profile.ID, "quota", err)
		return profile, false, err
	}
	window5H, window7D := quota.Windows()
	if window5H == nil && window7D == nil {
		err = errors.New("Sub2 额度响应中没有 5小时或7天窗口")
		s.failFreeAccount(profile.ID, "quota", err)
		return profile, false, err
	}
	now := time.Now()
	profile, err = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Quota5H, item.Quota7D = window5H, window7D
		item.QuotaStatus, item.Status, item.LastError = "completed", "monitoring", ""
		item.QuotaCheckedAt = &now
	})
	if err != nil {
		return profile, false, err
	}
	if !allowAutoRemove || !profile.AutoRemove || profile.RemoveStatus == "completed" {
		return profile, false, nil
	}
	selected := profile.Quota7D
	if profile.ExhaustionPolicy == "5h" {
		selected = profile.Quota5H
	}
	if selected == nil || selected.UsedPercent < 100 {
		return profile, false, nil
	}
	profile, err = s.performFreeAccountRemove(ctx, profile.ID)
	return profile, err == nil, err
}

func isSub2Unauthorized(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "http 401") || strings.Contains(text, "status 401") || strings.Contains(text, "code 401") || strings.Contains(text, "unauthorized")
}

// checkFreeAccountStatus performs the lightweight Sub2 account status probe
// independently from quota polling. A 401 triggers the existing relogin and
// repush flow; quota polling remains responsible only for quota/removal work.
func (s *Server) checkFreeAccountStatus(ctx context.Context, id string, settings model.Sub2Settings, password string) (bool, error) {
	profile, _, err := s.store.FreeAccountCredential(id)
	if err != nil {
		return false, err
	}
	if profile.Sub2AccountID < 1 {
		return false, errors.New("账号尚未推送到 Sub2")
	}
	checkedAt := time.Now()
	status, err := s.sub2.AccountStatus(ctx, settings, password, profile.Sub2AccountID)
	_, _ = s.store.UpdateFreeAccount(id, func(item *model.FreeAccountProfile) {
		item.StatusCheckedAt = &checkedAt
	})
	if err != nil {
		if isSub2Unauthorized(err) && profile.AcceptStatus == "completed" && profile.RemoveStatus != "completed" {
			if reloginErr := s.reloginAndRepush(ctx, id); reloginErr != nil {
				return false, fmt.Errorf("Sub2 返回 401，重登并重新推送失败: %w", reloginErr)
			}
			return true, nil
		}
		return false, err
	}
	if status == http.StatusUnauthorized && profile.AcceptStatus == "completed" && profile.RemoveStatus != "completed" {
		if reloginErr := s.reloginAndRepush(ctx, id); reloginErr != nil {
			return false, fmt.Errorf("Sub2 返回 401，重登并重新推送失败: %w", reloginErr)
		}
		return true, nil
	}
	return false, nil
}

func (s *Server) reloginAndRepush(ctx context.Context, accountID string) error {
	profile, _, err := s.store.FreeAccountCredential(accountID)
	if err != nil {
		return err
	}
	jobID := randomRegistrationID()
	job := map[string]any{"job_id": jobID, "account_id": accountID, "email": profile.Email, "status": "queued", "state": "queued", "logs": []any{}, "error": "", "result": nil}
	s.oauthMu.Lock()
	s.oauthJobs[jobID] = job
	s.oauthMu.Unlock()
	_, _ = s.store.UpdateFreeAccount(accountID, func(item *model.FreeAccountProfile) {
		item.OAuthStatus, item.Status, item.LastError = "running", "oauthing", "Sub2 返回 401，正在重新获取 Codex OAuth"
	})
	s.runFreeAccountOAuthUnlocked(jobID, accountID, profile.Email)
	s.oauthMu.RLock()
	completed := cloneRegistrationJob(s.oauthJobs[jobID])
	s.oauthMu.RUnlock()
	if fmt.Sprint(completed["status"]) != "success" {
		return errors.New(fmt.Sprint(completed["error"]))
	}
	profile, credentials, err := s.store.FreeAccountCredential(accountID)
	if err != nil {
		return err
	}
	settings, password, err := s.store.Sub2Settings()
	if err != nil {
		return err
	}
	name := profile.Email
	if strings.TrimSpace(profile.Label) != "" {
		name = profile.Label
	}
	name += "--" + time.Now().Format("15:04") + "-重登"
	input := sub2.CreateAccountInput{
		Name:        name,
		Credentials: map[string]any{"access_token": credentials.OAuthAccessToken, "refresh_token": credentials.OAuthRefreshToken, "chatgpt_account_id": profile.OAuthAccountID, "email": profile.Email},
		GroupIDs:    settings.GroupIDs, Models: settings.Models, Concurrency: settings.AccountConcurrency, Priority: settings.Priority,
	}
	fingerprint := sha256.Sum256([]byte("relogin|" + profile.ID + "|" + fmt.Sprint(profile.ReloginCount+1) + "|" + credentials.OAuthAccessToken + "|" + credentials.OAuthRefreshToken + "|" + fmt.Sprint(settings.GroupIDs) + "|" + fmt.Sprint(settings.Models)))
	key := "free-pipeline-relogin-" + profile.ID + "-" + fmt.Sprintf("%x", fingerprint[:8])
	created, err := s.sub2.CreateAccount(ctx, settings, password, input, key)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "idempotency") {
		created, err = s.sub2.CreateAccount(ctx, settings, password, input, key+"-"+randomRegistrationID())
	}
	if err != nil {
		return err
	}
	now := time.Now()
	_, err = s.store.UpdateFreeAccount(accountID, func(item *model.FreeAccountProfile) {
		item.Status, item.OAuthStatus, item.PushStatus, item.QuotaStatus, item.LastError = "monitoring", "completed", "completed", "pending", ""
		item.Sub2AccountID, item.Sub2AccountName = created.ID, created.Name
		item.StatusCheckedAt = nil
		item.Sub2GroupIDs, item.Sub2GroupNames = append([]int64(nil), settings.GroupIDs...), append([]string(nil), settings.GroupNames...)
		if len(settings.GroupIDs) > 0 {
			item.Sub2GroupID = settings.GroupIDs[0]
		}
		if len(settings.GroupNames) > 0 {
			item.Sub2GroupName = settings.GroupNames[0]
		}
		item.PushedAt, item.ReloginCount = &now, item.ReloginCount+1
	})
	return err
}

func (s *Server) removeFreeAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, err := s.performFreeAccountRemove(r.Context(), id)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func (s *Server) performFreeAccountRemove(ctx context.Context, id string) (model.FreeAccountProfile, error) {
	profile, _, err := s.store.FreeAccountCredential(id)
	if err != nil {
		return profile, err
	}
	if profile.RemoveStatus == "completed" {
		return profile, nil
	}
	if profile.AcceptStatus != "completed" || profile.AdminAccountID == "" || profile.TeamAccountID == "" {
		return profile, errors.New("账号没有可移出的团队空间记录")
	}
	_, adminCredentials, err := s.currentAdminCredential(ctx, profile.AdminAccountID)
	if err != nil {
		s.failFreeAccount(profile.ID, "remove", err)
		return profile, err
	}
	client, err := workflow.NewClient(s.store.Settings())
	if err != nil {
		return profile, err
	}
	_, _ = s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Status, item.RemoveStatus, item.LastError = "removing", "running", ""
	})
	if _, err = client.Kick(ctx, adminCredentials.AccessToken, profile.TeamAccountID, profile.UserID); err != nil {
		s.failFreeAccount(profile.ID, "remove", err)
		return profile, err
	}
	now := time.Now()
	return s.store.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Status, item.RemoveStatus, item.LastError = "removed", "completed", ""
		item.AutoRemove, item.RemovedAt = false, &now
	})
}

type freeAccountStageInput struct {
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	Message       string `json:"message"`
	Sub2AccountID int64  `json:"sub2_account_id"`
}

func (s *Server) updateFreeAccountStage(w http.ResponseWriter, r *http.Request) {
	var input freeAccountStageInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	input.Stage = strings.ToLower(strings.TrimSpace(input.Stage))
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if !validFreeAccountStage(input.Stage) {
		writeAPI(w, http.StatusBadRequest, nil, "阶段只能是 invite、accept、oauth、push、quota 或 remove")
		return
	}
	if input.Status != "not_started" && input.Status != "pending" && input.Status != "completed" && input.Status != "failed" {
		writeAPI(w, http.StatusBadRequest, nil, "阶段状态只能是 not_started、pending、completed 或 failed")
		return
	}
	if input.Stage == "push" && input.Status == "completed" && input.Sub2AccountID < 1 {
		writeAPI(w, http.StatusBadRequest, nil, "手动完成推送时必须填写 Sub2 账号 ID")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	now := time.Now()
	profile, err := s.store.UpdateFreeAccount(id, func(item *model.FreeAccountProfile) {
		applyManualFreeAccountStage(item, input.Stage, input.Status, input.Message, now)
		if input.Stage == "push" && input.Status == "completed" {
			item.Sub2AccountID = input.Sub2AccountID
			if item.Sub2AccountName == "" {
				item.Sub2AccountName = item.Email
			}
		} else if input.Stage == "push" {
			item.Sub2AccountID, item.Sub2GroupID = 0, 0
			item.Sub2AccountName, item.Sub2GroupName = "", ""
			item.Sub2GroupIDs, item.Sub2GroupNames = nil, nil
			item.Quota5H, item.Quota7D, item.QuotaCheckedAt, item.StatusCheckedAt = nil, nil, nil, nil
			item.QuotaStatus = "pending"
		}
		item.Status = freeAccountOverallStatus(*item)
	})
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func validFreeAccountStage(stage string) bool {
	switch stage {
	case "invite", "accept", "oauth", "push", "quota", "remove":
		return true
	default:
		return false
	}
}

func applyManualFreeAccountStage(item *model.FreeAccountProfile, stage, status, message string, now time.Time) {
	switch stage {
	case "invite":
		item.InviteStatus = status
	case "accept":
		item.AcceptStatus = status
		item.JoinedAt = stageTime(status, now)
	case "oauth":
		item.OAuthStatus = status
		item.OAuthReadyAt = stageTime(status, now)
	case "push":
		item.PushStatus = status
		item.PushedAt = stageTime(status, now)
	case "quota":
		item.QuotaStatus = status
		item.QuotaCheckedAt = stageTime(status, now)
	case "remove":
		item.RemoveStatus = status
		item.RemovedAt = stageTime(status, now)
		if status == "completed" {
			item.AutoRemove = false
		}
	}
	if status == "failed" {
		item.LastError = strings.TrimSpace(message)
		if item.LastError == "" {
			item.LastError = "手动标记为失败"
		}
	} else {
		item.LastError = ""
	}
	item.Status = freeAccountOverallStatus(*item)
}

func stageTime(status string, now time.Time) *time.Time {
	if status != "completed" {
		return nil
	}
	value := now
	return &value
}

func freeAccountOverallStatus(item model.FreeAccountProfile) string {
	stages := []struct {
		name   string
		status string
	}{
		{"remove", item.RemoveStatus}, {"quota", item.QuotaStatus}, {"push", item.PushStatus},
		{"oauth", item.OAuthStatus}, {"accept", item.AcceptStatus}, {"invite", item.InviteStatus},
	}
	for _, stage := range stages {
		if stage.status == "failed" {
			return stage.name + "_failed"
		}
	}
	if item.RemoveStatus == "completed" {
		return "removed"
	}
	if item.PushStatus == "completed" || item.QuotaStatus == "completed" {
		return "monitoring"
	}
	if item.OAuthStatus == "completed" {
		return "oauth_ready"
	}
	if item.AcceptStatus == "completed" {
		return "joined"
	}
	if item.InviteStatus == "completed" {
		return "invited"
	}
	return "imported"
}

type freeAccountPolicyInput struct {
	ExhaustionPolicy string `json:"exhaustion_policy"`
	AutoRemove       bool   `json:"auto_remove"`
}

func (s *Server) updateFreeAccountPolicy(w http.ResponseWriter, r *http.Request) {
	var input freeAccountPolicyInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if input.ExhaustionPolicy != "5h" && input.ExhaustionPolicy != "7d" {
		writeAPI(w, http.StatusBadRequest, nil, "移出策略只能选择 5小时或7天额度")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	unlock := s.lockFreeAccount(id)
	defer unlock()
	profile, err := s.store.UpdateFreeAccount(id, func(item *model.FreeAccountProfile) {
		item.ExhaustionPolicy, item.AutoRemove = input.ExhaustionPolicy, input.AutoRemove
	})
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func (s *Server) getSub2Settings(w http.ResponseWriter, _ *http.Request) {
	settings, _, err := s.store.Sub2Settings()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, settings, "")
}

type sub2SettingsInput struct {
	URL                        string   `json:"url"`
	Email                      string   `json:"email"`
	Password                   string   `json:"password"`
	GroupID                    int64    `json:"group_id"`
	GroupName                  string   `json:"group_name"`
	GroupIDs                   []int64  `json:"group_ids"`
	GroupNames                 []string `json:"group_names"`
	Models                     []string `json:"models"`
	AccountConcurrency         int      `json:"account_concurrency"`
	Priority                   int      `json:"priority"`
	Enable401Check             *bool    `json:"enable_401_check"`
	StatusCheckIntervalSeconds int      `json:"status_check_interval_seconds"`
	QuotaCheckIntervalSeconds  int      `json:"quota_check_interval_seconds"`
}

func (s *Server) saveSub2Settings(w http.ResponseWriter, r *http.Request) {
	var input sub2SettingsInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	current, currentPassword, err := s.store.Sub2Settings()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	input.URL, input.Email = strings.TrimRight(strings.TrimSpace(input.URL), "/"), strings.TrimSpace(input.Email)
	input.Password = strings.TrimSpace(input.Password)
	if input.Password == "" {
		input.Password = currentPassword
	}
	if err := validateSub2Connection(input.URL, input.Email, input.Password); err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	if input.AccountConcurrency == 0 {
		input.AccountConcurrency = current.AccountConcurrency
	}
	if input.AccountConcurrency < 1 || input.AccountConcurrency > 100 {
		writeAPI(w, http.StatusBadRequest, nil, "Sub2 账号并发数必须在 1 到 100 之间")
		return
	}
	if input.Priority == 0 {
		input.Priority = current.Priority
	}
	if input.Priority < 1 || input.Priority > 100 {
		writeAPI(w, http.StatusBadRequest, nil, "Sub2 优先级必须在 1 到 100 之间")
		return
	}
	if input.StatusCheckIntervalSeconds == 0 {
		input.StatusCheckIntervalSeconds = current.StatusCheckIntervalSeconds
	}
	if input.StatusCheckIntervalSeconds < 10 || input.StatusCheckIntervalSeconds > 86400 {
		writeAPI(w, http.StatusBadRequest, nil, "401 状态查询间隔必须在 10 到 86400 秒之间")
		return
	}
	enable401Check := current.Enable401Check
	if input.Enable401Check != nil {
		enable401Check = *input.Enable401Check
	}
	if input.QuotaCheckIntervalSeconds == 0 {
		input.QuotaCheckIntervalSeconds = current.QuotaCheckIntervalSeconds
	}
	if input.QuotaCheckIntervalSeconds < 10 || input.QuotaCheckIntervalSeconds > 86400 {
		writeAPI(w, http.StatusBadRequest, nil, "额度查询间隔必须在 10 到 86400 秒之间")
		return
	}
	input.GroupIDs = uniquePositiveInt64s(input.GroupIDs)
	if len(input.GroupIDs) == 0 && input.GroupID > 0 {
		input.GroupIDs = []int64{input.GroupID}
	}
	input.GroupNames = uniqueNonEmptyStrings(input.GroupNames)
	if len(input.GroupNames) == 0 && strings.TrimSpace(input.GroupName) != "" {
		input.GroupNames = []string{strings.TrimSpace(input.GroupName)}
	}
	input.Models = uniqueNonEmptyStrings(input.Models)
	settings, err := s.store.SaveSub2Settings(model.Sub2Settings{
		URL: input.URL, Email: input.Email, GroupID: input.GroupID, GroupName: strings.TrimSpace(input.GroupName),
		GroupIDs: input.GroupIDs, GroupNames: input.GroupNames, Models: input.Models, AccountConcurrency: input.AccountConcurrency, Priority: input.Priority,
		Enable401Check: enable401Check, StatusCheckIntervalSeconds: input.StatusCheckIntervalSeconds, QuotaCheckIntervalSeconds: input.QuotaCheckIntervalSeconds,
	}, input.Password)
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, settings, "")
}

func (s *Server) testSub2Settings(w http.ResponseWriter, r *http.Request) {
	settings, password, err := s.store.Sub2Settings()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return
	}
	if err := validateSub2Connection(settings.URL, settings.Email, password); err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	groups, err := s.sub2.Groups(r.Context(), settings, password)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"groups": groups}, "")
}

func validateSub2Connection(rawURL, email, password string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("Sub2 地址必须是有效的 HTTP/HTTPS URL")
	}
	if strings.TrimSpace(email) == "" || strings.TrimSpace(password) == "" {
		return errors.New("Sub2 管理员邮箱和密码不能为空")
	}
	return nil
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value < 1 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Server) failFreeAccount(id, stage string, stageErr error) {
	_, _ = s.store.UpdateFreeAccount(id, func(item *model.FreeAccountProfile) {
		item.Status, item.LastError = stage+"_failed", stageErr.Error()
		switch stage {
		case "invite":
			item.InviteStatus = "failed"
		case "accept":
			item.AcceptStatus = "failed"
		case "oauth":
			item.OAuthStatus = "failed"
		case "push":
			item.PushStatus = "failed"
		case "quota":
			item.QuotaStatus = "failed"
		case "remove":
			item.RemoveStatus = "failed"
		}
	})
}

func findJSONString(raw json.RawMessage, keys ...string) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	var visit func(any) string
	visit = func(current any) string {
		switch item := current.(type) {
		case map[string]any:
			for key, child := range item {
				if _, ok := wanted[key]; ok {
					if result, ok := child.(string); ok && strings.TrimSpace(result) != "" {
						return strings.TrimSpace(result)
					}
				}
			}
			for _, child := range item {
				if result := visit(child); result != "" {
					return result
				}
			}
		case []any:
			for _, child := range item {
				if result := visit(child); result != "" {
					return result
				}
			}
		}
		return ""
	}
	return visit(value)
}

func (s *Server) monitorFreeAccounts(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			settings, password, settingsErr := s.store.Sub2Settings()
			if settingsErr != nil {
				continue
			}
			statusInterval := time.Duration(settings.StatusCheckIntervalSeconds) * time.Second
			if statusInterval < 10*time.Second {
				statusInterval = 120 * time.Second
			}
			quotaInterval := time.Duration(settings.QuotaCheckIntervalSeconds) * time.Second
			if quotaInterval < 10*time.Second {
				quotaInterval = 120 * time.Second
			}
			now := time.Now()
			for _, account := range s.store.FreeAccounts() {
				if account.Sub2AccountID < 1 || account.RemoveStatus == "completed" {
					continue
				}
				statusDue := settings.Enable401Check && (account.StatusCheckedAt == nil || now.Sub(*account.StatusCheckedAt) >= statusInterval)
				quotaDue := account.QuotaCheckedAt == nil || now.Sub(*account.QuotaCheckedAt) >= quotaInterval
				if !statusDue && !quotaDue {
					continue
				}
				unlock := s.lockFreeAccount(account.ID)
				reloggedIn := false
				if statusDue {
					reloggedIn, _ = s.checkFreeAccountStatus(ctx, account.ID, settings, password)
				}
				var err error
				if quotaDue && !reloggedIn {
					_, _, err = s.performFreeAccountQuotaInternal(ctx, account.ID, true, settings.Enable401Check)
				}
				unlock()
				if err != nil {
					// The durable account state carries the actionable error for the UI.
					continue
				}
			}
		}
	}
}

func (s *Server) lockFreeAccount(id string) func() {
	value, _ := s.freeLocks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}
