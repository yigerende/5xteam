package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
	"chatgpt-space-merge/webui"
)

type Server struct {
	store     *store.Store
	jobs      *workflow.Manager
	static    fs.FS
	refreshMu sync.Mutex
}

const (
	adminRefreshCooldown = 60 * time.Second
	adminRefreshAhead    = 5 * time.Minute
)

type response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func New(dataStore *store.Store, jobs *workflow.Manager) (*Server, error) {
	static, err := fs.Sub(webui.Static, "static")
	if err != nil {
		return nil, err
	}
	return &Server{store: dataStore, jobs: jobs, static: static}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) { writeAPI(w, 200, map[string]string{"status": "ok"}, "") })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		writeAPI(w, 200, map[string]string{"status": "ready"}, "")
	})
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.saveSettings)
	mux.HandleFunc("GET /api/proxies", s.listProxies)
	mux.HandleFunc("POST /api/proxies", s.createProxy)
	mux.HandleFunc("PUT /api/proxies/{id}", s.updateProxy)
	mux.HandleFunc("DELETE /api/proxies/{id}", s.deleteProxy)
	mux.HandleFunc("POST /api/proxies/test", s.testProxy)
	mux.HandleFunc("GET /api/admin-accounts", s.listAdminAccounts)
	mux.HandleFunc("POST /api/admin-accounts", s.createAdminAccount)
	mux.HandleFunc("PUT /api/admin-accounts/{id}", s.updateAdminAccount)
	mux.HandleFunc("DELETE /api/admin-accounts/{id}", s.deleteAdminAccount)
	mux.HandleFunc("POST /api/admin-accounts/{id}/refresh", s.refreshAdminAccount)
	mux.HandleFunc("POST /api/admin-accounts/test", s.testAdminAccount)
	mux.HandleFunc("POST /api/tokens/inspect", s.inspectTokens)
	mux.HandleFunc("POST /api/jobs", s.startJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("DELETE /api/history", s.clearHistory)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(mustSub(s.static, "assets")))))
	mux.HandleFunc("GET /", s.index)
	return s.requestLog(s.securityHeaders(mux))
}

func mustSub(root fs.FS, dir string) fs.FS {
	value, err := fs.Sub(root, dir)
	if err != nil {
		panic(err)
	}
	return value
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.Error(w, "page unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, 200, s.store.Settings(), "")
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var settings model.Settings
	if err := decodeJSON(w, r, &settings, 1<<20); err != nil {
		return
	}
	if err := validateSettings(settings); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if !s.store.HasProxyURL(settings.ProxyURL) {
		writeAPI(w, 400, nil, "请选择已保存的代理配置")
		return
	}
	if _, err := workflow.NewClient(settings); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if err := s.store.SaveSettings(settings); err != nil {
		writeAPI(w, 500, nil, "保存设置失败")
		return
	}
	writeAPI(w, 200, settings, "")
}

func (s *Server) listProxies(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, 200, s.store.Proxies(), "")
}

func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	profile, err := saveProxyInput(s.store, model.ProxyProfile{Name: input.Name, URL: input.URL})
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusCreated, profile, "")
}

func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	profile, err := saveProxyInput(s.store, model.ProxyProfile{ID: r.PathValue("id"), Name: input.Name, URL: input.URL})
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, profile, "")
}

func saveProxyInput(dataStore *store.Store, profile model.ProxyProfile) (model.ProxyProfile, error) {
	profile.Name, profile.URL = strings.TrimSpace(profile.Name), strings.TrimSpace(profile.URL)
	if profile.Name == "" || len([]rune(profile.Name)) > 40 {
		return model.ProxyProfile{}, errors.New("代理名称不能为空且不能超过 40 个字符")
	}
	if len(profile.URL) > 1000 {
		return model.ProxyProfile{}, errors.New("代理地址不能超过 1000 个字符")
	}
	normalized, err := workflow.NormalizeProxyAddress(profile.URL)
	if err != nil {
		return model.ProxyProfile{}, err
	}
	profile.URL = normalized
	return dataStore.SaveProxy(profile)
}

func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteProxy(r.PathValue("id")); err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"deleted": true}, "")
}

