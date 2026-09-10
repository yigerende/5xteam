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

	"chatgpt-space-merge/internal/cpa"
	"chatgpt-space-merge/internal/mailbridge"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
	"chatgpt-space-merge/webui"
)

type Server struct {
	store             *store.Store
	jobs              *workflow.Manager
	static            fs.FS
	refreshMu         sync.Mutex
	freeLocks         sync.Map
	freeRemoveLocks   sync.Map
	teamRemoveLocks   sync.Map
	sub2              *sub2.Client
	cpa               *cpa.Client
	registrationMu    sync.RWMutex
	registrationJobs  map[string]map[string]any
	mailFetchMu       sync.RWMutex
	mailFetchJobs     map[string]map[string]any
	oauthMu           sync.RWMutex
	oauthJobs         map[string]map[string]any
	oauthProxyMu      sync.Mutex
	oauthProxyActive  map[string]int
	oauthProxyCursor  uint64
	planCheckMu       sync.Mutex
	planCheckNext     time.Time
	proLocks          sync.Map
	proMonitorMu      sync.Mutex
	proMonitorRunning bool
	mail              *mailbridge.Client // retained for API compatibility; local mail flows never call it
	authMu            sync.Mutex
	sessions          map[string]time.Time
	autoMu            sync.Mutex
	autoAdminLocks    sync.Map
	autoRunning       bool
	auditQueue        chan model.AutoRotationEvent
	auditWG           sync.WaitGroup
	auditStop         chan struct{}
	auditCloseOnce    sync.Once
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
	_ = dataStore.RecoverAutoRotationClaims()
	_ = dataStore.RecoverAutoRotationTasks()
	_ = dataStore.RecoverProWorkflows()
	// Keep the execution history bounded to the requested two-day window.
	// Cleanup is a single local transaction and runs once at startup, outside
	// all request/rotation workers.
	_ = dataStore.PurgeAutoRotationHistory(time.Now().Add(-48 * time.Hour))
	server := &Server{store: dataStore, jobs: jobs, static: static, sub2: sub2.New(), cpa: cpa.New(), mail: mailbridge.New(), sessions: make(map[string]time.Time), registrationJobs: make(map[string]map[string]any), mailFetchJobs: make(map[string]map[string]any), oauthJobs: make(map[string]map[string]any), oauthProxyActive: make(map[string]int), auditQueue: make(chan model.AutoRotationEvent, 2048), auditStop: make(chan struct{})}
	server.auditWG.Add(1)
	go server.auditWriter()
	return server, nil
}

