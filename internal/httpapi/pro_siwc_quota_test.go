package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

type siwcQuotaFixture struct {
	*proExecutionFixture
	logins, syncs, refreshes atomic.Int32
	rejectSync, unauthorized atomic.Bool
	login                    func() (map[string]any, error)
}

func newSIWCQuotaFixture(t *testing.T) *siwcQuotaFixture {
	t.Helper()
	f := &siwcQuotaFixture{proExecutionFixture: newProExecutionFixture(t)}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "fixture"})
		case "/api/v1/admin/accounts/42/apply-oauth-credentials":
			f.syncs.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			creds, _ := body["credentials"].(map[string]any)
			if len(creds) != 2 || creds["siwc_web_access_token"] != proAutoToken(f.email, "new") || creds["siwc_web_account_id"] != "personal-id" || len(body) != 2 {
				t.Error("must patch only two web credentials on original account")
			}
			if f.rejectSync.Load() {
				http.Error(w, "unavailable", 503)
				return
			}
			write(map[string]any{"id": 42})
		case "/api/v1/admin/siwc/accounts/42/usage":
			if r.Method == "POST" {
				f.refreshes.Add(1)
			}
			if f.unauthorized.Load() {
				write(map[string]any{"error": "SIWC 网页额度 HTTP 401；保留上次结果"})
				return
			}
			write(map[string]any{"checked_at": time.Now(), "rate_limit": map[string]any{"secondary_window": map[string]any{"used_percent": 17, "limit_window_seconds": 604800, "reset_at": time.Now().Add(time.Hour).Unix()}}})
		case "/api/v1/admin/accounts/42/stats":
			write(map[string]any{"summary": map[string]any{"total_standard_cost": 12, "total_user_cost": 3}})
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(remote.Close)
	v := model.DefaultProSettings()
	v.Provider = "sub2"
	v.Sub2.URL = remote.URL
	v.Sub2.Email = "admin@example.test"
	v.Sub2.AuthMode = "siwc"
	v.Sub2.GroupIDs = []int64{7}
	if _, e := f.s.store.SaveProSettings(v, "fixture", ""); e != nil {
		t.Fatal(e)
	}
	c := model.SIWCCredentials{AuthMode: "siwc", Email: f.email, ClientID: "app", ExtAgentHostID: "host", Subject: "subject", AccessToken: "siwc-at", RefreshToken: "siwc-rt", IDToken: "siwc-id", ExpiresAt: time.Now().Add(time.Hour).Unix(), WebAccessToken: proAutoToken(f.email, "old"), WebAccountID: "personal-id"}
	if e := f.s.store.SaveMailAccountSIWC(f.email, c); e != nil {
		t.Fatal(e)
	}
	f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		started := time.Now().Add(-10 * time.Minute)
		p.ProPushStartedAt = &started
		p.PushProvider = "sub2"
		p.ProAuto.Steps = map[string]string{"recharge": "completed"}
		p.ProAuto.StageTimes = map[string]time.Time{"recharge:completed": time.Now().Add(-time.Minute)}
	})
	f.login = func() (map[string]any, error) {
		return map[string]any{"success": true, "access_token": proAutoToken(f.email, "new")}, nil
	}
	f.s.proAutoHooks = &proAutomationHooks{QuotaWebLogin: func(context.Context, string) (map[string]any, error) { f.logins.Add(1); return f.login() }}
	return f
}

func (f *siwcQuotaFixture) query() (sub2.QuotaUsage, error) {
	p, _, e := f.s.store.MailAccountCredential(f.email)
	if e != nil {
		return sub2.QuotaUsage{}, e
	}
	v, password, _, e := f.s.proAccountPushSettings(p)
	if e != nil {
		return sub2.QuotaUsage{}, e
	}
	return f.s.queryProSIWCQuota(context.Background(), p, v, password)
}

func TestSIWCQuotaFreshLoginOnlyOnceAndSingleWindow(t *testing.T) {
	f := newSIWCQuotaFixture(t)
	before, _, _ := f.s.store.MailAccountCredential(f.email)
	for range 2 {
		p, e := f.s.performProQuota(context.Background(), f.email, false)
		if e != nil || p.Quota5H != nil || p.Quota7D == nil || p.Quota7D.UsedPercent != 17 || p.QuotaStatus != "completed" {
			t.Fatalf("quota=%+v err=%v", p.Quota7D, e)
		}
	}
	after, c, _ := f.s.store.MailAccountCredential(f.email)
	if f.logins.Load() != 1 || f.syncs.Load() != 1 || f.refreshes.Load() != 1 || c.SIWC.AccessToken != "siwc-at" || c.SIWC.RefreshToken != "siwc-rt" || !c.SIWC.WebQuota.Synced || !before.ProPushStartedAt.Equal(*after.ProPushStartedAt) {
		t.Fatal("repeated login or modified original authorization/timing")
	}
}

