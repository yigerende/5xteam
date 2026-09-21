package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func proAutoToken(email, version string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"version":%q,"https://api.openai.com/profile":{"email":%q},"https://api.openai.com/auth":{"chatgpt_account_id":"personal-id","chatgpt_user_id":"user-id"}}`, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), version, email))) + ".sig"
}
func configureProAutoFixture(t *testing.T, s *Server) {
	t.Helper()
	v := model.DefaultProSettings()
	v.Sub2.URL = "http://sub2.invalid"
	v.Sub2.Email = "admin@example.com"
	v.Sub2.GroupIDs = []int64{1}
	v.TargetAdminID = "fixture-mother"
	if _, err := s.store.SaveProSettings(v, "test-password", ""); err != nil {
		t.Fatal(err)
	}
}
func waitProAutoFinished(t *testing.T, s *Server, email string) model.MailAccountProfile {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !s.proAutomationRunning(email) {
			p, _, err := s.store.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("automation did not finish")
	return model.MailAccountProfile{}
}

func TestProAutoContinuousPaymentToCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		quota        float64
		threshold    float64
		paymentCode  int
		loseSession  bool
		cancellation string
	}{
		{name: "complete", status: "completed", quota: 100},
		{name: "cancel_failed_still_complete", status: "completed", quota: 100, cancellation: "failed"},
		{name: "cancel_pending_still_complete", status: "completed", quota: 100, cancellation: "pending"},
		{name: "wait_quota", status: "waiting_quota", quota: 30},
		{name: "shared_threshold_reached", status: "completed", quota: 85, threshold: 85},
		{name: "shared_threshold_below", status: "waiting_quota", quota: 84, threshold: 85},
		{name: "payment_failed", status: "failed", paymentCode: 402},
		{name: "uncertain", status: "failed", paymentCode: 503},
		{name: "session_lost_after_purchase", status: "failed", loseSession: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var purchases, logins, pushes, merges atomic.Int32
			var verifyInitialSaved func()
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				verifyInitialSaved()
				if strings.HasSuffix(r.URL.Path, "/status") {
					fmt.Fprintf(w, `{"code":0,"data":{"orders":[{"id":"auto-remote","status":"success","cancellationStatus":%q}]}}`, tc.cancellation)
					return
				}
				purchases.Add(1)
				if tc.paymentCode != 0 {
					w.WriteHeader(tc.paymentCode)
					fmt.Fprintf(w, `{"code":%d01}`, tc.paymentCode)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"id":"auto-remote","status":"processing"}}`)
			})
			configureProAutoFixture(t, s)
			verifyInitialSaved = func() {
				p, c, err := st.MailAccountCredential(email)
				if err != nil || c.AccessToken != proAutoToken(email, "before") || !strings.Contains(c.ChatGPTSession, "first-session-cookie") || !p.ChatGPTSessionPresent || p.ProAuto.Steps["login"] != "completed" {
					t.Error("payment dispatched before login AT and Session were persisted")
				}
			}
			if tc.threshold > 0 {
				pro, _, _, _ := st.ProSettings()
				pro.QuotaUsedThreshold, pro.QuotaCheckIntervalSeconds = tc.threshold, 45
				if _, err := st.SaveProSettings(pro, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			s.proAutoHooks = &proAutomationHooks{PollDelay: time.Millisecond, Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
				if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = "198.51.100.8" }); err != nil {
					return err
				}
				logins.Add(1)
				_, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(email, "before"), "accessToken": proAutoToken(email, "before"), "session_token": "first-session-cookie", "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}, "exit_ip": "198.51.100.8"})
				if err != nil {
					return err
				}
				if tc.loseSession {
					return errors.New("session expired")
				}
				_, err = rpc("pro_tokens", map[string]any{"exit_ip": "198.51.100.8", "tokens": map[string]any{"access_token": proAutoToken(email, "after"), "refresh_token": "after-rt"}, "web_session": map[string]any{"accessToken": "updated-web-at", "session_token": "updated-session-cookie"}})
				return err
			}, Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
				pushes.Add(1)
				_, c, _ := st.MailAccountCredential(email)
				if c.AccessToken != proAutoToken(email, "after") || c.RefreshToken != "after-rt" {
					t.Error("pushed pre-purchase credentials")
				}
				if !strings.Contains(c.ChatGPTSession, "updated-session-cookie") || !strings.Contains(c.ChatGPTSession, "updated-web-at") {
					t.Error("post-purchase Session was not persisted before pushing")
				}
				return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "completed"; p.PushProvider = "sub2" })
			}, Quota: func(ctx context.Context, email string, auto bool) (model.MailAccountProfile, error) {
				if auto {
					t.Error("legacy auto-merge must not run")
				}
				return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: tc.quota} })
			}, Merge: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
				merges.Add(1)
				return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.InviteStatus = "completed"
					p.AcceptStatus = "completed"
					p.TransferStatus = "completed"
					p.RemoveStatus = "completed"
					p.SpaceMergedOnce = true
				})
			}}
			rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
			if rec.Code != 202 {
				t.Fatal(rec.Body.String())
			}
			p := waitProAutoFinished(t, s, email)
			if tc.threshold > 0 && (p.ProAuto.QuotaUsedThreshold != tc.threshold || p.ProAuto.QuotaIntervalSeconds != 45) {
				t.Fatal("new task did not use shared Pro quota settings")
			}
			if p.ProAuto.Status != tc.status {
				t.Fatalf("%s %s", p.ProAuto.Status, p.ProAuto.Error)
			}
			if purchases.Load() != 1 || logins.Load() != 1 {
				t.Fatal("duplicate login/payment")
			}
			if tc.status == "completed" {
				for _, step := range []string{"login", "recharge", "oauth", "push", "quota", "merge"} {
					if p.ProAuto.Steps[step] != "completed" {
						t.Fatal(step)
					}
				}
				if merges.Load() != 1 {
					t.Fatal("merge not called")
				}
			}
			if tc.status == "waiting_quota" {
				if merges.Load() != 0 || p.ProAuto.NextCheckAt == nil {
					t.Fatal("merged below threshold")
				}
				if p.ProAuto.Error != "" || p.ProStages().Steps["quota"] != "waiting" {
					t.Fatal("ordinary quota wait shown as failure or completion")
				}
			}
			if tc.status == "failed" {
				_, saved, readErr := st.MailAccountCredential(email)
				if readErr != nil || saved.AccessToken != proAutoToken(email, "before") || !strings.Contains(saved.ChatGPTSession, "first-session-cookie") {
					t.Fatal("payment or OAuth failure lost the successful first login")
				}
				if pushes.Load() != 0 || merges.Load() != 0 {
					t.Fatal("continued after payment/session error")
				}
				rec = payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
				if rec.Code != 409 {
					t.Fatal("restarted payment after error")
				}
			}
		})
	}
}

