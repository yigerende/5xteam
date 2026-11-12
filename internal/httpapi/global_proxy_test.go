package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestGlobalProxyFailoverPriorityAndClassification(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var ps []model.ProxyProfile
	for i := 0; i < 3; i++ {
		p, e := st.SaveProxy(model.ProxyProfile{Name: fmt.Sprint(i), URL: fmt.Sprintf("http://secret-user:secret-password@proxy%d.example:8080", i)})
		if e != nil {
			t.Fatal(e)
		}
		ps = append(ps, p)
	}
	cfg := model.DefaultSettings()
	cfg.ProxyURL = ps[0].URL
	cfg.ProxyFailover.BackupIDs = []string{ps[2].ID, ps[1].ID}
	st.SaveSettings(cfg)
	s := &Server{store: st}
	a, err := s.acquireOAuthProxy()
	if err != nil || a.url != ps[0].URL {
		t.Fatal(a, err)
	}
	for _, text := range []string{"HTTP 401", "HTTP 403 network timeout", "HTTP 429", "CONNECT tunnel failed, response 407", "invalid password", "验证码", "certificate verify failed", "context canceled"} {
		if isLoginProxyNetworkFailure(errors.New(text)) {
			t.Fatal("business error cooled", text)
		}
	}
	for _, text := range []string{"curl: (28) Operation timed out", "curl: (5) Could not resolve proxy", "CONNECT tunnel failed, response 504", "connection reset by peer"} {
		if !isLoginProxyNetworkFailure(errors.New(text)) {
			t.Fatal("network not classified", text)
		}
	}
	err = s.recordLoginProxyFailure(a, errors.New("CONNECT tunnel failed, response 504"))
	var cooled *cooledLoginProxyError
	if !errors.As(err, &cooled) {
		t.Fatal("not immediate failover", err)
	}
	a.Release()
	b, err := s.acquireOAuthProxy()
	if err != nil || b.url != ps[2].URL {
		t.Fatal("backup order", b, err)
	}
	s.recordLoginProxyFailure(b, errors.New("curl: (28) Operation timed out"))
	b.Release()
	c, err := s.acquireOAuthProxy()
	if err != nil || c.url != ps[1].URL {
		t.Fatal("second backup", c, err)
	}
	s.recordLoginProxyFailure(c, errors.New("connection reset"))
	c.Release()
	_, err = s.acquireOAuthProxy()
	var waiting *globalProxyWaiting
	if !errors.As(err, &waiting) {
		t.Fatal("all cooling did not wait", err)
	}
	mux := http.NewServeMux()
	s.registerGlobalProxy(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/proxies/health", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-") || strings.Contains(w.Body.String(), "proxy0.example") {
		t.Fatal("unsafe projection", w.Body.String())
	}
	cfg.OAuthProxyMode = "least_used"
	st.SaveSettings(cfg)
	_, err = s.acquireOAuthProxy()
	if !errors.As(err, &waiting) {
		t.Fatal("pool reused cooling proxy")
	}
}