func (s *Server) testProxy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if len(input.URL) > 1000 {
		writeAPI(w, 400, nil, "代理地址不能超过 1000 个字符")
		return
	}
	normalized, err := workflow.NormalizeProxyAddress(strings.TrimSpace(input.URL))
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	settings := s.store.Settings()
	result := workflow.TestProxy(r.Context(), normalized, settings.BaseURL, 15*time.Second)
	writeAPI(w, 200, result, "")
}

func (s *Server) listAdminAccounts(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, 200, s.store.AdminAccounts(), "")
}

type adminAccountInput struct {
	Label         string `json:"label"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	TeamAccountID string `json:"team_account_id"`
}

func (s *Server) createAdminAccount(w http.ResponseWriter, r *http.Request) {
	var input adminAccountInput
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	profile, err := s.saveAdminAccount("", input)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusCreated, profile, "")
}

func (s *Server) updateAdminAccount(w http.ResponseWriter, r *http.Request) {
	var input adminAccountInput
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	profile, err := s.saveAdminAccount(r.PathValue("id"), input)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, profile, "")
}

func (s *Server) saveAdminAccount(id string, input adminAccountInput) (model.AdminAccountProfile, error) {
	input.Label, input.AccessToken = strings.TrimSpace(input.Label), strings.TrimSpace(input.AccessToken)
	input.RefreshToken, input.TeamAccountID = strings.TrimSpace(input.RefreshToken), strings.TrimSpace(input.TeamAccountID)
	if input.Label == "" || len([]rune(input.Label)) > 40 {
		return model.AdminAccountProfile{}, errors.New("母号名称不能为空且不能超过 40 个字符")
	}
	token := input.AccessToken
	var existing model.AdminAccountProfile
	if id != "" && token == "" {
		profile, credentials, err := s.store.AdminAccountCredential(id)
		if err != nil {
			return model.AdminAccountProfile{}, err
		}
		existing, token = profile, credentials.AccessToken
	} else if id != "" {
		profile, _, err := s.store.AdminAccountCredential(id)
		if err != nil {
			return model.AdminAccountProfile{}, err
		}
		existing = profile
	}
	info, err := workflow.DecodeUserInfo(token)
	if err != nil {
		return model.AdminAccountProfile{}, fmt.Errorf("母号 AT 无效: %w", err)
	}
	teamID := input.TeamAccountID
	if teamID == "" {
		teamID = info.AccountID
	}
	if teamID == "" {
		return model.AdminAccountProfile{}, errors.New("无法读取团队 ID，请手动填写")
	}
	profile := model.AdminAccountProfile{
		ID: id, Label: input.Label, Email: info.Email, Name: info.Name, UserID: info.UserID,
		AccountID: info.AccountID, TeamAccountID: teamID, PlanType: info.PlanType, LastRefreshedAt: existing.LastRefreshedAt,
	}
	if expiresAt, ok := workflow.AccessTokenExpiry(token); ok {
		profile.AccessTokenExpiresAt = &expiresAt
	}
	return s.store.SaveAdminAccountCredentials(profile, input.AccessToken, input.RefreshToken)
}

func (s *Server) deleteAdminAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAdminAccount(r.PathValue("id")); err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"deleted": true}, "")
}

func (s *Server) refreshAdminAccount(w http.ResponseWriter, r *http.Request) {
	profile, _, refreshed, err := s.refreshStoredAdmin(r.Context(), r.PathValue("id"), true)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	message := "AT 和 RT 已刷新并加密保存"
	if !refreshed {
		message = "刚刚已经刷新过，冷却期内未重复调用"
	}
	writeAPI(w, 200, map[string]any{"profile": profile, "refreshed": refreshed, "message": message}, "")
}

func (s *Server) refreshStoredAdmin(ctx context.Context, id string, force bool) (model.AdminAccountProfile, store.AdminAccountCredentials, bool, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	profile, credentials, err := s.store.AdminAccountCredential(strings.TrimSpace(id))
	if err != nil {
		return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, false, err
	}
	now := time.Now()
	if profile.LastRefreshedAt != nil && now.Sub(*profile.LastRefreshedAt) < adminRefreshCooldown {
		return profile, credentials, false, nil
	}
	if !force && (profile.AccessTokenExpiresAt == nil || profile.AccessTokenExpiresAt.After(now.Add(adminRefreshAhead))) {
		return profile, credentials, false, nil
	}
	if credentials.RefreshToken == "" {
		return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, false, errors.New("母号未保存 RT，无法自动续期")
	}
	tokens, err := workflow.RefreshOAuthTokens(ctx, credentials.RefreshToken, s.store.Settings())
	if err != nil {
		return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, false, err
	}
	info, err := workflow.DecodeUserInfo(tokens.AccessToken)
	if err != nil {
		return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, false, fmt.Errorf("刷新返回的 AT 无效: %w", err)
	}
	newRefreshToken := tokens.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = credentials.RefreshToken
	}
	profile.Email, profile.Name, profile.UserID = info.Email, info.Name, info.UserID
	profile.AccountID, profile.PlanType, profile.LastRefreshedAt = info.AccountID, info.PlanType, &now
	if expiresAt, ok := workflow.AccessTokenExpiry(tokens.AccessToken); ok {
		profile.AccessTokenExpiresAt = &expiresAt
	} else if !tokens.ExpiresAt.IsZero() {
		profile.AccessTokenExpiresAt = &tokens.ExpiresAt
	} else {
		profile.AccessTokenExpiresAt = nil
	}
	profile, err = s.store.SaveAdminAccountCredentials(profile, tokens.AccessToken, newRefreshToken)
	if err != nil {
		return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, false, fmt.Errorf("保存刷新凭据失败: %w", err)
	}
	return profile, store.AdminAccountCredentials{AccessToken: tokens.AccessToken, RefreshToken: newRefreshToken}, true, nil
}

func (s *Server) currentAdminCredential(ctx context.Context, id string) (model.AdminAccountProfile, store.AdminAccountCredentials, error) {
	profile, credentials, err := s.store.AdminAccountCredential(strings.TrimSpace(id))
	if err != nil {
		return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, err
	}
	if profile.AccessTokenExpiresAt == nil || profile.AccessTokenExpiresAt.After(time.Now().Add(adminRefreshAhead)) {
		return profile, credentials, nil
	}
	if credentials.RefreshToken == "" {
		if profile.AccessTokenExpiresAt.Before(time.Now()) {
			return model.AdminAccountProfile{}, store.AdminAccountCredentials{}, errors.New("母号 AT 已过期且未保存 RT")
		}
		return profile, credentials, nil
	}
	profile, credentials, _, err = s.refreshStoredAdmin(ctx, id, false)
	return profile, credentials, err
}

func (s *Server) testAdminAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID            string `json:"id"`
		AccessToken   string `json:"access_token"`
		TeamAccountID string `json:"team_account_id"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	token, teamID := strings.TrimSpace(input.AccessToken), strings.TrimSpace(input.TeamAccountID)
	savedID := strings.TrimSpace(input.ID)
	if token == "" && savedID != "" {
		profile, credentials, err := s.currentAdminCredential(r.Context(), savedID)
		if err != nil {
			writeAPI(w, 404, nil, err.Error())
			return
		}
		token = credentials.AccessToken
		if teamID == "" {
			teamID = profile.TeamAccountID
		}
	}
	if token == "" {
		writeAPI(w, 400, nil, "Access Token 不能为空")
		return
	}
	result := workflow.TestAdminAccount(r.Context(), token, teamID, s.store.Settings())
	if savedID != "" && !result.Valid && result.HTTPStatus == http.StatusUnauthorized {
		_, credentials, refreshed, refreshErr := s.refreshStoredAdmin(r.Context(), savedID, true)
		if refreshErr == nil && refreshed {
			result = workflow.TestAdminAccount(r.Context(), credentials.AccessToken, teamID, s.store.Settings())
		}
	}
	writeAPI(w, 200, result, "")
}

