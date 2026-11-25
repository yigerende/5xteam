package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProOAuthConfiguredRetryNoSecondPayment(t *testing.T) {
	for _, source := range []string{"local", "sub2api", "codex"} {
		for _, scenario := range []string{"same_client", "expired", "exhausted", "child_exit"} {
			t.Run(source+"/"+scenario, func(t *testing.T) {
				var payments, logins, authorizations, pushes atomic.Int32
				s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
					payments.Add(1)
					fmt.Fprint(w, `{"code":0,"data":{"id":"paid","status":"success"}}`)
				})
				s.proAutoCancel()
				s.proAutoWG.Wait()
				configureProAutoFixture(t, s)
				cfg, password, _, _ := st.ProSettings()
				cfg.RetryCount = 2
				cfg.RetryIntervalSeconds = 1
				if source != "codex" {
					cfg.Sub2.AuthMode = "siwc"
					cfg.Sub2.SIWCAuthorizationSource = source
					cfg.Sub2.SIWCConsentDeniedFinal = true
				}
				if _, err := st.SaveProSettings(cfg, password, ""); err != nil {
					t.Fatal(err)
				}
				var clock time.Time
				var orderID, flowID string
				var times []time.Time
				s.proAutoHooks = &proAutomationHooks{
					Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
						n := logins.Add(1)
						if n > 1 && !isProPaidRecovery(ctx) {
							t.Error("new client can pay again")
						}
						if _, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(current, "login"), "user": map[string]any{"email": current}, "account": map[string]any{"id": "personal-id"}}}); err != nil {
							return err
						}
						p, _, _ := st.MailAccountCredential(current)
						if n == 1 {
							clock = *p.ProPushStartedAt
							orderID = p.ProAuto.OrderID
							flowID = p.ProAuto.ID
						}
						for {
							attempt := authorizations.Add(1)
							times = append(times, time.Now())
							if attempt == 3 && scenario != "exhausted" {
								_, err := rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(current, "oauth"), "refresh_token": "new-rt"}})
								return err
							}
							if scenario == "child_exit" {
								return errors.New("child exited during authorization")
							}
							code := ""
							if scenario == "expired" {
								code = "siwc_session_expired"
							}
							result, err := rpc("pro_oauth_retry", map[string]any{"error_code": code, "error": "HTTP 200 password continuation / upstream failure"})
							if err != nil {
								return err
							}
							decision := result.(map[string]any)
							if decision["retry"] != true {
								return errProOAuthRetriesExhausted
							}
							if decision["relogin"] == true {
								return errProOAuthReloginReady
							}
						}
					},
					Push: func(_ context.Context, e string) (model.MailAccountProfile, error) {
						pushes.Add(1)
						return st.UpdateProAccount(e, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
					},
					Quota: func(_ context.Context, e string, _ bool) (model.MailAccountProfile, error) {
						return st.UpdateProAccount(e, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 10} })
					},
					Merge: func(context.Context, string) (model.MailAccountProfile, error) {
						t.Error("authorization failure executed final action")
						return model.MailAccountProfile{}, errors.New("unexpected final")
					},
				}
				response := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
				if response.Code != 202 {
					t.Fatal(response.Body.String())
				}
				p := waitProAutoFinished(t, s, email)
				if payments.Load() != 1 || authorizations.Load() != 3 || p.ProAuto.OAuthRecovery.Attempts != 2 || p.ProAuto.RetryAttempts != 0 || p.ProAuto.OrderID != orderID || p.ProAuto.ID != flowID || !p.ProPushStartedAt.Equal(clock) {
					t.Fatalf("payment/clock/budget changed: pay=%d auth=%d state=%+v", payments.Load(), authorizations.Load(), p.ProAuto)
				}
				for i := 1; i < len(times); i++ {
					if times[i].Sub(times[i-1]) < time.Second {
						t.Fatal("retry interval bypassed")
					}
				}
				wantLogins := int32(1)
				if scenario == "expired" || scenario == "child_exit" {
					wantLogins = 3
				}
				if logins.Load() != wantLogins {
					t.Fatalf("logins=%d want=%d", logins.Load(), wantLogins)
				}
				if scenario == "exhausted" {
					if !p.ProAuto.OAuthRecovery.Exhausted || pushes.Load() != 0 || p.ProAuto.Status != "failed" || p.ProAuto.SIWCConsentFinal() {
						t.Fatal("exhaustion failed", p.ProAuto)
					}
					p, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProRestockTaskID = "original-task" })
					if err != nil {
						t.Fatal(err)
					}
					for range 5 {
						s.queueRestockPaidRecovery(p, cfg)
					}
					if s.proAutomationRunning(email) || logins.Load() != 1 {
						t.Fatal("restock added more retries")
					}
				} else if pushes.Load() != 1 || p.ProAuto.Status != "waiting_quota" {
					t.Fatal("recovered flow did not continue", p.ProAuto)
				}
			})
		}
	}
}

