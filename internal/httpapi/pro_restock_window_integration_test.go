package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/nexovcc"
	"chatgpt-space-merge/internal/restock"
)

// All services, credentials, card numbers and orders here are local fixtures.
func restockWindowIntegration(t *testing.T, payment http.HandlerFunc) (*Server, *nexFixture, string, restock.Config) {
	t.Helper()
	s, st, email := gptPayFixture(t, payment)
	configureProAutoFixture(t, s)
	s.proAutoCancel()
	s.proAutoWG.Wait()
	nex, f := nexFixtureServer(t)
	s.nexovcc.transport = nex.nexovcc.transport
	cfg, credentials, _ := nex.store.NexovccSettings()
	if _, err := st.SaveNexovccSettings(cfg, credentials, false); err != nil {
		t.Fatal(err)
	}
	if err := st.NexovccCache("products", []nexovcc.Product{{ID: "267", Minimum: "1.00"}}); err != nil {
		t.Fatal(err)
	}
	p, _, _, _ := st.ProSettings()
	p.CardProvider = "ejh"
	p.EJHAutoOpen = false
	p.NexovccAutoOpen = true
	p.NexovccTemplateID = "template"
	p.NexovccDailyOpenLimit = 1
	p.NexovccDailyAmountLimit = "10.00"
	if _, err := st.SaveProSettings(p, "", ""); err != nil {
		t.Fatal(err)
	}
	c := providerWindow(t, s, "nexovcc")
	c.CapacityPercent, c.Confirmations, c.FailureLimit = 100, 1, 1
	c.TimeEnabled, c.MinBatch, c.MaxBatch, c.MaxInflight = false, 1, 1, 2
	c.UnitCost = 25
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	pay, _, _ := st.GPTPaySettings()
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		now := time.Now()
		return restock.Snapshot{At: now, CollectionStartedAt: now, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: 9}}}, nil
	}
	return s, f, email, c
}