// Close flushes the diagnostic queue without participating in normal request
// latency. It is intended for graceful process shutdown only.
func (s *Server) Close() {
	if s == nil {
		return
	}
	s.auditCloseOnce.Do(func() {
		close(s.auditStop)
		s.auditWG.Wait()
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) { writeAPI(w, 200, map[string]string{"status": "ok"}, "") })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		writeAPI(w, 200, map[string]string{"status": "ready"}, "")
	})
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/me", s.me)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /api/auth/change-password", s.changePassword)
	mux.HandleFunc("POST /api/integrations/turb/register", s.importTurbRegistration)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.saveSettings)
	mux.HandleFunc("GET /api/proxies", s.listProxies)
	mux.HandleFunc("POST /api/proxies", s.createProxy)
	mux.HandleFunc("PUT /api/proxies/{id}", s.updateProxy)
	mux.HandleFunc("DELETE /api/proxies/{id}", s.deleteProxy)
	mux.HandleFunc("POST /api/proxies/test", s.testProxy)
	mux.HandleFunc("POST /api/proxies/openai-quality", s.testProxyOpenAIQuality)
	mux.HandleFunc("GET /api/admin-accounts", s.listAdminAccounts)
	mux.HandleFunc("POST /api/admin-accounts", s.createAdminAccount)
	mux.HandleFunc("PUT /api/admin-accounts/{id}", s.updateAdminAccount)
	mux.HandleFunc("DELETE /api/admin-accounts/{id}", s.deleteAdminAccount)
	mux.HandleFunc("POST /api/admin-accounts/{id}/refresh", s.refreshAdminAccount)
	mux.HandleFunc("GET /api/admin-accounts/{id}/credentials", s.adminAccountCredentials)
	mux.HandleFunc("GET /api/admin-accounts/{id}/capacity", s.adminAccountCapacity)
	mux.HandleFunc("POST /api/admin-accounts/test", s.testAdminAccount)
	mux.HandleFunc("POST /api/tokens/inspect", s.inspectTokens)
	mux.HandleFunc("GET /api/openai-accounts", s.listOpenAIAccounts)
	mux.HandleFunc("POST /api/openai-accounts", s.createOpenAIAccount)
	mux.HandleFunc("PUT /api/openai-accounts/{id}", s.updateOpenAIAccount)
	mux.HandleFunc("DELETE /api/openai-accounts/{id}", s.deleteOpenAIAccount)
	mux.HandleFunc("POST /api/openai-accounts/{id}/check", s.checkOpenAIAccount)
	mux.HandleFunc("POST /api/openai-accounts/{id}/refresh", s.refreshOpenAIAccount)
	mux.HandleFunc("POST /api/openai-accounts/refresh/batch", s.batchRefreshOpenAIAccounts)
	mux.HandleFunc("POST /api/openai-accounts/check/batch", s.batchCheckOpenAIAccounts)
	mux.HandleFunc("POST /api/openai-accounts/{id}/quota", s.queryOpenAIQuota)
	mux.HandleFunc("POST /api/openai-accounts/quota/batch", s.batchQueryOpenAIQuota)
	mux.HandleFunc("POST /api/jobs", s.startJob)
	mux.HandleFunc("POST /api/jobs/{operation}", s.startOperationJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("DELETE /api/history", s.clearHistory)
	mux.HandleFunc("GET /api/account-progress", s.accountProgress)
	mux.HandleFunc("GET /api/free-accounts", s.listFreeAccounts)
	mux.HandleFunc("GET /api/auto-rotation/settings", s.getAutoRotationSettings)
	mux.HandleFunc("PUT /api/auto-rotation/settings", s.saveAutoRotationSettings)
	mux.HandleFunc("POST /api/auto-rotation/run", s.triggerAutoRotation)
	mux.HandleFunc("GET /api/auto-rotation/runs", s.listAutoRotationRuns)
	mux.HandleFunc("GET /api/auto-rotation/runs/{id}/tasks", s.listAutoRotationTasks)
	mux.HandleFunc("GET /api/auto-rotation/events", s.listAutoRotationEvents)
	mux.HandleFunc("GET /api/auto-rotation/events/export", s.exportAutoRotationEvents)
	mux.HandleFunc("GET /api/free-accounts/{id}/events", s.listFreeAccountEvents)
	mux.HandleFunc("GET /api/free-accounts/{id}/events/export", s.exportFreeAccountEvents)
	mux.HandleFunc("POST /api/free-accounts/import", s.importFreeAccounts)
	mux.HandleFunc("DELETE /api/free-accounts/{id}", s.deleteFreeAccount)
	mux.HandleFunc("POST /api/free-accounts/{id}/join", s.joinFreeAccount)
	mux.HandleFunc("POST /api/free-accounts/{id}/oauth/start", s.startFreeAccountOAuth)
	mux.HandleFunc("GET /api/free-accounts/{id}/oauth/status/{job_id}", s.freeAccountOAuthStatus)
	mux.HandleFunc("POST /api/free-accounts/{id}/oauth", s.attachFreeAccountOAuth)
	mux.HandleFunc("POST /api/free-accounts/{id}/relogin", s.reloginFreeAccount)
	mux.HandleFunc("POST /api/free-accounts/{id}/push", s.pushFreeAccount)
	mux.HandleFunc("POST /api/free-accounts/{id}/quota", s.checkFreeAccountQuota)
	mux.HandleFunc("POST /api/free-accounts/{id}/remove", s.removeFreeAccount)
	mux.HandleFunc("PUT /api/free-accounts/{id}/stage", s.updateFreeAccountStage)
	mux.HandleFunc("PUT /api/free-accounts/{id}/policy", s.updateFreeAccountPolicy)
	mux.HandleFunc("GET /api/sub2-settings", s.getSub2Settings)
	mux.HandleFunc("PUT /api/sub2-settings", s.saveSub2Settings)
	mux.HandleFunc("POST /api/sub2-settings/test", s.testSub2Settings)
	mux.HandleFunc("GET /api/push-settings", s.getPushSettings)
	mux.HandleFunc("PUT /api/push-settings", s.savePushSettings)
	mux.HandleFunc("POST /api/push-settings/cpa/test", s.testCPASettings)
	mux.HandleFunc("GET /api/push-settings/cpa/groups", s.getCPAGroups)
	mux.HandleFunc("GET /api/mail/status", s.mailStatus)
	mux.HandleFunc("GET /api/mail/accounts", s.listMailAccounts)
	mux.HandleFunc("GET /api/pro-accounts", s.listProAccounts)
	mux.HandleFunc("POST /api/pro-accounts/access-tokens", s.proAccountAccessTokens)
	mux.HandleFunc("POST /api/pro-accounts/check-plan", s.checkProAccountPlans)
	mux.HandleFunc("POST /api/pro-accounts/{email}/check-plan", s.checkProAccountPlan)
	mux.HandleFunc("POST /api/pro-accounts/oauth/start", s.startProOAuth)
	mux.HandleFunc("POST /api/pro-accounts/oauth/complete", s.completeProOAuth)
	mux.HandleFunc("POST /api/pro-accounts/{email}/oauth/refresh", s.refreshProOAuth)
	mux.HandleFunc("POST /api/pro-accounts/{email}/push", s.pushProAccount)
	mux.HandleFunc("POST /api/pro-accounts/push", s.pushProAccounts)
	mux.HandleFunc("POST /api/pro-accounts/{email}/quota", s.checkProAccountQuota)
	mux.HandleFunc("POST /api/pro-accounts/quota", s.checkProAccountQuotas)
	mux.HandleFunc("POST /api/pro-accounts/{email}/merge", s.mergeProAccount)
	mux.HandleFunc("GET /api/pro-settings", s.getProSettings)
	mux.HandleFunc("PUT /api/pro-settings", s.saveProSettings)
	mux.HandleFunc("POST /api/pro-settings/sub2/test", s.testProSub2)
	mux.HandleFunc("GET /api/pro-settings/sub2/groups", s.getProSub2Groups)
	mux.HandleFunc("POST /api/pro-settings/cpa/test", s.testProCPA)
	mux.HandleFunc("GET /api/pro-settings/cpa/groups", s.getProCPAGroups)
	mux.HandleFunc("POST /api/mail/accounts/import", s.importMailAccounts)
	mux.HandleFunc("POST /api/mail/accounts/check-at", s.checkMailAccountsAT)
	mux.HandleFunc("POST /api/mail/accounts/{email}/check-at", s.checkMailAccountAT)
	mux.HandleFunc("DELETE /api/mail/accounts/{email}", s.deleteMailAccount)
	mux.HandleFunc("PUT /api/mail/accounts/{email}/management-scope", s.updateMailAccountManagementScope)
	mux.HandleFunc("POST /api/mail/accounts/{email}/register", s.startMailAccountRegistration)
	mux.HandleFunc("POST /api/mail/accounts/{email}/register-at", s.startMailAccountRegistrationWithAT)
	mux.HandleFunc("POST /api/mail/accounts/{email}/login", s.startMailAccountLogin)
	mux.HandleFunc("POST /api/mail/accounts/{email}/oauth", s.startMailAccountOAuth)
	mux.HandleFunc("GET /api/mail/oauth/{job_id}", s.freeAccountOAuthStatus)
	mux.HandleFunc("POST /api/mail/accounts/credentials/export-batch", s.exportMailAccountCredentialsBatch)
	mux.HandleFunc("GET /api/mail/accounts/{email}/credentials", s.exportMailAccountCredentials)
	mux.HandleFunc("GET /api/mail/login/{id}", s.mailAccountLoginStatus)
	mux.HandleFunc("GET /api/mail/register/{id}", s.mailAccountLoginStatus)
	mux.HandleFunc("GET /api/mail/register-at/{id}", s.mailAccountLoginStatus)
	mux.HandleFunc("POST /api/mail/accounts/{email}/team", s.mailAccountToTeam)
	mux.HandleFunc("POST /api/mail/fetch", s.startMailFetch)
	mux.HandleFunc("GET /api/mail/fetch/{id}", s.mailFetchStatus)
	mux.HandleFunc("GET /api/mail/messages", s.listMailMessages)
	mux.HandleFunc("POST /api/mail/messages/delete", s.deleteMailMessage)
	mux.HandleFunc("GET /api/sms/providers", s.listSMSProviders)
	mux.HandleFunc("GET /api/sms/phones", s.listSMSPhones)
	mux.HandleFunc("POST /api/sms/phones/import", s.importSMSPhones)
	mux.HandleFunc("POST /api/sms/phones/update", s.updateSMSPhone)
	mux.HandleFunc("POST /api/sms/phones/delete", s.deleteSMSPhones)
	mux.HandleFunc("POST /api/sms/phones/fetch-code", s.fetchSMSCode)
	mux.HandleFunc("POST /api/sms/phones/bind", s.bindSMSPhone)
	mux.HandleFunc("POST /api/sms/phones/unbind", s.unbindSMSPhone)
	mux.HandleFunc("GET /api/sms/platform/config", s.getSMSPlatformConfig)
	mux.HandleFunc("POST /api/sms/platform/config", s.saveSMSPlatformConfig)
	mux.HandleFunc("GET /api/sms/platform/balance", s.getSMSPlatformBalance)
	mux.HandleFunc("POST /api/sms/platform/test", s.testSMSPlatform)
	mux.HandleFunc("GET /api/sms/platform/history", s.getSMSPlatformHistory)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(mustSub(s.static, "assets")))))
	mux.HandleFunc("GET /", s.index)
	return s.requestLog(s.securityHeaders(s.authMiddleware(mux)))
}

