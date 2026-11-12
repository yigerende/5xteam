package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSIWCIntegrationLostResponseRecoveryCompletesBothStages(t *testing.T) {
	f := newProExecutionFixture(t)
	var callbackCalls int
	var key string
	var cfg model.ProSettings
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "test"})
		case "/api/v1/admin/openai/siwc/integration/generate-auth-url":
			write(sub2.SIWCAuthURL{AuthURL: "https://auth.openai.com/api/accounts/authorize", SessionID: "session", HostID: "host"})
		case "/api/v1/admin/openai/siwc/integration/callback":
			callbackCalls++
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if fmt.Sprint(b["group_ids"]) != "[7 8]" || b["concurrency"] != float64(22) {
				t.Error("push settings lost")
			}
			if key == "" {
				key = r.Header.Get("Idempotency-Key")
			} else if key != r.Header.Get("Idempotency-Key") {
				t.Error("idempotency key changed")
			}
			if callbackCalls == 1 {
				w.WriteHeader(502)
				fmt.Fprint(w, "private response must not escape")
				return
			}
			tokens := model.SIWCCredentials{AuthMode: "siwc", Email: f.email, ExtAgentHostID: "host", AccessToken: "at", RefreshToken: "rt", IDToken: "id", Subject: "subject", ClientID: "client", PlanUseEnabled: true, ExpiresAt: time.Now().Add(time.Hour).Unix()}
			write(map[string]any{"account": map[string]any{"id": 73, "name": f.email, "extra": map[string]any{"auth_provider": "siwc"}}, "tokens": tokens})
		default:
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	cfg = model.DefaultProSettings()
	cfg.Sub2 = model.DefaultSub2Settings()
	cfg.Sub2.AuthMode = "siwc"
	cfg.Sub2.SIWCAuthorizationSource = "sub2api"
	cfg.Sub2.URL = remote.URL
	cfg.Sub2.Email = "admin@test.invalid"
	cfg.Sub2.GroupIDs = []int64{7, 8}
	cfg.Sub2.AccountConcurrency = 22
	f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.PushStatus = ""
		p.Sub2AccountID = 0
		p.ProPushDownstream = nil
		p.ProAuto = model.ProAutoState{ID: "flow", Steps: map[string]string{}}
	})
	if _, e := f.s.store.SaveProSettings(cfg, "test", ""); e != nil {
		t.Fatal(e)
	}
	b := &siwcBroker{s: f.s, ctx: context.Background(), settings: cfg, password: "test", email: f.email, hostID: "host", proxy: "http://user:private@proxy.test:80"}
	if _, err := b.call("siwc_generate", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.call("siwc_callback", map[string]any{"callback_url": "http://127.0.0.1:1456/auth/callback?code=private", "web_session": map[string]any{"access_token": proAutoToken(f.email, "web")}}); err == nil {
		t.Fatal("lost reply must not mark complete")
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.PushStatus == "completed" {
		t.Fatal("push reported before confirmation")
	}
	p, found, err := f.s.recoverSIWCImport(context.Background(), p, cfg, "test")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if p.Sub2AccountID != 73 || p.ProAuto.Steps["oauth"] != "completed" || p.ProAuto.Steps["push"] != "completed" || p.ProAuto.OAuthSource != "siwc_sub2api" {
		t.Fatal("stages or binding not saved")
	}
	_, c, err := f.s.store.MailAccountCredential(f.email)
	if err != nil || c.SIWC == nil || c.SIWC.WebAccountID != "personal-id" {
		t.Fatal("encrypted credentials not saved")
	}
	_, found, err = f.s.recoverSIWCImport(context.Background(), p, cfg, "test")
	if err != nil || found || callbackCalls != 2 {
		t.Fatal("completed import submitted again")
	}
}