func windowTick(t *testing.T, s *Server) {
	t.Helper()
	if err := s.tickProRestock(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func windowRecharge(s *Server, email string, rpc func(string, map[string]any) (any, error)) error {
	if _, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = "198.51.100.8" }); err != nil {
		return err
	}
	_, err := rpc("pro_recharge", map[string]any{"exit_ip": "198.51.100.8", "session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}})
	return err
}

func TestProRestockWindowFullFlowAndPaidRecovery(t *testing.T) {
	for _, recoverPaid := range []bool{false, true} {
		t.Run(fmt.Sprint(recoverPaid), func(t *testing.T) {
			var purchases, pushes atomic.Int32
			s, f, email, c := restockWindowIntegration(t, func(w http.ResponseWriter, r *http.Request) {
				purchases.Add(1)
				fmt.Fprint(w, `{"code":0,"data":{"id":"window-paid","status":"success"}}`)
			})
			var failAuth atomic.Bool
			failAuth.Store(recoverPaid)
			st := s.store
			if recoverPaid {
				settings, _, _, _ := st.ProSettings()
				settings.RetryCount, settings.RetryIntervalSeconds = 1, 1
				if _, err := st.SaveProSettings(settings, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			s.notifications.queue = make(chan notificationEvent, 30)
			s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
				if current != email {
					t.Error("switched account")
				}
				if isProPaidRecovery(ctx) {
					p, _, _ := st.MailAccountCredential(current)
					if p.ProAuto.Stage != "oauth" || p.ProAuto.Steps["login"] != "completed" || p.ProAuto.Steps["recharge"] != "completed" {
						t.Error("restock recovery reset the completed login/purchase stages")
					}
				}
				if err := windowRecharge(s, current, rpc); err != nil {
					return err
				}
				if failAuth.Swap(false) {
					// A blocked purchase window must still allow the configured
					// third-step retry without another card or payment.
					c.NoRestockWindows[0].AllowedProvider = ""
					if _, err := st.SaveProRestockConfig(c); err != nil {
						return err
					}
					return errors.New("fixture authorization interrupted after payment")
				}
				_, err := rpc("pro_tokens", map[string]any{"exit_ip": "198.51.100.8", "tokens": map[string]any{"access_token": proAutoToken(current, "new"), "refresh_token": "fixture-new-rt"}})
				return err
			}, Push: func(ctx context.Context, current string) (model.MailAccountProfile, error) {
				pushes.Add(1)
				return st.UpdateProAccount(current, func(p *model.MailAccountProfile) {
					p.PushStatus = "completed"
					p.PushProvider = "sub2"
					p.Sub2AccountID = 88
				})
			}, Quota: func(context.Context, string, bool) (model.MailAccountProfile, error) {
				return model.MailAccountProfile{}, errors.New("fixture quota unavailable")
			}, Merge: func(context.Context, string) (model.MailAccountProfile, error) {
				t.Error("unexpected merge of Nexovcc account")
				return model.MailAccountProfile{}, nil
			}}
			windowTick(t, s)
			profile := waitProAutoFinished(t, s, email)
			if recoverPaid {
				if profile.ProAuto.OAuthRecovery == nil || profile.ProAuto.OAuthRecovery.Attempts != 1 || profile.ProAuto.RestockRecoveries != 0 {
					t.Fatal("did not use the configured third-step budget", profile.ProAuto)
				}
			}
			if profile.ProAuto.Status != "waiting_quota" || purchases.Load() != 1 || pushes.Load() != 1 || f.opens.Load() != 1 {
				t.Fatal("full chain failed or paid twice", profile.ProAuto, purchases.Load(), pushes.Load(), f.opens.Load())
			}
			windowTick(t, s)
			tasks, _ := st.ProRestockTasks()
			if len(tasks) != 1 || tasks[0].Status != "verifying" || tasks[0].CardProvider != "nexovcc" {
				t.Fatal("push was treated as ready before monitoring", tasks)
			}
			collect := s.proRestock.monitor
			s.proRestock.monitor = func(ctx context.Context, c restock.Config, p model.ProSettings, password string) (restock.Snapshot, error) {
				snap, err := collect(ctx, c, p, password)
				snap.Accounts = append(snap.Accounts, restock.Account{ID: 88, Ready: true, Capacity: 10})
				return snap, err
			}
			windowTick(t, s)
			tasks, _ = st.ProRestockTasks()
			if tasks[0].Status != "ready" || tasks[0].ReadyAt == nil {
				t.Fatal("new monitor did not confirm ready", tasks)
			}
			for range 3 {
				windowTick(t, s)
			}
			if purchases.Load() != 1 || f.opens.Load() != 1 {
				t.Fatal("duplicate task after push")
			}
			seenNotice := false
			for len(s.notifications.queue) > 0 {
				n := <-s.notifications.queue
				if n.Kind == "restock" {
					seenNotice = true
					if !strings.Contains(n.Text, "补货卡台：环球") {
						t.Fatal("wrong issuer in notification", n.Text)
					}
				}
			}
			if !seenNotice {
				t.Fatal("missing restock notification")
			}
			p, _, _, _ := st.ProSettings()
			if p.CardProvider != "ejh" {
				t.Fatal("mutated default card provider")
			}
		})
	}
}

func TestProRestockWindowUnknownOpenAndPaymentHoldBatch(t *testing.T) {
	for _, failure := range []string{"open", "payment"} {
		t.Run(failure, func(t *testing.T) {
			var purchases atomic.Int32
			s, f, email, _ := restockWindowIntegration(t, func(w http.ResponseWriter, r *http.Request) {
				purchases.Add(1)
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":50301}`)
			})
			f.unknown = failure == "open"
			s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
				return windowRecharge(s, current, rpc)
			}}
			windowTick(t, s)
			waitProAutoFinished(t, s, email)
			for range 3 {
				windowTick(t, s)
			}
			tasks, _ := s.store.ProRestockTasks()
			limits, _ := s.store.ProRestockLimits(time.Now())
			if len(tasks) != 1 || !tasks[0].Active() || tasks[0].CardProvider != "nexovcc" || limits.DayCost != 25 || f.opens.Load() != 1 {
				t.Fatal("unknown result released/repeated batch", tasks, limits, f.opens.Load())
			}
			if failure == "open" && purchases.Load() != 0 || failure == "payment" && (purchases.Load() != 1 || tasks[0].Status != "unknown") {
				t.Fatal("unsafe payment count", purchases.Load(), tasks)
			}
		})
	}
}

func TestProRestockWindowNoNewCardsStillUsesOldestEligible(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			s, f, email, c := restockWindowIntegration(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
			p, _, _, _ := s.store.ProSettings()
			p.NexovccAutoOpen = false
			if _, err := s.store.SaveProSettings(p, "", ""); err != nil {
				t.Fatal(err)
			}
			want := ""
			if existing {
				for i, serial := range []string{"newer", "oldest"} {
					card, err := s.store.SaveNexovccCard(nexovcc.Card{Serial: serial, Status: "1", Balance: "10.00", CreatedAt: time.Now().Add(-time.Duration(i+1) * time.Hour)}, &nexovcc.PrivateCard{Number: []string{"5555555555554444", "4000000000000002"}[i], CVV: "123", Month: 12, Year: 2099})
					if err != nil {
						t.Fatal(err)
					}
					// Eligible issuer cards carry their original opening-template evidence.
					op := nexovcc.Operation{ID: "fixture-open-" + serial, Action: "open", TemplateID: "template", PlanCode: "pro20", CardID: card.ID, Status: "submitted", CreatedAt: time.Now(), UpdatedAt: time.Now()}
					if err = s.store.BeginNexovccOperation(op); err != nil {
						t.Fatal(err)
					}
					op.Status = "succeeded"
					if err = s.store.SaveNexovccOperation(op); err != nil {
						t.Fatal(err)
					}
					if serial == "oldest" {
						want = card.ID
					}
				}
			}
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				return errors.New("fixture stop before payment")
			}}
			windowTick(t, s)
			profile := waitProAutoFinished(t, s, email)
			if f.opens.Load() != 0 {
				t.Fatal("auto-open disabled but purchased card")
			}
			if existing {
				if profile.ProAuto.CardID != want {
					t.Fatal("did not choose oldest Nexovcc", profile.ProAuto.CardID, want)
				}
				return
			}
			for range 3 {
				windowTick(t, s)
			}
			tasks, _ := s.store.ProRestockTasks()
			if len(tasks) != 1 || tasks[0].Status != "reserved" || !strings.Contains(tasks[0].Message, "环球无可用卡") || profile.ProAuto.ID != "" {
				t.Fatal("queued work used default card or duplicated", tasks)
			}
			// Turning the interval back into a complete prohibition cancels only
			// this unstarted reservation and returns its account/budget.
			c.NoRestockWindows[0].AllowedProvider = ""
			if _, err := s.store.SaveProRestockConfig(c); err != nil {
				t.Fatal(err)
			}
			windowTick(t, s)
			tasks, _ = s.store.ProRestockTasks()
			limits, _ := s.store.ProRestockLimits(time.Now())
			profile, _, _ = s.store.MailAccountCredential(email)
			if tasks[0].Status != "cancelled" || limits.DayCost != 0 || profile.ProRestockTaskID != "" {
				t.Fatal("unstarted reservation not released", tasks, limits)
			}
		})
	}
}