// StartBackground runs periodic Free account quota checks until ctx is done.
func (s *Server) StartBackground(ctx context.Context) {
	go s.monitorFreeAccounts(ctx)
	go s.autoRotationLoop(ctx)
	go s.autoRotationHistoryCleanup(ctx)
	go s.monitorProAccounts(ctx)
}

func (s *Server) autoRotationHistoryCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.store.PurgeAutoRotationHistory(time.Now().Add(-48 * time.Hour))
		}
	}
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
	settings.OAuthProxyMode = normalizeOAuthProxyMode(settings.OAuthProxyMode)
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

func (s *Server) listProxies(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.Proxies(), "")
		return
	}
	page := parsePagination(r)
	items, total, err := s.store.ProxiesPage(page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取代理列表失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, nil), "")
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
	result := workflow.TestProxyExit(r.Context(), normalized, 15*time.Second)
	writeAPI(w, 200, result, "")
}

func (s *Server) testProxyOpenAIQuality(w http.ResponseWriter, r *http.Request) {
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
	result := workflow.TestProxyOpenAIQuality(r.Context(), normalized, 15*time.Second)
	writeAPI(w, 200, result, "")
}

func (s *Server) listAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.AdminAccounts(), "")
		return
	}
	page := parsePagination(r)
	items, total, summary, err := s.store.AdminAccountsPage(page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取母号列表失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, map[string]any{"summary": summary}), "")
}