func TestProAutoConcurrentStartAndConflictingActions(t *testing.T) {
	s, _, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected payment") })
	configureProAutoFixture(t, s)
	var starts atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
		starts.Add(1)
		close(entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return errors.New("fixture stop")
		}
	}}
	rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
			if out.Code != 202 {
				t.Error(out.Code)
			}
		}()
	}
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatal("concurrent duplicate workers")
	}
	protected := s.guardProAutomation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("conflicting login reached handler") }))
	out := httptest.NewRecorder()
	protected.ServeHTTP(out, httptest.NewRequest("POST", "/api/mail/accounts/"+email+"/oauth", nil))
	if out.Code != 409 {
		t.Fatal("login wasn't blocked")
	}
	close(release)
	waitProAutoFinished(t, s, email)
}

func TestProAutoResumeTailDoesNotLoginOrPay(t *testing.T) {
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("must not purchase") })
	configureProAutoFixture(t, s)
	_, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "prior", Status: "interrupted", OrderID: "paid-order", QuotaUsedThreshold: 80, QuotaIntervalSeconds: 30, Steps: map[string]string{"login": "completed", "recharge": "completed", "oauth": "completed", "push": "completed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	var merged bool
	s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
		t.Fatal("relogin")
		return nil
	}, Push: func(context.Context, string) (model.MailAccountProfile, error) {
		t.Fatal("duplicate push")
		return model.MailAccountProfile{}, nil
	}, Quota: func(ctx context.Context, email string, _ bool) (model.MailAccountProfile, error) {
		return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 85} })
	}, Merge: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
		merged = true
		p, _, err := st.MailAccountCredential(email)
		return p, err
	}}
	rec := payCall(t, s.startProAutomation, "email", email, map[string]any{})
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	p := waitProAutoFinished(t, s, email)
	if !merged || p.ProAuto.Status != "completed" {
		t.Fatal("did not use configured 80% threshold", p.ProAuto)
	}
}

func TestProAutoDueSchedulerAndStop(t *testing.T) {
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected purchase") })
	configureProAutoFixture(t, s)
	var calls atomic.Int32
	s.proAutoHooks = &proAutomationHooks{Quota: func(ctx context.Context, email string, auto bool) (model.MailAccountProfile, error) {
		calls.Add(1)
		p, _, err := st.MailAccountCredential(email)
		return p, err
	}}
	future := time.Now().UTC().Add(time.Hour)
	_, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "due", Status: "waiting_quota", QuotaUsedThreshold: 100, QuotaIntervalSeconds: 120, NextCheckAt: &future, Steps: map[string]string{"oauth": "completed", "push": "completed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	s.queueDueProAutomation(email)
	if calls.Load() != 0 {
		t.Fatal("future task executed early")
	}
	past := time.Now().UTC().Add(-time.Minute)
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.NextCheckAt = &past })
	s.queueDueProAutomation(email)
	p := waitProAutoFinished(t, s, email)
	if calls.Load() != 1 || p.ProAuto.Status != "waiting_quota" || p.ProAuto.NextCheckAt == nil || !p.ProAuto.NextCheckAt.After(time.Now()) {
		t.Fatal("missing quota not rescheduled", p.ProAuto)
	}
	rec := payCall(t, s.stopProAutomation, "email", email, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	s.queueDueProAutomation(email)
	if calls.Load() != 1 {
		t.Fatal("stopped job still ran")
	}
}
