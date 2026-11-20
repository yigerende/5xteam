package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestProXUPaySessionRecovery(t *testing.T) {
	const expired = "当前 Session 已失效或无法刷新。请重新登录该账号，获取新的完整 Session 后重新提交。本单未提交付款，冻结服务费已释放。"
	for _, tc := range []struct {
		name                                           string
		ordinaryLimit, ordinaryUsed, logins, purchases int
		final                                          string
	}{
		{"zero_retries", 0, 0, 2, 2, "waiting_quota"},
		{"already_exhausted", 1, 1, 3, 2, "waiting_quota"},
		{"expired_twice", 2, 0, 2, 2, "failed"},
		{"relogin_failed", 0, 0, 2, 1, "failed"},
		{"relogin_uses_ordinary_policy", 1, 1, 3, 2, "waiting_quota"},
		{"restock_success", 0, 0, 2, 2, "waiting_quota"},
		{"restock_expired_twice", 0, 0, 2, 2, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const email = "pro-test@example.com"
			var purchases, logins atomic.Int32
			var keys, cookies []string
			s, st, fixtureEmail := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					var body struct {
						Session gptpay.Session `json:"session"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					var raw map[string]any
					_ = json.Unmarshal(body.Session.Raw, &raw)
					keys = append(keys, r.Header.Get("Idempotency-Key"))
					cookies = append(cookies, fmt.Sprint(raw["sessionToken"]))
					fmt.Fprintf(w, `{"id":"test-order-%d","status":"queued"}`, purchases.Add(1))
					return
				}
				id := path.Base(r.URL.Path)
				if id == "test-order-1" || strings.HasSuffix(tc.name, "expired_twice") {
					fmt.Fprintf(w, `{"order":{"id":%q,"email":%q,"product_id":"pro20x","workflow":"subscription","status":"failed","fee":100},"payment":{"state":"failed","fee_state":"released","message":%q}}`, id, email, expired)
				} else {
					fmt.Fprintf(w, `{"order":{"id":%q,"email":%q,"product_id":"pro20x","workflow":"subscription","status":"succeeded","activation":"active","renewal":"disabled"},"payment":{"state":"succeeded","fee_state":"charged"}}`, id, email)
				}
			})
			if fixtureEmail != email {
				t.Fatal("fixture account changed")
			}
			s.proAutoCancel()
			s.proAutoWG.Wait()
			configureProAutoFixture(t, s)
			cfg, _, _ := st.GPTPaySettings()
			cfg.Provider, cfg.PlanCode, cfg.Country, cfg.TermsAccepted = gptpay.XUPay, "pro20", "PH", true
			if _, err := st.SaveGPTPaySettings(cfg, "xupay-fixture-key"); err != nil {
				t.Fatal(err)
			}
			settings, password, _, _ := st.ProSettings()
			settings.RetryCount, settings.RetryIntervalSeconds = tc.ordinaryLimit, 1
			if _, err := st.SaveProSettings(settings, password, ""); err != nil {
				t.Fatal(err)
			}
			var flowID string
			var failedLoginAt time.Time
			isRestock := strings.HasPrefix(tc.name, "restock_")
			pauses := make(chan string)
			proceed := make(chan struct{})
			finished := make(chan struct{})
			defer close(finished)
			pause := func(stage string) {
				select {
				case pauses <- stage:
				case <-finished:
					return
				}
				select {
				case <-proceed:
				case <-finished:
				}
			}
			s.proAutoHooks = &proAutomationHooks{PollDelay: time.Millisecond, Session: func(_ context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
				n := int(logins.Add(1))
				p, _, err := st.MailAccountCredential(current)
				if err != nil {
					return err
				}
				if n == 1 {
					flowID = p.ProAuto.ID
				}
				if current != email || p.ProAuto.ID != flowID || p.ProAuto.CardID != "test-card" {
					return errors.New("changed account, flow or card")
				}
				if isRestock && n == 2 {
					pause("login")
				}
				if tc.name == "already_exhausted" && n == 1 || (tc.name == "relogin_failed" || tc.name == "relogin_uses_ordinary_policy") && n == 2 {
					failedLoginAt = time.Now()
					return errors.New("fixture ordinary login failure")
				}
				if !failedLoginAt.IsZero() {
					if time.Since(failedLoginAt) < time.Second {
						t.Error("ordinary retry interval skipped")
					}
					failedLoginAt = time.Time{}
				}
				token := proAutoToken(current, strconv.Itoa(n))
				_, err = rpc("pro_recharge", map[string]any{"session": map[string]any{"accessToken": token, "access_token": token, "session_token": fmt.Sprintf("fixture-session-%d", n), "user": map[string]any{"email": current}, "account": map[string]any{"id": "personal-id"}}})
				if err != nil {
					return err
				}
				_, err = rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(current, "after"), "refresh_token": "fixture-rt"}})
				return err
			}, Push: func(_ context.Context, current string) (model.MailAccountProfile, error) {
				return st.UpdateProAccount(current, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
			}, Quota: func(_ context.Context, current string, _ bool) (model.MailAccountProfile, error) {
				return st.UpdateProAccount(current, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 10} })
			}}
			if isRestock {
				s.proAutoHooks.PaymentReserved = func() {
					if logins.Load() == 2 {
						pause("recharge")
					}
				}
				runXUPayRestockDuringRecovery(t, s, cfg, settings, pauses, proceed)
			} else {
				rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": gptpay.XUPay, "card_id": "test-card", "plan_code": "pro20"})
				if rec.Code != 202 {
					t.Fatal(rec.Body.String())
				}
			}
			p := waitProAutoFinished(t, s, email)
			a := p.ProAuto
			if int(logins.Load()) != tc.logins || int(purchases.Load()) != tc.purchases || a.Status != tc.final || a.RetryAttempts != tc.ordinaryUsed || a.ProxyRetryAttempts != 0 {
				t.Fatalf("logins=%d submissions=%d state=%+v", logins.Load(), purchases.Load(), a)
			}
			if len(keys) == 0 || keys[0] == "" || keys[0] != a.SessionRecoveryOrderID {
				t.Fatal("original key not retained", keys, a)
			}
			old, _, err := st.GPTPayOrder(a.SessionRecoveryOrderID)
			if err != nil || old.Remote.ID != "test-order-1" || !old.CanReloginXUPaySession() {
				t.Fatal("lost original order", err)
			}
			if len(keys) == 2 && (keys[0] == keys[1] || keys[1] != a.OrderID || cookies[0] == cookies[1] || !strings.HasPrefix(cookies[1], "fixture-session-")) {
				t.Fatal("new session and new key were not submitted together", keys, cookies)
			}
			if isRestock {
				if err := s.tickProRestock(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				tasks, err := st.ProRestockTasks()
				if err != nil || len(tasks) != 1 || tasks[0].ID != p.ProRestockTaskID || tasks[0].OrderID != a.OrderID {
					t.Fatal("restock did not follow new order", tasks, err)
				}
				limits, err := st.ProRestockLimits(time.Now())
				wantFailures := 0
				if tc.final == "failed" {
					wantFailures = 1
				}
				if err != nil || limits.Failures != wantFailures {
					t.Fatal("incorrect terminal failure count", limits, err)
				}
			}
			if tc.name == "expired_twice" {
				// Simulate a crash immediately after the second failed detail was saved.
				if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.ProAuto.Status = "running"
					p.ProAuto.Steps["recharge"] = "running"
				}); err != nil {
					t.Fatal(err)
				}
				if err := st.RecoverProAutomations(); err != nil {
					t.Fatal(err)
				}
				rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"retry_current": true})
				again, _, err := st.MailAccountCredential(email)
				if err != nil || rec.Code != 409 || again.ProAuto.Status != "failed" || again.ProAuto.OrderID != a.OrderID || !strings.Contains(rec.Body.String(), "再次失效") || purchases.Load() != 2 {
					t.Fatal("interrupted second failure was restarted", rec.Body.String(), again.ProAuto, err)
				}
			}
			if tc.name == "relogin_failed" {
				// Reopen the interruption gap after the claim, before a new payment was created.
				if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "running"; p.ProAuto.Steps["login"] = "running" }); err != nil {
					t.Fatal(err)
				}
				if err := st.RecoverProAutomations(); err != nil {
					t.Fatal(err)
				}
				failedLoginAt = time.Time{}
				rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"retry_current": true})
				if rec.Code != 202 {
					t.Fatal(rec.Body.String())
				}
				continued := waitProAutoFinished(t, s, email).ProAuto
				if continued.Status != "waiting_quota" || continued.ID != a.ID || continued.SessionRecoveryOrderID != a.SessionRecoveryOrderID || continued.OrderID != a.PaymentOrderID() || purchases.Load() != 2 {
					t.Fatal("lost claim on resume", continued)
				}
			}
		})
	}
}

func runXUPayRestockDuringRecovery(t *testing.T, s *Server, pay gptpay.Settings, p model.ProSettings, pauses <-chan string, proceed chan<- struct{}) {
	t.Helper()
	if _, err := s.store.SaveMailAccount(model.MailAccountProfile{Email: "spare@example.com", ManagementScope: "pro"}, model.MailAccountCredentials{Email: "spare@example.com"}); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c.CapacityPercent, c.Confirmations, c.FailureLimit = 100, 1, 1
	c.TimeEnabled, c.MinBatch, c.MaxBatch, c.MaxInflight = false, 1, 1, 2
	c.CooldownSeconds, c.EmergencyCooldownSeconds = 1, 1
	if _, err := s.store.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		now := time.Now()
		return restock.Snapshot{At: now, CollectionStartedAt: now, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: 9}}}, nil
	}
	tick := func() {
		t.Helper()
		if err := s.tickProRestock(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	var originalTask string
	for _, stage := range []string{"login", "recharge"} {
		select {
		case got := <-pauses:
			if got != stage {
				t.Fatal("unexpected stage", got)
			}
		case <-time.After(15 * time.Second):
			tasks, _ := s.store.ProRestockTasks()
			t.Fatal("restock did not reach recovery stage", stage, tasks, s.proRestock.decision.Reason)
		}
		for range 2 {
			tick()
			tasks, err := s.store.ProRestockTasks()
			if err != nil || len(tasks) != 1 || !tasks[0].Active() || tasks[0].Stage != stage || s.proRestock.decision.Started != 0 {
				t.Fatal("restock released or replaced the active task", tasks, err)
			}
			if originalTask == "" {
				originalTask = tasks[0].ID
			}
			limits, err := s.store.ProRestockLimits(time.Now())
			if err != nil || limits.Failures != 0 || limits.DayCount != 1 || tasks[0].ID != originalTask {
				t.Fatal("restock counted a new intent or failure", tasks, limits, err)
			}
		}
		proceed <- struct{}{}
	}
}