func TestProOAuthRetryStopDuringConfiguredWait(t *testing.T) {
	var payments, logins atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		payments.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"id":"paid","status":"success"}}`)
	})
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	cfg, pass, _, _ := st.ProSettings()
	cfg.RetryCount = 2
	cfg.RetryIntervalSeconds = 10
	if _, err := st.SaveProSettings(cfg, pass, ""); err != nil {
		t.Fatal(err)
	}
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, e string, rpc func(string, map[string]any) (any, error)) error {
		logins.Add(1)
		if _, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(e, "login"), "user": map[string]any{"email": e}, "account": map[string]any{"id": "personal-id"}}}); err != nil {
			return err
		}
		_, err := rpc("pro_oauth_retry", map[string]any{"error": "failure"})
		return err
	}}
	if r := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"}); r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, _, _ := st.MailAccountCredential(email)
		if p.ProAuto.OAuthRecovery != nil {
			if time.Until(*p.ProAuto.OAuthRecovery.NextAt) < 8*time.Second {
				t.Fatal("10 second policy not used")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry never scheduled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	r := payCall(t, s.stopProAutomation, "email", email, map[string]any{})
	if r.Code != 200 && r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	p := waitProAutoFinished(t, s, email)
	if time.Since(start) > 2*time.Second || payments.Load() != 1 || logins.Load() != 1 || !p.ProAuto.StopRequested || p.ProAuto.OAuthRecovery.Attempts != 1 {
		t.Fatal("stop did not cancel wait", p.ProAuto)
	}
	if err := st.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	p, _, _ = st.MailAccountCredential(email)
	if p.ProAuto.Status != "stopped" || !p.ProAuto.StopRequested || p.ProAuto.OAuthRecovery.Attempts != 1 {
		t.Fatal("restart cleared stop or budget")
	}
	if strings.Contains(p.ProAuto.Error, "收尾") {
		t.Fatal("stop executed final")
	}
}

func TestProOAuthExhaustionReleasesRestockWithoutFinal(t *testing.T) {
	var payments, logins atomic.Int32
	s, _, email, _ := restockWindowIntegration(t, func(w http.ResponseWriter, r *http.Request) {
		payments.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"id":"paid","status":"success"}}`)
	})
	cfg, pass, _, _ := s.store.ProSettings()
	cfg.RetryCount = 1
	cfg.RetryIntervalSeconds = 1
	if _, err := s.store.SaveProSettings(cfg, pass, ""); err != nil {
		t.Fatal(err)
	}
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, e string, rpc func(string, map[string]any) (any, error)) error {
		logins.Add(1)
		if err := windowRecharge(s, e, rpc); err != nil {
			return err
		}
		return errors.New("authorization never returns code")
	}, Merge: func(context.Context, string) (model.MailAccountProfile, error) {
		t.Error("unexpected final action")
		return model.MailAccountProfile{}, nil
	}}
	windowTick(t, s)
	p := waitProAutoFinished(t, s, email)
	if !p.ProAuto.OAuthRecovery.Exhausted || payments.Load() != 1 || logins.Load() != 2 {
		t.Fatal("incorrect budget", p.ProAuto)
	}
	windowTick(t, s)
	tasks, err := s.store.ProRestockTasks()
	if err != nil || len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Fatal("exhausted task still blocks restock", tasks, err)
	}
	active, err := s.store.ProRestockActiveTasks()
	if err != nil || len(active) != 0 || s.proAutomationRunning(email) {
		t.Fatal("execution occupancy retained", err)
	}
	if payments.Load() != 1 || logins.Load() != 2 {
		t.Fatal("another retry after exhaustion")
	}
}
