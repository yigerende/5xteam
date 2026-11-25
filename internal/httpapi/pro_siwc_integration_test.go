package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
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

func TestSIWCIntegration409ReauthorizesOnceWithoutPayment(t *testing.T) {
	for _, scenario := range []string{"success", "twice_failed", "in_progress", "legacy_unknown", "stopped", "pending_replay"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProExecutionFixture(t)
			var calls int
			var keys []string
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
				switch r.URL.Path {
				case "/api/v1/auth/login":
					write(map[string]any{"access_token": "test"})
				case "/api/v1/admin/openai/siwc/integration/generate-auth-url":
					write(sub2.SIWCAuthURL{AuthURL: "https://auth.openai.com/api/accounts/authorize", SessionID: fmt.Sprint("session-", calls), HostID: "host"})
				case "/api/v1/admin/openai/siwc/integration/callback":
					calls++
					keys = append(keys, r.Header.Get("Idempotency-Key"))
					if calls == 1 || scenario != "success" && scenario != "pending_replay" || scenario == "pending_replay" && calls == 2 {
						if scenario == "stopped" {
							f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true; p.ProAuto.StopVersion++ })
						}
						reason := "SIWC_CALLBACK_UNCONFIRMED"
						meta := map[string]string{"failure_stage": "exchange", "reauthorize_allowed": "true"}
						if scenario == "in_progress" {
							reason = "SIWC_CALLBACK_IN_PROGRESS"
						}
						if scenario == "legacy_unknown" {
							meta = nil
						}
						w.WriteHeader(409)
						json.NewEncoder(w).Encode(map[string]any{"code": 409, "reason": reason, "metadata": meta})
						return
					}
					tokens := model.SIWCCredentials{AuthMode: "siwc", Email: f.email, ExtAgentHostID: "host", AccessToken: "at", RefreshToken: "rt", IDToken: "id", Subject: "subject", ClientID: "client", PlanUseEnabled: true, ExpiresAt: time.Now().Add(time.Hour).Unix()}
					write(map[string]any{"account": map[string]any{"id": 73, "name": f.email, "extra": map[string]any{"auth_provider": "siwc"}}, "tokens": tokens})
				default:
					t.Error("unexpected request (must not pay)", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer remote.Close()
			cfg := model.DefaultProSettings()
			cfg.Provider = "sub2"
			cfg.Sub2 = model.DefaultSub2Settings()
			cfg.Sub2.AuthMode = "siwc"
			cfg.Sub2.SIWCAuthorizationSource = "sub2api"
			cfg.Sub2.URL = remote.URL
			cfg.Sub2.Email = "admin@test.invalid"
			cfg.Sub2.GroupIDs = []int64{7, 8}
			if _, err := f.s.store.SaveProSettings(cfg, "test", ""); err != nil {
				t.Fatal(err)
			}
			a := model.ProAutoState{ID: "flow", OrderID: "paid", Stage: "oauth", Status: "running", RetryAttempts: 2, RestockBinding: "original-pool", Steps: map[string]string{"recharge": "completed", "oauth": "running"}}
			f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.ProAuto = a
				p.Sub2AccountID = 0
				p.PushStatus = ""
				p.CPAAuthFileName = ""
				p.ProPushDownstream = nil
				p.ProRestockTaskID = "original-task"
			})
			snapshot := gptpay.Snapshot{}
			snapshot.Input.Session.Account.ID = "personal-id"
			if err := f.s.store.CreateGPTPayOrder(gptpay.Order{ID: "paid", Email: f.email, Status: "success", CreatedAt: time.Now(), UpdatedAt: time.Now()}, snapshot); err != nil {
				t.Fatal(err)
			}
			b := &siwcBroker{s: f.s, ctx: context.Background(), email: f.email, password: "test", hostID: "host", settings: cfg, paid: true}
			if _, err := b.call("siwc_generate", nil); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"callback_url": "http://127.0.0.1:1456/auth/callback?code=private", "web_session": map[string]any{"access_token": proAutoToken(f.email, "web")}}
			if scenario == "pending_replay" {
				b.paid = false
			}
			out, err := b.call("siwc_callback", args)
			if scenario == "pending_replay" {
				if err == nil {
					t.Fatal("expected first failure")
				}
				p, _, _ := f.s.store.MailAccountCredential(f.email)
				_, found, e := f.s.recoverSIWCImport(context.Background(), p, cfg, "test")
				if e != nil || found {
					t.Fatal(found, e)
				}
				b.paid = true
				b.generated = nil
			} else if scenario == "success" || scenario == "twice_failed" {
				if err != nil || out.(map[string]any)["reauthorize"] != true || b.generated != nil {
					t.Fatal(out, err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe conflict recovered")
				}
				return
			}
			if _, err = b.call("siwc_generate", nil); err != nil {
				t.Fatal(err)
			}
			_, err = b.call("siwc_callback", args)
			if (err != nil) != (scenario == "twice_failed") {
				t.Fatal(err)
			}
			if keys[0] == keys[len(keys)-1] {
				t.Fatal("new authorization replayed old callback key")
			}
			if scenario == "pending_replay" && keys[0] != keys[1] {
				t.Fatal("pending request key changed before reconciliation")
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if p.ProAuto.SIWCImportRecoveries != 1 || p.ProAuto.ID != "flow" || p.ProAuto.OrderID != "paid" || p.ProAuto.RetryAttempts != 2 || p.ProRestockTaskID != "original-task" {
				t.Fatal("workflow changed", p.ProAuto)
			}
			if scenario != "twice_failed" && (p.Sub2AccountID != 73 || p.ProAuto.Steps["push"] != "completed") {
				t.Fatal("recovery not complete")
			}
			order, _, _ := f.s.store.GPTPayOrder("paid")
			if order.Status != "success" {
				t.Fatal("payment changed")
			}
		})
	}
}

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
	recovered, err := b.call("siwc_retry_prepare", nil)
	if err != nil || recovered.(map[string]any)["completed"] != true {
		t.Fatal(recovered, err)
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if p.Sub2AccountID != 73 || p.ProAuto.Steps["oauth"] != "completed" || p.ProAuto.Steps["push"] != "completed" || p.ProAuto.OAuthSource != "siwc_sub2api" {
		t.Fatal("stages or binding not saved")
	}
	_, c, err := f.s.store.MailAccountCredential(f.email)
	if err != nil || c.SIWC == nil || c.SIWC.WebAccountID != "personal-id" {
		t.Fatal("encrypted credentials not saved")
	}
	_, found, err := f.s.recoverSIWCImport(context.Background(), p, cfg, "test")
	if err != nil || found || callbackCalls != 2 {
		t.Fatal("completed import submitted again")
	}
}