func (s *Server) adminAccountCapacity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	profile, credentials, err := s.currentAdminCredential(r.Context(), id)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	settings := s.store.Settings()
	client, err := workflow.NewClient(settings)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	capacity, err := client.TeamSeatCapacity(r.Context(), credentials.AccessToken, profile.TeamAccountID)
	if err != nil {
		writeAPI(w, http.StatusBadGateway, nil, err.Error())
		return
	}
	// Keep the latest successful Team capacity request available to the
	// automatic rotation scheduler. The scheduler intentionally uses this
	// locally refreshed snapshot and does not poll the upstream endpoint on
	// every trigger.
	_ = s.store.SaveAdminCapacitySnapshot(profile.ID, capacity)
	writeAPI(w, http.StatusOK, capacity, "")
}

type openAIAccountInput struct {
	Label        string `json:"label"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) listOpenAIAccounts(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.OpenAIAccounts(), "")
		return
	}
	page := parsePagination(r)
	items, total, err := s.store.OpenAIAccountsPage(page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取 OpenAI 账号失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, nil), "")
}

func (s *Server) createOpenAIAccount(w http.ResponseWriter, r *http.Request) {
	var input openAIAccountInput
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	profile, err := s.saveOpenAIAccount("", input)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusCreated, profile, "")
}

func (s *Server) updateOpenAIAccount(w http.ResponseWriter, r *http.Request) {
	var input openAIAccountInput
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	profile, err := s.saveOpenAIAccount(r.PathValue("id"), input)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, profile, "")
}

func (s *Server) saveOpenAIAccount(id string, input openAIAccountInput) (model.OpenAIAccountProfile, error) {
	input.Label, input.AccessToken = strings.TrimSpace(input.Label), strings.TrimSpace(input.AccessToken)
	if len([]rune(input.Label)) > 40 {
		return model.OpenAIAccountProfile{}, errors.New("OpenAI 账号名称不能超过 40 个字符")
	}
	var existing model.OpenAIAccountProfile
	token := workflow.ExtractAccessToken(input.AccessToken)
	refreshToken := workflow.ExtractRefreshToken(input.RefreshToken)
	if id != "" {
		profile, credentials, err := s.store.OpenAIAccountCredential(id)
		if err != nil {
			return model.OpenAIAccountProfile{}, err
		}
		existing = profile
		if token == "" {
			token = credentials.AccessToken
		}
		if refreshToken == "" {
			refreshToken = credentials.RefreshToken
		}
	}
	info, err := workflow.DecodeUserInfo(token)
	if err != nil {
		return model.OpenAIAccountProfile{}, fmt.Errorf("OpenAI AT 无效: %w", err)
	}
	if strings.TrimSpace(info.AccountID) == "" {
		return model.OpenAIAccountProfile{}, errors.New("OpenAI AT 中缺少 chatgpt_account_id")
	}
	if input.Label == "" {
		input.Label = info.Email
		if input.Label == "" {
			input.Label = info.AccountID
		}
	}
	profile := model.OpenAIAccountProfile{
		ID: id, Label: input.Label, Email: info.Email, Name: info.Name, UserID: info.UserID,
		AccountID: info.AccountID, PlanType: info.PlanType, LastCheckedAt: existing.LastCheckedAt,
		LastCheckValid: existing.LastCheckValid, LastCheckHTTPStatus: existing.LastCheckHTTPStatus,
		LastCheckMessage: existing.LastCheckMessage, ResetCredits: existing.ResetCredits,
		ResetCreditsFetchedAt: existing.ResetCreditsFetchedAt,
	}
	if expiresAt, ok := workflow.AccessTokenExpiry(token); ok {
		profile.AccessTokenExpiresAt = &expiresAt
	} else {
		profile.AccessTokenExpiresAt = existing.AccessTokenExpiresAt
	}
	return s.store.SaveOpenAIAccount(profile, token, refreshToken)
}

func (s *Server) refreshOpenAIAccount(w http.ResponseWriter, r *http.Request) {
	profile, err := s.refreshStoredOpenAI(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, profile, "")
}

func (s *Server) refreshStoredOpenAI(ctx context.Context, id string) (model.OpenAIAccountProfile, error) {
	profile, credentials, err := s.store.OpenAIAccountCredential(strings.TrimSpace(id))
	if err != nil {
		return model.OpenAIAccountProfile{}, err
	}
	if credentials.RefreshToken == "" {
		return model.OpenAIAccountProfile{}, errors.New("OpenAI 账号未保存 RT，无法刷新 AT")
	}
	tokens, err := workflow.RefreshOAuthTokens(ctx, credentials.RefreshToken, s.store.Settings())
	if err != nil {
		return model.OpenAIAccountProfile{}, err
	}
	info, err := workflow.DecodeUserInfo(tokens.AccessToken)
	if err != nil {
		return model.OpenAIAccountProfile{}, fmt.Errorf("刷新返回的 AT 无效: %w", err)
	}
	profile.Email, profile.Name, profile.UserID = info.Email, info.Name, info.UserID
	profile.AccountID, profile.PlanType = info.AccountID, info.PlanType
	if expiresAt, ok := workflow.AccessTokenExpiry(tokens.AccessToken); ok {
		profile.AccessTokenExpiresAt = &expiresAt
	}
	newRefresh := tokens.RefreshToken
	if newRefresh == "" {
		newRefresh = credentials.RefreshToken
	}
	profile, err = s.store.SaveOpenAIAccount(profile, tokens.AccessToken, newRefresh)
	if err != nil {
		return model.OpenAIAccountProfile{}, fmt.Errorf("保存刷新凭据失败: %w", err)
	}
	return profile, nil
}

func (s *Server) batchRefreshOpenAIAccounts(w http.ResponseWriter, r *http.Request) {
	var input openAIAccountBatchInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	ids := input.IDs
	if len(ids) == 0 {
		for _, p := range s.store.OpenAIAccounts() {
			ids = append(ids, p.ID)
		}
	}
	items := make([]model.OpenAIAccountProfile, 0, len(ids))
	errs := map[string]string{}
	for _, id := range ids {
		p, err := s.refreshStoredOpenAI(r.Context(), id)
		if err != nil {
			errs[id] = err.Error()
		} else {
			items = append(items, p)
		}
	}
	writeAPI(w, 200, map[string]any{"items": items, "errors": errs}, "")
}

func (s *Server) deleteOpenAIAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteOpenAIAccount(r.PathValue("id")); err != nil {
		writeAPI(w, 404, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"deleted": true}, "")
}

func (s *Server) checkOpenAIAccount(w http.ResponseWriter, r *http.Request) {
	profile, result, err := s.performOpenAICheck(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"account": profile, "result": result}, "")
}

func (s *Server) performOpenAICheck(ctx context.Context, id string) (model.OpenAIAccountProfile, model.AdminAccountTestResult, error) {
	profile, credentials, err := s.store.OpenAIAccountCredential(strings.TrimSpace(id))
	if err != nil {
		return model.OpenAIAccountProfile{}, model.AdminAccountTestResult{}, err
	}
	result := workflow.TestAdminAccount(ctx, credentials.AccessToken, profile.AccountID, s.store.Settings())
	// Keep the OpenAI account list/API response concise. The upstream /me
	// response can be a large JSON document; only the validity state is needed
	// here (HTTP status remains available separately).
	if result.Valid {
		result.Message = "AT 有效"
	} else {
		result.Message = "AT 无效"
	}
	now := result.CheckedAt
	updated, updateErr := s.store.UpdateOpenAIAccountStatus(profile.ID, func(item *model.OpenAIAccountProfile) {
		item.LastCheckedAt = &now
		item.LastCheckValid = result.Valid
		item.LastCheckHTTPStatus = result.HTTPStatus
		item.LastCheckMessage = result.Message
	})
	if updateErr != nil {
		return model.OpenAIAccountProfile{}, result, updateErr
	}
	return updated, result, nil
}

type openAIAccountBatchInput struct {
	IDs []string `json:"ids"`
}

func (s *Server) batchCheckOpenAIAccounts(w http.ResponseWriter, r *http.Request) {
	var input openAIAccountBatchInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	ids := input.IDs
	if len(ids) == 0 {
		for _, profile := range s.store.OpenAIAccounts() {
			ids = append(ids, profile.ID)
		}
	}
	items := make([]any, 0, len(ids))
	errorsByID := map[string]string{}
	for _, id := range ids {
		profile, result, err := s.performOpenAICheck(r.Context(), id)
		if err != nil {
			errorsByID[id] = err.Error()
			continue
		}
		items = append(items, map[string]any{"account": profile, "result": result})
	}
	writeAPI(w, 200, map[string]any{"items": items, "errors": errorsByID}, "")
}

func (s *Server) queryOpenAIQuota(w http.ResponseWriter, r *http.Request) {
	profile, quota, err := s.performOpenAIQuota(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"account": profile, "quota": quota}, "")
}

func (s *Server) performOpenAIQuota(ctx context.Context, id string) (model.OpenAIAccountProfile, workflow.OpenAIResetCreditInfo, error) {
	profile, credentials, err := s.store.OpenAIAccountCredential(strings.TrimSpace(id))
	if err != nil {
		return model.OpenAIAccountProfile{}, workflow.OpenAIResetCreditInfo{}, err
	}
	client, err := workflow.NewClient(s.store.Settings())
	if err != nil {
		return model.OpenAIAccountProfile{}, workflow.OpenAIResetCreditInfo{}, err
	}
	quota, err := client.QueryOpenAIResetCredits(ctx, credentials.AccessToken, profile.AccountID)
	if err != nil {
		return model.OpenAIAccountProfile{}, workflow.OpenAIResetCreditInfo{}, err
	}
	fetched := quota.FetchedAt
	updated, updateErr := s.store.UpdateOpenAIAccountStatus(profile.ID, func(item *model.OpenAIAccountProfile) {
		count := quota.AvailableCount
		item.ResetCredits = &count
		item.ResetCreditsFetchedAt = &fetched
	})
	if updateErr != nil {
		return model.OpenAIAccountProfile{}, quota, updateErr
	}
	return updated, quota, nil
}

func (s *Server) batchQueryOpenAIQuota(w http.ResponseWriter, r *http.Request) {
	var input openAIAccountBatchInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	ids := input.IDs
	if len(ids) == 0 {
		for _, profile := range s.store.OpenAIAccounts() {
			ids = append(ids, profile.ID)
		}
	}
	items := make([]any, 0, len(ids))
	errorsByID := map[string]string{}
	for _, id := range ids {
		profile, quota, err := s.performOpenAIQuota(r.Context(), id)
		if err != nil {
			errorsByID[id] = err.Error()
			continue
		}
		items = append(items, map[string]any{"account": profile, "quota": quota})
	}
	writeAPI(w, 200, map[string]any{"items": items, "errors": errorsByID}, "")
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
		TeamRotationChildCount: existing.TeamRotationChildCount,
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

// adminAccountCredentials returns the decrypted AT/RT for the explicit
// credential viewer in the mother-account list. The list endpoint continues
// to expose presence flags only; this route is called only after an operator
// clicks the per-account view action.
func (s *Server) adminAccountCredentials(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	profile, credentials, err := s.store.AdminAccountCredential(id)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{
		"label":                profile.Label,
		"email":                profile.Email,
		"account_id":           profile.AccountID,
		"team_account_id":      profile.TeamAccountID,
		"access_token":         credentials.AccessToken,
		"refresh_token":        credentials.RefreshToken,
		"access_token_expires": profile.AccessTokenExpiresAt,
	}, "")
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
	if value.AcceptedTOSVersion == "" || value.Role == "" {
		return errors.New("TOS 版本和角色不能为空")
	}
	if value.Role != "standard-user" && value.Role != "admin" {
		return errors.New("成员角色只能选择 standard-user 或 admin")
	}
	if mode := normalizeOAuthProxyMode(value.OAuthProxyMode); mode != "global" && mode != "least_used" {
		return errors.New("OAuth 代理策略无效")
	}
	if value.Concurrency < 1 || value.Concurrency > 20 {
		return errors.New("并发数必须在 1 到 20 之间（当前流程实际固定串行执行）")
	}
	if value.RequestTimeoutSeconds < 5 || value.RequestTimeoutSeconds > 300 {
		return errors.New("请求超时必须在 5 到 300 秒之间")
	}
	if value.NetworkRetryCount < 0 || value.NetworkRetryCount > 10 {
		return errors.New("网络重试次数必须在 0 到 10 次之间")
	}
	if value.NetworkRetryInterval < 0 || value.NetworkRetryInterval > 120 {
		return errors.New("网络重试间隔必须在 0 到 120 秒之间")
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

type jobInput struct {
	AdminAccountID string   `json:"admin_account_id"`
	AdminToken     string   `json:"admin_token"`
	UserTokens     []string `json:"user_tokens"`
	TeamAccountID  string   `json:"team_account_id"`
	SeatType       string   `json:"seat_type"`
}

func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	s.startJobWithOperation(w, r, "full")
}

func (s *Server) startOperationJob(w http.ResponseWriter, r *http.Request) {
	operation := r.PathValue("operation")
	if operation != "enter" && operation != "transfer" && operation != "kick" {
		writeAPI(w, 404, nil, "不支持的任务类型")
		return
	}
	s.startJobWithOperation(w, r, operation)
}

func (s *Server) startJobWithOperation(w http.ResponseWriter, r *http.Request, operation string) {
	var input jobInput
	if err := decodeJSON(w, r, &input, 8<<20); err != nil {
		return
	}
	if len(input.UserTokens) > 500 {
		writeAPI(w, 400, nil, "单次最多处理 500 个子号")
		return
	}
	seatType := strings.TrimSpace(input.SeatType)
	if operation == "full" || operation == "enter" {
		if seatType != "default" && seatType != "prolite" {
			writeAPI(w, 400, nil, "邀请席位类型只能选择 Standard 或 Premium（5x）")
			return
		}
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
	startInput := workflow.StartInput{AdminToken: adminToken, UserTokens: cleanTokens(input.UserTokens), TeamOverride: teamID, SeatType: seatType, Settings: settings}
	if adminAccountID != "" {
		startInput.RefreshAdminToken = func(ctx context.Context) (string, error) {
			_, credentials, _, err := s.refreshStoredAdmin(ctx, adminAccountID, true)
			return credentials.AccessToken, err
		}
	}
	job, err := s.jobs.StartOperation(startInput, operation)
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

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.History(), "")
		return
	}
	page := parsePagination(r)
	items, total, summary, err := s.store.HistoryPage(page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取执行历史失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, map[string]any{"summary": summary}), "")
}

func (s *Server) clearHistory(w http.ResponseWriter, _ *http.Request) {
	if err := s.store.ClearHistory(); err != nil {
		writeAPI(w, 500, nil, "清空历史失败")
		return
	}
	writeAPI(w, 200, map[string]bool{"cleared": true}, "")
}

func (s *Server) accountProgress(w http.ResponseWriter, r *http.Request) {
	if !paginationRequested(r) {
		writeAPI(w, 200, s.store.AccountProgress(r.URL.Query().Get("team_account_id")), "")
		return
	}
	page := parsePagination(r)
	items, total, err := s.store.AccountProgressPage(r.URL.Query().Get("team_account_id"), page.Limit, page.Offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取账号进度失败: "+err.Error())
		return
	}
	writeAPI(w, 200, paginatedData(items, total, page, nil), "")
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
