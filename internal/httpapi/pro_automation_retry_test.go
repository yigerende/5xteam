package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestProAutoSameAccountRetries(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		retries, logins, orders int
		push                    bool
	}{
		{"login_then_success", 2, 2, 1, true},
		{"payment_then_success", 2, 1, 1, false},
		{"payment_risk", 2, 1, 1, false},
		{"exhausted", 2, 3, 0, false},
		{"disabled", 0, 1, 0, false},
		{"unknown_payment", 2, 1, 1, false},
		{"paid_oauth_failure", 2, 3, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logins, purchases, pushes atomic.Int32
			var keyMu sync.Mutex
			keys := map[string]bool{}
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				n := purchases.Add(1)
				key := r.Header.Get("Idempotency-Key")
				keyMu.Lock()
				if key == "" || keys[key] {
					t.Error("payment retry reused a failed order key")
				}
				keys[key] = true
				keyMu.Unlock()
				if tc.name == "unknown_payment" {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"code":50301}`)
					return
				}
				if tc.name == "payment_risk" {
					fmt.Fprint(w, `{"code":0,"data":{"id":"risk-rejected","status":"failed","failureReason":"OpenAI 支付风控拦截，请稍后重试或更换卡。"}}`)
					return
				}
				if tc.name == "payment_then_success" && n == 1 {
					fmt.Fprint(w, `{"code":0,"data":{"id":"unpaid","status":"failed","failureReason":"payment rejected"}}`)
					return
				}
				fmt.Fprintf(w, `{"code":0,"data":{"id":"paid-%d","status":"success"}}`, n)
			})
			s.proAutoCancel()
			s.proAutoWG.Wait()
			configureProAutoFixture(t, s)
			settings, password, _, _ := st.ProSettings()
			settings.RetryCount, settings.RetryIntervalSeconds = tc.retries, 1
			if _, err := st.SaveProSettings(settings, password, ""); err != nil {
				t.Fatal(err)
			}
			var firstFlow string
			var times []time.Time
			s.proAutoHooks = &proAutomationHooks{
				Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
					n := logins.Add(1)
					times = append(times, time.Now())
					p, _, _ := st.MailAccountCredential(current)
					if n == 1 {
						firstFlow = p.ProAuto.ID
					}
					wantRetries := int(n) - 1
					if tc.name == "paid_oauth_failure" {
						wantRetries = 0
					}
					if current != email || p.ProAuto.ID != firstFlow || p.ProAuto.CardID != "test-card" || p.ProAuto.RetryAttempts != wantRetries {
						t.Error("retry changed account, flow, card or lost its attempt count")
					}
					if tc.name == "exhausted" || tc.name == "disabled" || tc.name == "login_then_success" && n == 1 {
						return errors.New("fixture login failed")
					}
					if _, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(current, fmt.Sprint(n)), "user": map[string]any{"email": current}, "account": map[string]any{"id": "personal-id"}}}); err != nil {
						return err
					}
					if tc.name == "paid_oauth_failure" {
						return errors.New("fixture OAuth failed after payment")
					}
					_, err := rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(current, "new"), "refresh_token": "new-rt"}})
					return err
				},
				Push: func(_ context.Context, current string) (model.MailAccountProfile, error) {
					pushes.Add(1)
					return st.UpdateProAccount(current, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
				},
				Quota: func(_ context.Context, current string, _ bool) (model.MailAccountProfile, error) {
					return st.UpdateProAccount(current, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 10} })
				},
			}
			rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": "tokenseek", "card_id": "test-card", "plan_code": "pro20"})
			if rec.Code != 202 {
				t.Fatal(rec.Body.String())
			}
			p := waitProAutoFinished(t, s, email)
			if logins.Load() != int32(tc.logins) || purchases.Load() != int32(tc.orders) || (pushes.Load() == 1) != tc.push {
				t.Fatalf("attempts=%d orders=%d pushes=%d state=%+v", logins.Load(), purchases.Load(), pushes.Load(), p.ProAuto)
			}
			for i := 1; i < len(times); i++ {
				if times[i].Sub(times[i-1]) < time.Second {
					t.Fatal("configured retry interval was skipped")
				}
			}
			if p.ProAuto.RetryAt != nil || tc.push && p.ProAuto.Status != "waiting_quota" || !tc.push && p.ProAuto.Status != "failed" {
				t.Fatal("unexpected final progress", p.ProAuto)
			}
			if tc.name == "exhausted" && !strings.Contains(p.ProAuto.Error, "重试 2 次后仍失败") {
				t.Fatal("retry exhaustion was hidden", p.ProAuto.Error)
			}
			if tc.name == "payment_risk" {
				if !p.ProAuto.PaymentReviewRequired || p.IsProDead() || p.ProAuto.RetryAttempts != 0 || !strings.Contains(p.ProAuto.Error, "待核实") {
					t.Fatal("risk rejection did not retain account for review", p.ProAuto)
				}
				cfg, _, _, _ := st.ProSettings()
				tasks, _, err := s.restockTasks(restock.Snapshot{Binding: restockBinding(cfg), GroupID: 1}, cfg, restock.Config{GroupID: 1, LeadMinutes: 5, LeadSamples: 2})
				if err != nil || len(tasks) != 1 || !tasks[0].AutoIsolated || tasks[0].Active() || tasks[0].Predictable() {
					t.Fatal("review still blocks restock or was counted as capacity", tasks, err)
				}
				run := model.ProScheduleRun{ID: "risk-run", Status: "running", StartedAt: time.Now(), Tasks: []model.ProScheduleTask{{Email: email, Status: "running"}}}
				if !s.reconcileProSchedule(&run) || run.Tasks[0].Status != "failed" {
					t.Fatal("disabled replacement should finish the failed chain", run)
				}
			}
			orders, total, _ := st.GPTPayOrders(10, 0, email)
			if total != tc.orders || tc.name == "payment_then_success" && orders[0].Status != "failed" {
				t.Fatal("original failed order was lost", orders)
			}
		})
	}
}

func TestProRestockRetryRecoversCurrentUnlinkedOrder(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("recovery must not submit payment") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ReserveProRestock(restock.Decision{ID: "original", At: time.Now(), Version: c.Version, Suggested: 1}, "", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatal("missing task", err)
	}
	state := model.ProAutoState{ID: "flow", RetryAttempts: 1, Status: "interrupted", Stage: "recharge", CardID: "test-card", Steps: map[string]string{"login": "completed"}}
	if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto = state }); err != nil {
		t.Fatal(err)
	}
	profile, credentials, _ := st.MailAccountCredential(email)
	session, _ := gptPaySession(profile, credentials)
	cfg, key, _ := st.GPTPaySettings()
	_, cardSecret, _ := st.GPTPayCard("test-card")
	for _, order := range []gptpay.Order{
		{ID: "pro-auto-flow", Email: email, CardID: "test-card", Status: "failed", CreatedAt: time.Now().Add(-time.Minute)},
		{ID: state.PaymentOrderID(), Email: email, CardID: "test-card", Status: "submission_unknown", CreatedAt: time.Now()},
	} {
		if err := st.CreateGPTPayOrder(order, gptpay.NewSnapshot(cfg, key, cardSecret, session)); err != nil {
			t.Fatal(err)
		}
	}
	tasks[0].Status, tasks[0].OrderID = "running", "pro-auto-flow"
	if err := st.SaveProRestockTask(tasks[0]); err != nil {
		t.Fatal(err)
	}
	settings, _, _, _ := st.ProSettings()
	tracked, _, err := s.restockTasks(restock.Snapshot{}, settings, c)
	if err != nil || len(tracked) != 1 || !tracked[0].AutoIsolated || tracked[0].Active() || tracked[0].OrderID != state.PaymentOrderID() {
		t.Fatal("old failed order hid the retry's uncertain payment", tracked, err)
	}
	saved, _, _ := st.MailAccountCredential(email)
	if saved.ProAuto.OrderID != state.PaymentOrderID() || saved.ProAuto.RetryAttempts != 1 {
		t.Fatal("current payment link or retry count lost", saved.ProAuto)
	}
}

func TestProRestockRetryKeepsBatchAndBreaker(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop_%v", stop), func(t *testing.T) {
			var attempts atomic.Int32
			s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
			s.proAutoCancel()
			s.proAutoWG.Wait()
			configureProAutoFixture(t, s)
			p, password, _, _ := st.ProSettings()
			p.RetryCount, p.RetryIntervalSeconds = 2, 1
			if _, err := st.SaveProSettings(p, password, ""); err != nil {
				t.Fatal(err)
			}
			st.SaveMailAccount(model.MailAccountProfile{Email: "spare@example.com", ManagementScope: "pro"}, model.MailAccountCredentials{Email: "spare@example.com"})
			c := restock.DefaultConfig()
			c.Enabled, c.Mode, c.GroupID = true, "auto", 1
			c.CapacityPercent, c.Confirmations, c.FailureLimit = 100, 1, 1
			c.TimeEnabled, c.MinBatch, c.MaxBatch, c.MaxInflight = false, 1, 1, 2
			c.CooldownSeconds, c.EmergencyCooldownSeconds = 1, 1
			if _, err := st.SaveProRestockConfig(c); err != nil {
				t.Fatal(err)
			}
			pay, _, _ := st.GPTPaySettings()
			s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
				now := time.Now()
				return restock.Snapshot{At: now, CollectionStartedAt: now, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: 9}}}, nil
			}
			s.proAutoHooks = &proAutomationHooks{Session: func(_ context.Context, current string, _ func(string, map[string]any) (any, error)) error {
				if current != email {
					t.Error("picked a new account while retrying")
				}
				attempts.Add(1)
				return errors.New("fixture login failed")
			}}
			tick := func() {
				t.Helper()
				if err := s.tickProRestock(context.Background(), true); err != nil {
					t.Fatal(err)
				}
			}
			tick()
			until := time.Now().Add(2 * time.Second)
			for {
				current, _, _ := st.MailAccountCredential(email)
				if current.ProAuto.RetryAt != nil {
					break
				}
				if time.Now().After(until) {
					t.Fatal("no retry wait")
				}
				time.Sleep(5 * time.Millisecond)
			}
			for range 3 {
				tick()
				tasks, _ := st.ProRestockTasks()
				limits, _ := st.ProRestockLimits(time.Now())
				if len(tasks) != 1 || !tasks[0].Active() || limits.Failures != 0 || s.proRestock.decision.Started != 0 {
					t.Fatal("retry released batch or counted a terminal failure", tasks, limits)
				}
			}
			if stop {
				if rec := payCall(t, s.stopProAutomation, "email", email, map[string]any{}); rec.Code != 202 {
					t.Fatal(rec.Body.String())
				}
			}
			result := waitProAutoFinished(t, s, email)
			if stop {
				if attempts.Load() != 1 || !result.ProAuto.StopRequested || result.ProAuto.Status != "stopped" {
					t.Fatal("stop did not cancel retry", attempts.Load(), result.ProAuto)
				}
				return
			}
			for range 4 {
				tick()
			}
			tasks, _ := st.ProRestockTasks()
			limits, _ := st.ProRestockLimits(time.Now())
			if attempts.Load() != 3 || len(tasks) != 1 || tasks[0].Status != "failed" || limits.Failures != 1 || !strings.Contains(s.proRestock.decision.Reason, "熔断") {
				t.Fatal("retry exhaustion bypassed breaker", attempts.Load(), tasks, limits, s.proRestock.decision.Reason)
			}
		})
	}
}