func TestProRestockWindowDoesNotBypassExecutionGates(t *testing.T) {
	for _, mode := range []string{"disabled", "observe", "no-demand", "invalid-monitor", "stale-monitor", "confirmation", "cancelled-context"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, c := restockWindowIntegration(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
			switch mode {
			case "disabled":
				c.Enabled = false
			case "observe":
				c.Mode = "observe"
			case "confirmation":
				c.Confirmations = 2
			}
			if _, err := s.store.SaveProRestockConfig(c); err != nil {
				t.Fatal(err)
			}
			collect := s.proRestock.monitor
			s.proRestock.monitor = func(ctx context.Context, c restock.Config, p model.ProSettings, password string) (restock.Snapshot, error) {
				snap, err := collect(ctx, c, p, password)
				switch mode {
				case "no-demand":
					snap.Accounts[0].Used = 0
				case "invalid-monitor":
					snap.Valid = false
					snap.Error = "fixture down"
				case "stale-monitor":
					snap.At = time.Now().Add(-time.Hour)
				}
				return snap, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled-context" {
				cancel()
			}
			if err := s.tickProRestock(ctx, true); err != nil {
				t.Fatal(err)
			}
			tasks, _ := s.store.ProRestockTasks()
			if len(tasks) != 0 || f.opens.Load() != 0 {
				raw, _ := json.Marshal(tasks)
				t.Fatal("execution guard bypassed", string(raw))
			}
		})
	}
}

func TestProRestockWindowLoginRetryAndBreaker(t *testing.T) {
	s, f, email, c := restockWindowIntegration(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
	st := s.store
	p, _, _, _ := st.ProSettings()
	p.RetryCount, p.RetryIntervalSeconds = 2, 1
	if _, err := st.SaveProSettings(p, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: "spare@example.test", ManagementScope: "pro"}, model.MailAccountCredentials{Email: "spare@example.test"}); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	started := make(chan struct{}, 1)
	var originalCard string
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, current string, _ func(string, map[string]any) (any, error)) error {
		profile, _, _ := st.MailAccountCredential(current)
		if current != email {
			t.Error("retry switched account")
		}
		if n := attempts.Add(1); n == 1 {
			originalCard = profile.ProAuto.CardID
			started <- struct{}{}
		} else if profile.ProAuto.CardID != originalCard {
			t.Error("retry switched card")
		}
		return errors.New("fixture login failed")
	}}
	windowTick(t, s)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("workflow did not start")
	}
	for range 2 {
		windowTick(t, s)
		if s.proRestock.decision.Started != 0 {
			t.Fatal("retry spawned a new batch")
		}
	}
	result := waitProAutoFinished(t, s, email)
	for range 3 {
		windowTick(t, s)
	}
	tasks, _ := st.ProRestockTasks()
	limits, _ := st.ProRestockLimits(time.Now())
	if attempts.Load() != 3 || result.ProAuto.RetryAttempts != 2 || len(tasks) != 1 || tasks[0].Status != "failed" || limits.Failures != 1 || !strings.Contains(s.proRestock.decision.Reason, "熔断") || f.opens.Load() != 1 {
		t.Fatal("retry or breaker regression", attempts.Load(), tasks, limits)
	}
	// Saving a schedule change must never reset the failure latch.
	c.NoRestockEnabled = false
	if _, err := st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	windowTick(t, s)
	limits, _ = st.ProRestockLimits(time.Now())
	if limits.Failures != 1 || s.proRestock.decision.Started != 0 {
		t.Fatal("time-window change reset breaker")
	}
}