func TestSIWCQuotaPendingSyncDoesNotRelogin(t *testing.T) {
	f := newSIWCQuotaFixture(t)
	f.rejectSync.Store(true)
	if _, e := f.query(); e == nil {
		t.Fatal("expected sync failure")
	}
	_, c, _ := f.s.store.MailAccountCredential(f.email)
	if c.SIWC.WebQuota == nil || c.SIWC.WebQuota.Synced || c.SIWC.WebAccessToken != proAutoToken(f.email, "new") {
		t.Fatal("new token was not retained")
	}
	f.rejectSync.Store(false)
	if _, e := f.query(); e != nil {
		t.Fatal(e)
	}
	if f.logins.Load() != 1 {
		t.Fatal("pending sync repeated login")
	}
}

func TestSIWCQuota401RecoveryAndThrottle(t *testing.T) {
	f := newSIWCQuotaFixture(t)
	v, _, _, _ := f.s.proAccountPushSettings(model.MailAccountProfile{})
	endpoint, _ := sub2.EndpointKey(v.Sub2.URL)
	f.s.store.UpdateMailAccountSIWCWeb(f.email, "app", func(c *model.SIWCCredentials) error {
		c.WebQuota = &model.SIWCWebQuotaState{LoginAt: time.Now(), Synced: true, Endpoint: endpoint, AccountID: 42}
		return nil
	})
	f.unauthorized.Store(true)
	if _, e := f.query(); !errors.Is(e, sub2.ErrSIWCWebUnauthorized) {
		t.Fatal(e)
	}
	if _, e := f.query(); e == nil {
		t.Fatal("expected throttled failure")
	}
	if f.logins.Load() != 1 {
		t.Fatal("401 caused repeated login")
	}
}

func TestSIWCQuotaRejectWrongLoginAndPreserveAccount(t *testing.T) {
	for _, mode := range []string{"wrong_email", "same_token", "failure", "cancelled_target"} {
		t.Run(mode, func(t *testing.T) {
			f := newSIWCQuotaFixture(t)
			f.login = func() (map[string]any, error) {
				token := proAutoToken(f.email, "new")
				switch mode {
				case "wrong_email":
					token = proAutoToken("other@example.test", "new")
				case "same_token":
					token = proAutoToken(f.email, "old")
				case "failure":
					return nil, errors.New("network unavailable")
				case "cancelled_target":
					f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.Sub2AccountID = 43 })
				}
				return map[string]any{"success": true, "access_token": token}, nil
			}
			if _, e := f.query(); e == nil {
				t.Fatal("accepted invalid login")
			}
			_, c, e := f.s.store.MailAccountCredential(f.email)
			if e != nil || c.SIWC.RefreshToken != "siwc-rt" || f.syncs.Load() != 0 {
				t.Fatal("login failure changed/deleted original account")
			}
			if mode != "cancelled_target" {
				f.query()
				if f.logins.Load() != 1 {
					t.Fatal("failed login not throttled")
				}
			}
		})
	}
}

func TestSIWCQuotaWebUpdatePreservesRenewedTokens(t *testing.T) {
	f := newSIWCQuotaFixture(t)
	f.login = func() (map[string]any, error) {
		_, c, _ := f.s.store.MailAccountCredential(f.email)
		c.SIWC.AccessToken = "renewed-at"
		c.SIWC.RefreshToken = "renewed-rt"
		if e := f.s.store.SaveMailAccountSIWC(f.email, *c.SIWC); e != nil {
			t.Fatal(e)
		}
		return map[string]any{"success": true, "access_token": proAutoToken(f.email, "new")}, nil
	}
	if _, e := f.query(); e != nil {
		t.Fatal(e)
	}
	_, c, _ := f.s.store.MailAccountCredential(f.email)
	if c.SIWC.AccessToken != "renewed-at" || c.SIWC.RefreshToken != "renewed-rt" {
		t.Fatal("overwrote concurrent SIWC renewal")
	}
	if strings.Contains(c.SIWC.WebAccessToken, "siwc-at") {
		t.Fatal("used SIWC AT as webpage token")
	}
}
