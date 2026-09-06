package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func makeToken(userID, accountID, email string) string {
	claims := map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_user_id": userID, "chatgpt_account_id": accountID, "chatgpt_plan_type": "team"},
		"https://api.openai.com/profile": map[string]any{"email": email, "name": strings.Split(email, "@")[0]},
	}
	payload, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestDecodeUserInfo(t *testing.T) {
	info, err := DecodeUserInfo(makeToken("user-1", "account-1", "one@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if info.UserID != "user-1" || info.AccountID != "account-1" || info.Email != "one@example.com" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if _, err := DecodeUserInfo("not-a-token"); err == nil {
		t.Fatal("expected invalid token error")
	}
}

func TestExtractAccessTokenFromSessionJSON(t *testing.T) {
	token := makeToken("user-1", "account-1", "one@example.com")
	input := `{"user":{"email":"one@example.com"},"credentials":{"accessToken":"` + token + `"}}`
	if got := ExtractAccessToken(input); got != token {
		t.Fatalf("extracted token mismatch: %q", got)
	}
	if got := ExtractAccessToken(token); got != token {
		t.Fatalf("raw token changed: %q", got)
	}
}

func TestClientRequestShape(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID := r.Header.Get("chatgpt-account-id")
		if r.Header.Get("Authorization") == "" || accountID != "team-1" {
			t.Errorf("missing auth headers")
		}
		if r.URL.Path == "/accounts/team-1/invites" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode invite body: %v", err)
			}
			if body["seat_type"] != "prolite" || body["role"] != "standard-user" || body["resend_emails"] != true {
				t.Errorf("unexpected invite body: %#v", body)
			}
			if _, ok := body["flow_id"].(string); !ok {
				t.Errorf("missing flow_id: %#v", body)
			}
			if _, ok := body["submission_id"].(string); !ok {
				t.Errorf("missing submission_id: %#v", body)
			}
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	settings.RequestTimeoutSeconds = 5
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = client.Invite(ctx, "admin", "team-1", "one@example.com", "prolite"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Accept(ctx, "user", "team-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Transfer(ctx, "user", "team-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Kick(ctx, "admin", "team-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /accounts/team-1/invites", "POST /accounts/team-1/invites/accept", "POST /accounts/transfer", "DELETE /accounts/team-1/users/user-1"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestTransferUsesWorkspaceAccountContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/transfer" {
			t.Errorf("unexpected transfer request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "team-1" {
			t.Errorf("transfer account context = %q, want team-1", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode transfer body: %v", err)
		} else {
			if body["workspace_id"] != "team-1" || body["target_account_id"] != "team-1" || body["transfer_personal"] != true {
				t.Errorf("unexpected transfer body: %#v", body)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Transfer(context.Background(), "user", "team-1"); err != nil {
		t.Fatal(err)
	}
}

func TestQueryOpenAIResetCreditsUsesCodexHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("chatgpt-account-id") != "acct-1" {
			t.Fatalf("missing quota auth headers: auth=%q account=%q", r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id"))
		}
		if r.Header.Get("openai-beta") != "codex-1" || r.Header.Get("originator") != "Codex Desktop" {
			t.Fatalf("missing Codex headers: beta=%q originator=%q", r.Header.Get("openai-beta"), r.Header.Get("originator"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/wham/usage" {
			_, _ = w.Write([]byte(`{"rate_limit_reset_credits":{"available_count":7}}`))
			return
		}
		if r.URL.Path == "/wham/rate-limit-reset-credits" {
			_, _ = w.Write([]byte(`{"availableCount":2,"credits":[{"status":"available"},{"status":"redeemed"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := client.QueryOpenAIResetCredits(context.Background(), "access-token", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if quota.AvailableCount != 2 || quota.FetchedAt.IsZero() {
		t.Fatalf("unexpected quota: %+v", quota)
	}
}

func TestQueryOpenAIResetCreditsHonorsAuthoritativeZeroDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/wham/usage" {
			_, _ = w.Write([]byte(`{"rate_limit_reset_credits":{"available_count":5}}`))
			return
		}
		if r.URL.Path == "/wham/rate-limit-reset-credits" {
			_, _ = w.Write([]byte(`[{"reset_type":"codex_rate_limits","status":"redeemed"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := client.QueryOpenAIResetCredits(context.Background(), "access-token", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if quota.AvailableCount != 0 {
		t.Fatalf("detail endpoint should override usage fallback with zero, got %d", quota.AvailableCount)
	}
}

func TestProxyConnectivity(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "target.invalid" {
			t.Errorf("proxy target = %s", r.URL.String())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	result := TestProxy(context.Background(), proxy.URL, "http://target.invalid/backend-api", 2*time.Second)
	if !result.Reachable || result.HTTPStatus != http.StatusNoContent {
		t.Fatalf("unexpected proxy result: %+v", result)
	}
}

func TestGlobalProxyIsUsedForChatGPTRequests(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "127.0.0.1:65534" || r.URL.Path != "/backend-api/accounts/team-1/invites" {
			t.Errorf("unexpected proxied target: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer proxy.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = "http://127.0.0.1:65534/backend-api"
	settings.ProxyURL = proxy.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Invite(context.Background(), "admin", "team-1", "one@example.com", "default"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("proxy calls = %d, want 1", calls.Load())
	}
}

func TestGlobalProxyIsUsedForOpenAIOAuthRefresh(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "127.0.0.1:65533" || r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected proxied OAuth target: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-at","refresh_token":"new-rt","expires_in":3600}`))
	}))
	defer proxy.Close()
	settings := model.DefaultSettings()
	settings.ProxyURL = proxy.URL
	tokens, err := refreshOAuthTokensAt(context.Background(), "http://127.0.0.1:65533/oauth/token", "old-rt", settings)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || tokens.AccessToken != "new-at" || tokens.RefreshToken != "new-rt" {
		t.Fatalf("unexpected OAuth result: calls=%d tokens=%+v", calls.Load(), tokens)
	}
}

func TestProxyAuthenticationFailure(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusProxyAuthRequired) }))
	defer proxy.Close()
	result := TestProxy(context.Background(), proxy.URL, "http://target.invalid", 2*time.Second)
	if result.Reachable || result.HTTPStatus != http.StatusProxyAuthRequired {
		t.Fatalf("unexpected proxy result: %+v", result)
	}
}

func TestNormalizeProxyAddress(t *testing.T) {
	got, err := NormalizeProxyAddress("proxy.example.com:10000:demo-user:demo-password")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://demo-user:demo-password@proxy.example.com:10000"
	if got != want {
		t.Fatalf("normalized proxy = %q, want %q", got, want)
	}
	got, err = NormalizeProxyAddress("http://user:p%40ss@127.0.0.1:7890")
	if err != nil || got != "http://user:p%40ss@127.0.0.1:7890" {
		t.Fatalf("normal URL changed unexpectedly: %q, %v", got, err)
	}
	if _, err := NormalizeProxyAddress("host:bad:user:pass"); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestConnectionClosedRetriesCurrentStep(t *testing.T) {
	settings := model.DefaultSettings()
	settings.NetworkRetryCount = 2
	settings.NetworkRetryInterval = 0
	calls := 0
	retries := 0
	manager := &Manager{}
	response, err := manager.callStepWithRetry(context.Background(), settings, func(context.Context) (Response, error) {
		calls++
		if calls < 3 {
			return Response{}, errors.New("网络请求失败: curl: (56) Connection closed abruptly")
		}
		return Response{StatusCode: http.StatusOK}, nil
	}, func(retry, total, interval int) {
		retries++
		if retry != retries || total != 2 || interval != 0 {
			t.Fatalf("unexpected retry callback: retry=%d total=%d interval=%d", retry, total, interval)
		}
	})
	if err != nil || response.StatusCode != http.StatusOK || calls != 3 || retries != 2 {
		t.Fatalf("response=%+v err=%v calls=%d retries=%d", response, err, calls, retries)
	}
}

func TestBusinessErrorDoesNotRetry(t *testing.T) {
	settings := model.DefaultSettings()
	settings.NetworkRetryCount = 5
	settings.NetworkRetryInterval = 0
	calls := 0
	manager := &Manager{}
	_, err := manager.callStepWithRetry(context.Background(), settings, func(context.Context) (Response, error) {
		calls++
		return Response{StatusCode: http.StatusUnprocessableEntity}, errors.New("HTTP 422: invalid request")
	}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one call", err, calls)
	}
}

func TestCancellingStopsRetryWait(t *testing.T) {
	settings := model.DefaultSettings()
	settings.NetworkRetryCount = 2
	settings.NetworkRetryInterval = 30
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	manager := &Manager{}
	_, err := manager.callStepWithRetry(ctx, settings, func(context.Context) (Response, error) {
		calls++
		return Response{}, errors.New("curl: (56) Connection closed abruptly")
	}, func(_, _, _ int) { cancel() })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestOAuthRefreshUsesConfiguredProxyAndRotatesTokens(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.String() != "http://oauth.invalid/token" {
			t.Errorf("unexpected refresh target: %s %s", r.Method, r.URL.String())
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != openAIClientID || r.Form.Get("scope") != openAIRefreshScope {
			t.Errorf("unexpected refresh form: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer proxy.Close()
	settings := model.DefaultSettings()
	settings.ProxyURL = proxy.URL
	result, err := refreshOAuthTokensAt(context.Background(), "http://oauth.invalid/token", "old-refresh", settings)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != "new-access" || result.RefreshToken != "new-refresh" || result.ExpiresAt.IsZero() {
		t.Fatalf("unexpected refreshed tokens: %+v", result)
	}
}

func TestAdminAccountCheckUsesConfiguredProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/backend-api/me") {
			t.Errorf("unexpected check request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing authorization")
		}
		if r.Header.Get("x-openai-target-path") != "/backend-api/me" || r.Header.Get("x-openai-target-route") != "/backend-api/me" {
			t.Error("missing target headers")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = "http://127.0.0.1:18122/backend-api"
	settings.ProxyURL = proxy.URL
	result := TestAdminAccount(context.Background(), makeToken("admin", "team-1", "admin@example.com"), "team-1", settings)
	if !result.Valid || result.HTTPStatus != http.StatusNoContent {
		t.Fatalf("unexpected account test: %+v", result)
	}
}

type memoryHistory struct {
	mu      sync.Mutex
	entries []model.HistoryEntry
}

func (m *memoryHistory) AddHistory(entry model.HistoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func TestManagerRunsCompleteWorkflow(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL, settings.Concurrency = server.URL, 1
	settings.InviteDelaySeconds, settings.AcceptDelaySeconds, settings.TransferDelaySeconds = 0, 0, 0
	history := &memoryHistory{}
	manager := NewManager(history)
	job, err := manager.Start(StartInput{AdminToken: makeToken("admin-user", "team-1", "admin@example.com"), UserTokens: []string{makeToken("child-1", "personal-1", "child@example.com")}, SeatType: "default", Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForJob(t, manager, job.ID)
	if completed.Status != "completed" || completed.Succeeded != 1 || completed.Failed != 0 {
		t.Fatalf("unexpected job: %+v", completed)
	}
	if len(calls) != 4 {
		t.Fatalf("got %d calls: %v", len(calls), calls)
	}
	deadline := time.Now().Add(1 * time.Second)
	for {
		history.mu.Lock()
		ready := len(history.entries) > 0
		valid := ready && len(history.entries) == 1 && history.entries[0].Results[0].Email == "child@example.com"
		history.mu.Unlock()
		if ready {
			if !valid {
				t.Fatalf("unexpected history: %+v", history.entries)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("history entry was not written")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestManagerRunsSplitOperations(t *testing.T) {
	tests := []struct {
		operation string
		seatType  string
		wantCalls []string
	}{
		{operation: "enter", seatType: "default", wantCalls: []string{"POST /accounts/team-1/invites", "POST /accounts/team-1/invites/accept"}},
		{operation: "transfer", wantCalls: []string{"POST /accounts/transfer"}},
		{operation: "kick", wantCalls: []string{"DELETE /accounts/team-1/users/child"}},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			settings := model.DefaultSettings()
			settings.BaseURL = server.URL
			settings.InviteDelaySeconds, settings.AcceptDelaySeconds, settings.TransferDelaySeconds = 0, 0, 0
			manager := NewManager(&memoryHistory{})
			job, err := manager.StartOperation(StartInput{
				AdminToken: makeToken("admin", "team-1", "admin@example.com"),
				UserTokens: []string{makeToken("child", "personal", "child@example.com")},
				SeatType:   test.seatType,
				Settings:   settings,
			}, test.operation)
			if err != nil {
				t.Fatal(err)
			}
			completed := waitForJob(t, manager, job.ID)
			if completed.Status != "completed" || completed.Operation != test.operation {
				t.Fatalf("unexpected job: %+v", completed)
			}
			if got, want := strings.Join(calls, "|"), strings.Join(test.wantCalls, "|"); got != want {
				t.Fatalf("calls = %q, want %q", got, want)
			}
			if len(completed.Results[0].Steps) != len(test.wantCalls) {
				t.Fatalf("steps = %+v", completed.Results[0].Steps)
			}
		})
	}
}

func TestManagerRequiresSeatTypeForInvitationTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	manager := NewManager(&memoryHistory{})
	_, err := manager.Start(StartInput{
		AdminToken: makeToken("admin", "team-1", "admin@example.com"),
		UserTokens: []string{makeToken("child", "personal", "child@example.com")},
		Settings:   settings,
	})
	if err == nil || !strings.Contains(err.Error(), "邀请席位类型") {
		t.Fatalf("expected task-level seat type validation, got %v", err)
	}
}

func TestManagerRunsUsersSerially(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL, settings.Concurrency = server.URL, 20
	settings.InviteDelaySeconds, settings.AcceptDelaySeconds, settings.TransferDelaySeconds = 0, 0, 0
	manager := NewManager(&memoryHistory{})
	job, err := manager.Start(StartInput{
		AdminToken: makeToken("admin-user", "team-1", "admin@example.com"),
		UserTokens: []string{makeToken("child-1", "personal-1", "one@example.com"), makeToken("child-2", "personal-2", "two@example.com")},
		SeatType:   "default",
		Settings:   settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForJob(t, manager, job.ID)
	if completed.Status != "completed" || completed.Succeeded != 2 {
		t.Fatalf("unexpected serial job: %+v", completed)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("workflow ran concurrently: max active requests = %d", got)
	}
}

func TestManagerCleansUpAfterTransferFailure(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/accounts/transfer" {
			http.Error(w, `{"message":"transfer rejected"}`, http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	settings.InviteDelaySeconds, settings.AcceptDelaySeconds, settings.TransferDelaySeconds = 0, 0, 0
	manager := NewManager(&memoryHistory{})
	job, err := manager.Start(StartInput{AdminToken: makeToken("admin", "team-1", "admin@example.com"), UserTokens: []string{makeToken("child", "personal", "child@example.com")}, SeatType: "default", Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForJob(t, manager, job.ID)
	if completed.Status != "failed" || completed.Results[0].Steps[3].Status != "completed" {
		t.Fatalf("cleanup did not complete: %+v", completed.Results[0])
	}
	if got := calls[len(calls)-1]; got != "DELETE /accounts/team-1/users/child" {
		t.Fatalf("last call = %s", got)
	}
}

func TestManagerRejectsDuplicateUser(t *testing.T) {
	settings := model.DefaultSettings()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	settings.BaseURL = server.URL
	settings.InviteDelaySeconds, settings.AcceptDelaySeconds, settings.TransferDelaySeconds = 0, 0, 0
	history := &memoryHistory{}
	manager := NewManager(history)
	token := makeToken("child", "personal", "child@example.com")
	job, err := manager.Start(StartInput{AdminToken: makeToken("admin", "team-1", "admin@example.com"), UserTokens: []string{token, token}, SeatType: "default", Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if job.Results[1].Status != "failed" || !strings.Contains(job.Results[1].Error, "重复子号") {
		t.Fatalf("duplicate was not rejected: %+v", job.Results[1])
	}
	manager.Cancel(job.ID)
}

func waitForJob(t *testing.T, manager *Manager, id string) model.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatal("job disappeared")
		}
		if job.Status != "queued" && job.Status != "running" && job.Status != "cancelling" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not complete")
	return model.Job{}
}