func validateSettings(value model.Settings) error {
	parsed, err := url.Parse(strings.TrimSpace(value.BaseURL))
	if err != nil || parsed.Host == "" {
		return errors.New("API 基址无效")
	}
	if value.AcceptedTOSVersion == "" || value.Role == "" || value.SeatType == "" {
		return errors.New("TOS 版本、角色和席位类型不能为空")
	}
	if value.Concurrency < 1 || value.Concurrency > 20 {
		return errors.New("并发数必须在 1 到 20 之间（当前流程实际固定串行执行）")
	}
	if value.RequestTimeoutSeconds < 5 || value.RequestTimeoutSeconds > 300 {
		return errors.New("请求超时必须在 5 到 300 秒之间")
	}
	for _, delay := range []int{value.InviteDelaySeconds, value.AcceptDelaySeconds, value.TransferDelaySeconds, value.AccountIntervalSeconds} {
		if delay < 0 || delay > 120 {
			return errors.New("步骤等待必须在 0 到 120 秒之间")
		}
	}
	return nil
}

func (s *Server) inspectTokens(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AdminAccountID string   `json:"admin_account_id"`
		AdminToken     string   `json:"admin_token"`
		UserTokens     []string `json:"user_tokens"`
	}
	if err := decodeJSON(w, r, &input, 8<<20); err != nil {
		return
	}
	result := map[string]any{}
	adminToken := strings.TrimSpace(input.AdminToken)
	if strings.TrimSpace(input.AdminAccountID) != "" {
		_, credentials, err := s.currentAdminCredential(r.Context(), strings.TrimSpace(input.AdminAccountID))
		if err != nil {
			result["admin_error"] = err.Error()
		} else {
			adminToken = credentials.AccessToken
		}
	}
	if adminToken != "" {
		info, err := workflow.DecodeUserInfo(adminToken)
		if err != nil {
			result["admin_error"] = err.Error()
		} else {
			result["admin"] = info
		}
	}
	users := make([]map[string]any, 0, len(input.UserTokens))
	for index, token := range input.UserTokens {
		item := map[string]any{"index": index + 1}
		info, err := workflow.DecodeUserInfo(token)
		if err != nil {
			item["error"] = err.Error()
		} else {
			item["user"] = info
		}
		users = append(users, item)
	}
	result["users"] = users
	writeAPI(w, 200, result, "")
}

