package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/sub2"
)

func TestSIWCManualPushQuotaCadence(t *testing.T) {
	now := time.Now()
	checked := now.Add(-30 * time.Second)
	p := model.MailAccountProfile{QuotaCheckedAt: &checked}
	if siwcQuotaSweepDue(p, now, time.Time{}, 120) {
		t.Fatal("SIWC quota must not follow a shorter ordinary downstream sweep")
	}
	if !siwcQuotaSweepDue(p, now, time.Time{}, 10) {
		t.Fatal("SIWC quota must run independently of a longer ordinary sweep")
	}
	p.QuotaCheckedAt = nil
	if siwcQuotaSweepDue(p, now, now.Add(time.Minute), 10) {
		t.Fatal("failed queries must respect retry cadence")
	}
}

func TestSIWCTransferRemovesThenCreatesOnlyOnce(t *testing.T) {
	f := newProExecutionFixture(t)
	sequence := []string{}
	groups := []int64{1, 2, 9}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "test"})
		case "/api/v1/admin/accounts/42":
			if r.Method == "GET" {
				write(map[string]any{"id": 42, "group_ids": groups})
				return
			}
			var body struct {
				Groups []int64 `json:"group_ids"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			groups = body.Groups
			sequence = append(sequence, "remove")
			if f.s.proSIWCBlock() == "" {
				t.Error("source removed without durable pause")
			}
			write(map[string]any{"id": 42})
		case "/api/v1/admin/accounts/42/stats":
			write(map[string]any{"summary": map[string]any{"total_standard_cost": 12, "total_user_cost": 3}})
		case "/api/v1/admin/accounts":
			sequence = append(sequence, "create")
			var input struct {
				Credentials map[string]any `json:"credentials"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			if input.Credentials["auth_mode"] != "siwc" || input.Credentials["siwc_app_id"] != "app" {
				t.Error("wrong pushed SIWC credentials")
			}
			write(map[string]any{"id": 9, "name": "siwc"})
		default:
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	cfg := model.DefaultProSIWCSettings()
	cfg.Sub2.URL = remote.URL
	cfg.Sub2.Email = "siwc@example.test"
	cfg.Sub2.GroupIDs = []int64{7}
	cfg.MonitorGroupIDs = []int64{7}
	if _, e := f.s.store.SaveProSIWCSettings(cfg, "test"); e != nil {
		t.Fatal(e)
	}
	source := model.DefaultProSettings()
	source.Sub2.URL = remote.URL
	source.Sub2.Email = "source@example.test"
	source.Sub2.GroupIDs = []int64{1, 2}
	web := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())))
	tokens := model.SIWCCredentials{AuthMode: "siwc", Email: f.email, AccessToken: "at", RefreshToken: "rt", IDToken: "id", ClientID: "app", ExtAgentHostID: "host", Subject: "subject", ExpiresAt: time.Now().Add(time.Hour).Unix(), WebAccessToken: "e30." + web + ".sig"}
	if e := f.s.store.SaveMailAccountSIWC(f.email, tokens); e != nil {
		t.Fatal(e)
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.PushProvider = "sub2" })
	if e := f.s.queueProSIWC(p, source, "test"); e != nil {
		t.Fatal(e)
	}
	if e := f.s.runProSIWC(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	if e := f.s.runProSIWC(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if strings.Join(sequence, ",") != "remove,create" || fmt.Sprint(groups) != "[9]" || p.CostDownstreamSnapshot != 12 || p.ProSIWC.Status != "warming" || p.ProSIWC.AccountID != 9 {
		t.Fatalf("incorrect transfer: calls=%v groups=%v state=%+v cost=%v", sequence, groups, p.ProSIWC, p.CostDownstreamSnapshot)
	}
}

func TestSIWCTransferWarmupFreshMonitorAndOriginalDeadline(t *testing.T) {
	f := newProExecutionFixture(t)
	calls := 0
	stale := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "test"})
		case "/api/v1/admin/accounts":
			calls++
			write(map[string]any{"total": 1, "items": []any{map[string]any{"id": 42, "group_ids": []int64{7, 8}, "platform": "openai", "status": "active", "schedulable": true, "concurrency": 20, "credentials": map[string]any{"model_mapping": map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"}}}}})
		case "/api/v1/admin/ops/concurrency":
			at := time.Now()
			if stale {
				at = at.Add(-time.Hour)
			}
			write(map[string]any{"enabled": true, "timestamp": at, "account": map[string]any{"42": map[string]any{"account_id": 42, "current_in_use": 3, "waiting_in_queue": 0, "max_capacity": 20}}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	cfg := model.DefaultProSIWCSettings()
	cfg.Sub2.URL = remote.URL
	cfg.Sub2.Email = "admin@example.test"
	cfg.Sub2.Models = []string{"gpt-6.1-sol", "gpt-6.2-sol"}
	cfg.Sub2.GroupIDs = []int64{7}
	cfg.MonitorGroupIDs = []int64{7, 8}
	if _, e := f.s.store.SaveProSIWCSettings(cfg, "test"); e != nil {
		t.Fatal(e)
	}
	source, _, _, _ := f.s.store.ProSettings()
	source.Provider = "sub2"
	source.Sub2.URL = "https://source.example.test"
	source.Sub2.Email = "admin@example.test"
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if e := f.s.queueProSIWC(p, source, "test"); e != nil {
		t.Fatal(e)
	}
	if f.s.proSIWCBlock() == "" {
		t.Fatal("transfer must pause before any upstream calls")
	}
	started := time.Now().Add(-time.Minute)
	ready := time.Now().Add(10 * time.Second)
	p, e := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProSIWC.AccountID = 42
		p.ProSIWC.Stage = "warm"
		p.ProSIWC.Status = "warming"
		p.ProSIWC.ReadyAt = &ready
		p.ProPushStartedAt = &started
		enabled := true
		p.ProPushTimeoutEnabled = &enabled
		p.ProPushTimeoutMinutes = 60
	})
	if e != nil {
		t.Fatal(e)
	}
	originalDeadline := p.ProPushDeadline()
	if e = f.s.runProSIWC(context.Background(), f.email); e != nil || calls != 0 {
		t.Fatal("warmup must not prematurely activate", e, calls)
	}
	ready = time.Now().Add(-time.Second)
	f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProSIWC.ReadyAt = &ready })
	stale = true
	if e = f.s.runProSIWC(context.Background(), f.email); e == nil || f.s.proSIWCBlock() == "" {
		t.Fatal("stale monitor released pause")
	}
	stale = false
	if e = f.s.runProSIWC(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if p.ProSIWC.Status != "active" || f.s.proSIWCBlock() != "" || !p.ProPushStartedAt.Equal(started) || !p.ProPushDeadline().Equal(*originalDeadline) {
		t.Fatal("activation changed original timeout or stayed paused")
	}
}

func TestSIWCCostCarryIsMonotonicAndRejectsOldSource(t *testing.T) {
	f := newProExecutionFixture(t)
	cfg := model.DefaultProSettings()
	cfg.Provider = "sub2"
	cfg.Sub2.URL = "https://siwc.example.test"
	cfg.Sub2.Email = "admin@example.test"
	cfg.Sub2.AuthMode = "siwc"
	p, e := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.PushProvider = "sub2"
		p.Sub2AccountID = 9
		p.ProPushDownstream = &model.ProDownstreamRef{Provider: "sub2", URL: cfg.Sub2.URL, LoginEmail: cfg.Sub2.Email}
		p.ProSIWC = &model.ProSIWCState{AccountID: 9, Stage: "warm", Status: "warming", CarriedCost: 12, CarriedUserCost: 3}
		p.CostProvider = "sub2"
		p.CostConnectionIdentity = proCostConnectionIdentity(cfg.Sub2)
		p.CostDownstreamIdentity = "9"
		p.CostDownstreamSnapshot = 12
		p.UserCostDownstreamSnapshot = 3
	})
	if e != nil || p.CostDownstreamSnapshot != 12 {
		t.Fatal("transfer rebind cleared original consumption", e)
	}
	for _, amount := range []float64{4, 4, 2, 6} {
		user := amount / 2
		p, e = f.s.saveProCostSnapshot(p, cfg, sub2.AccountCosts{StandardCostUSD: amount, UserCostUSD: &user})
		if e != nil {
			t.Fatal(e)
		}
	}
	if p.CostDownstreamSnapshot != 18 || p.UserCostDownstreamSnapshot != 6 {
		t.Fatal("not original+new monotonic totals", p.CostDownstreamSnapshot, p.UserCostDownstreamSnapshot)
	}
	source := cfg
	source.Sub2.AuthMode = ""
	source.Sub2.URL = "https://old.example.test"
	p, e = f.s.saveProCostSnapshot(p, source, sub2.AccountCosts{StandardCostUSD: 999})
	if e != nil || p.CostDownstreamSnapshot != 18 {
		t.Fatal("late source response replaced destination total", e)
	}
}

func TestSIWCCombinedRestockCapacityDemandAndMissingLoads(t *testing.T) {
	f := newProExecutionFixture(t)
	broken := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "test"})
		case "/api/v1/admin/accounts":
			write(map[string]any{"total": 1, "items": []any{map[string]any{"id": 42, "group_ids": []int64{7, 8}, "platform": "openai", "status": "active", "schedulable": true, "concurrency": 20, "credentials": map[string]any{"model_mapping": map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"}}}}})
		case "/api/v1/admin/ops/concurrency":
			load := map[string]any{}
			if !broken {
				load["42"] = map[string]any{"account_id": 42, "current_in_use": 6, "waiting_in_queue": 1, "max_capacity": 20}
			}
			write(map[string]any{"enabled": true, "timestamp": time.Now(), "account": load})
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	cfg := model.DefaultProSIWCSettings()
	cfg.Sub2.URL = remote.URL
	cfg.Sub2.Email = "admin@example.test"
	cfg.Sub2.Models = []string{"gpt-6.1-sol", "gpt-6.2-sol"}
	cfg.Sub2.GroupIDs = []int64{7, 8}
	target, e := f.s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-siwc", Provider: "sub2", Sub2: cfg.Sub2, Secret: "test"})
	if e != nil {
		t.Fatal(e)
	}
	primary := model.DefaultProSettings()
	primary.Sub2.URL = "https://source.example.test"
	owner := model.MailAccountProfile{Sub2AccountID: 42, ProSIWC: &model.ProSIWCState{TargetID: target, AccountID: 42, Status: "active", MonitorGroupIDs: []int64{7, 8}}}
	snap := restock.Snapshot{At: time.Now(), Accounts: []restock.Account{{ID: 42, Capacity: 10, Used: 2, Ready: true}}}
	c := restock.DefaultConfig()
	c.FreshSeconds = 30
	if e = f.s.appendSIWCRestock(context.Background(), c, primary, []model.MailAccountProfile{owner, owner}, &snap); e != nil {
		t.Fatal(e)
	}
	if len(snap.Accounts) != 2 || snap.Accounts[1].ID >= 0 || snap.Accounts[1].RemoteID != 42 || snap.Accounts[1].Used != 6 || snap.Accounts[1].Capacity != 20 || !snap.Accounts[1].Ready {
		t.Fatalf("cross-server collision or duplicate groups: %+v", snap.Accounts)
	}
	broken = true
	if e = f.s.appendSIWCRestock(context.Background(), c, primary, []model.MailAccountProfile{owner}, &snap); e == nil {
		t.Fatal("missing remote loads accepted")
	}
}

func TestSIWCQuotaPoliciesAndTransferGate(t *testing.T) {
	f := newProExecutionFixture(t)
	cfg := model.DefaultProSIWCSettings()
	cfg.FiveHourRemaining = 10
	cfg.SevenDayRemaining = 20
	if _, e := f.s.store.SaveProSIWCSettings(cfg, ""); e != nil {
		t.Fatal(e)
	}
	settings := model.DefaultProSettings()
	settings.Sub2.AuthMode = "siwc"
	now := time.Now()
	p := model.MailAccountProfile{QuotaCheckedAt: &now, Quota5H: &model.FreeQuotaWindow{UsedPercent: 90}, Quota7D: &model.FreeQuotaWindow{UsedPercent: 2}}
	if !f.s.proQuotaExitReached(p, settings, 100) {
		t.Fatal("5h remaining policy ignored")
	}
	p.ProSIWC = &model.ProSIWCState{Status: "warming"}
	if f.s.proQuotaExitReached(p, settings, 100) {
		t.Fatal("quota exit admitted during transfer")
	}
	p.ProSIWC.Status = "active"
	p.Quota5H.UsedPercent = 1
	p.Quota7D.UsedPercent = 80
	if !f.s.proQuotaExitReached(p, settings, 100) {
		t.Fatal("7d remaining policy ignored")
	}
	now = now.Add(-time.Hour)
	if f.s.proQuotaExitReached(p, settings, 100) {
		t.Fatal("stale quota admitted")
	}
	if siwcRestockID("https://one.example", 42) == siwcRestockID("https://two.example", 42) {
		t.Fatal("ID collision")
	}
}

func TestSIWCCreateUsesDurableIdempotentRequest(t *testing.T) {
	f := newProExecutionFixture(t)
	requests := 0
	var key, body string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/login") {
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"test"}}`)
			return
		}
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/accounts") {
			t.Error(r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var input map[string]any
		json.NewDecoder(r.Body).Decode(&input)
		raw, _ := json.Marshal(input)
		requests++
		if requests == 1 {
			key = r.Header.Get("Idempotency-Key")
			body = string(raw)
			http.Error(w, "temporarily unavailable", 500)
			return
		}
		if key == "" || key != r.Header.Get("Idempotency-Key") || body != string(raw) {
			t.Error("retry changed idempotency or credentials")
		}
		fmt.Fprint(w, `{"code":0,"data":{"id":9,"name":"test"}}`)
	}))
	defer remote.Close()
	cfg := model.DefaultSub2Settings()
	cfg.URL = remote.URL
	cfg.Email = "admin@example.test"
	cfg.AuthMode = "siwc"
	in := sub2.CreateAccountInput{Name: "test", Credentials: map[string]any{"auth_mode": "siwc", "access_token": "first"}, GroupIDs: []int64{7}}
	_, e := f.s.createSub2WithProxy(context.Background(), cfg, "test", in, "test-scope", "test-key", nil)
	if e == nil {
		t.Fatal("expected first failure")
	}
	in.Credentials["access_token"] = "different"
	got, e := f.s.createSub2WithProxy(context.Background(), cfg, "test", in, "test-scope", "test-key", nil)
	if e != nil || got.ID != 9 {
		t.Fatal(got, e)
	}
	got, e = f.s.createSub2WithProxy(context.Background(), cfg, "test", in, "test-scope", "test-key", nil)
	if e != nil || got.ID != 9 || requests != 2 {
		t.Fatal("repeat created extra account", requests, e)
	}
}