func TestGlobalProxyImmediateReloginKeepsPaymentIdentity(t *testing.T) {
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"id":"paid","status":"success"}}`)
	})
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	main, _ := st.SaveProxy(model.ProxyProfile{Name: "primary", URL: "http://main.example:8080"})
	backup, _ := st.SaveProxy(model.ProxyProfile{Name: "backup", URL: "http://backup.example:8080"})
	global := st.Settings()
	global.ProxyURL = main.URL
	global.OAuthProxyMode = "global"
	global.ProxyFailover = model.DefaultGlobalProxyPolicy()
	global.ProxyFailover.BackupIDs = []string{backup.ID}
	st.SaveSettings(global)
	cfg, password, _, _ := st.ProSettings()
	cfg.RetryCount = 0
	cfg.RetryIntervalSeconds = 120
	st.SaveProSettings(cfg, password, "")
	attempts := 0
	var first time.Time
	var identity string
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
		p, _, _ := st.MailAccountCredential(current)
		lease, e := s.acquireOAuthProxy()
		if e != nil {
			return e
		}
		defer lease.Release()
		attempts++
		if attempts == 1 {
			identity = p.ProAuto.PaymentOrderID()
			first = time.Now()
			if lease.url != main.URL {
				t.Error("not primary")
			}
			return s.recordLoginProxyFailure(lease, errors.New("CONNECT tunnel failed, response 504"))
		}
		if attempts != 2 || lease.url != backup.URL || time.Since(first) > 5*time.Second || p.ProAuto.PaymentOrderID() != identity || p.ProAuto.CardID != "test-card" || p.ProAuto.RetryAttempts != 0 {
			t.Error("failed immediate retained-account retry")
		}
		if _, e = rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(current, "initial"), "user": map[string]any{"email": current}, "account": map[string]any{"id": "personal-id"}}}); e != nil {
			return e
		}
		_, e = rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(current, "new"), "refresh_token": "rt"}})
		return e
	}, Push: func(_ context.Context, email string) (model.MailAccountProfile, error) {
		return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
	}, Quota: func(_ context.Context, email string, _ bool) (model.MailAccountProfile, error) {
		return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 10} })
	}}
	rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": "tokenseek", "card_id": "test-card", "plan_code": "pro20"})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	done := waitProAutoFinished(t, s, email)
	if attempts != 2 || done.ProAuto.Status != "waiting_quota" {
		t.Fatal(attempts, done.ProAuto)
	}
}

func TestGlobalProxyWorkerAndSettingsRoundTrip(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	primary, _ := st.SaveProxy(model.ProxyProfile{Name: "primary", URL: "http://primary.example:8080"})
	backup, _ := st.SaveProxy(model.ProxyProfile{Name: "backup", URL: "http://backup.example:8080"})
	cfg := model.DefaultSettings()
	cfg.Concurrency = 1
	cfg.ProxyURL = primary.URL
	cfg.ProxyFailover.BackupIDs = []string{backup.ID}
	st.SaveSettings(cfg)
	s := &Server{store: st}
	calls := 0
	s.globalProxyProbe = func(context.Context, string) bool { calls++; return true }
	st.FailGlobalProxy(primary.URL, 0, time.Now().Add(-time.Hour))
	s.tickGlobalProxyRecovery(context.Background())
	if calls != 1 {
		t.Fatal("worker did not test", calls)
	}
	if _, ready, _ := st.ReadyGlobalProxy(primary.URL, time.Now()); !ready {
		t.Fatal("worker did not restore")
	}
	s.tickGlobalProxyRecovery(context.Background())
	if calls != 1 {
		t.Fatal("worker repeated success")
	}
	response := payCall(t, s.saveSettings, "", "", map[string]any{"global_proxy_failover": map[string]any{"backup_ids": []string{backup.ID}, "cooldown_seconds": 60, "test_interval_seconds": 9, "recovery_tests": nil}})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	saved := st.Settings()
	if saved.ProxyFailover.RecoveryTests != nil || saved.ProxyFailover.TestIntervalSeconds != 9 || saved.ProxyURL != primary.URL {
		t.Fatal("blank policy or primary lost", saved.ProxyFailover)
	}
	response = payCall(t, s.saveSettings, "", "", map[string]any{"network_retry_count": 1})
	if response.Code != 200 || st.Settings().ProxyFailover.RecoveryTests != nil {
		t.Fatal("unrelated save reset policy")
	}
	for _, body := range []any{map[string]any{"backup_ids": []string{"missing"}}, map[string]any{"recovery_tests": 0}, map[string]any{"recovery_tests": 11}, map[string]any{"test_interval_seconds": 301}} {
		response = payCall(t, s.saveSettings, "", "", map[string]any{"global_proxy_failover": body})
		if response.Code != 400 {
			t.Fatal("invalid policy accepted", response.Code)
		}
	}
	if e = st.DeleteProxy(backup.ID); e != nil {
		t.Fatal(e)
	}
	if len(st.Settings().ProxyFailover.BackupIDs) != 0 {
		t.Fatal("deleted backup retained")
	}
}

func TestGlobalProxyWaitingCanStopWithoutConsumingRetries(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	_, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "waiting-flow", Status: "running", RetryAttempts: 2, ProxyRetryAttempts: 1}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
		attempts++
		time.AfterFunc(20*time.Millisecond, cancel)
		return &globalProxyWaiting{Until: time.Now().Add(time.Hour)}
	}}
	started := time.Now()
	err = s.runProProvisioning(ctx, email, gptpay.Settings{SessionTimeoutMinutes: 1}, "", gptpay.Card{}, gptpay.CardSecret{})
	p, _, _ := st.MailAccountCredential(email)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second || attempts != 1 || p.ProAuto.RetryAttempts != 2 || p.ProAuto.ProxyRetryAttempts != 1 || p.ProAuto.RetryAt == nil {
		t.Fatal("proxy waiting did not stop cleanly", err, attempts, p.ProAuto)
	}
}