func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AdminAccountID string   `json:"admin_account_id"`
		AdminToken     string   `json:"admin_token"`
		UserTokens     []string `json:"user_tokens"`
		TeamAccountID  string   `json:"team_account_id"`
	}
	if err := decodeJSON(w, r, &input, 8<<20); err != nil {
		return
	}
	if len(input.UserTokens) > 500 {
		writeAPI(w, 400, nil, "单次最多处理 500 个子号")
		return
	}
	settings := s.store.Settings()
	adminToken, teamID := strings.TrimSpace(input.AdminToken), strings.TrimSpace(input.TeamAccountID)
	adminAccountID := strings.TrimSpace(input.AdminAccountID)
	if adminAccountID != "" {
		profile, credentials, err := s.currentAdminCredential(r.Context(), adminAccountID)
		if err != nil {
			writeAPI(w, 400, nil, err.Error())
			return
		}
		adminToken = credentials.AccessToken
		if teamID == "" {
			teamID = profile.TeamAccountID
		}
	}
	startInput := workflow.StartInput{AdminToken: adminToken, UserTokens: cleanTokens(input.UserTokens), TeamOverride: teamID, Settings: settings}
	if adminAccountID != "" {
		startInput.RefreshAdminToken = func(ctx context.Context) (string, error) {
			_, credentials, _, err := s.refreshStoredAdmin(ctx, adminAccountID, true)
			return credentials.AccessToken, err
		}
	}
	job, err := s.jobs.Start(startInput)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusAccepted, job, "")
}

func cleanTokens(tokens []string) []string {
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			result = append(result, token)
		}
	}
	return result
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeAPI(w, 404, nil, "任务不存在或已从内存清理")
		return
	}
	writeAPI(w, 200, job, "")
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.jobs.Cancel(r.PathValue("id")); err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"cancelled": true}, "")
}

func (s *Server) history(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, 200, s.store.History(), "")
}

func (s *Server) clearHistory(w http.ResponseWriter, _ *http.Request) {
	if err := s.store.ClearHistory(); err != nil {
		writeAPI(w, 500, nil, "清空历史失败")
		return
	}
	writeAPI(w, 200, map[string]bool{"cleared": true}, "")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPI(w, 400, nil, "请求格式错误: "+err.Error())
		return err
	}
	return nil
}

func writeAPI(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{OK: message == "", Data: data, Error: message})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			slog.Info("http request", "method", r.Method, "path", safePath(r.URL.Path), "duration_ms", time.Since(started).Milliseconds())
		}
	})
}

func safePath(path string) string {
	if strings.HasPrefix(path, "/api/jobs/") {
		return "/api/jobs/{id}"
	}
	return path
}
