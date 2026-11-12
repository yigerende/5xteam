package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProRetryOriginalPayment(t *testing.T) {
	for _, tc := range []struct {
		name, status, remote, result string
		success                      bool
		creates                      int32
	}{
		{"paid", "success", "original-remote", "success", true, 0},
		{"paid login fails", "success", "original-remote", "success", true, 0},
		{"paid proxy fails", "success", "original-remote", "success", true, 0},
		{"review becomes paid", "manual_review", "original-remote", "success", true, 0},
		{"still reviewing", "manual_review", "original-remote", "manual_review", false, 0},
		{"still processing", "processing", "original-remote", "processing", false, 0},
		{"ambiguous replay", "submission_unknown", "", "success", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var creates, logins, pushes, merges atomic.Int32
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/status") {
					fmt.Fprintf(w, `{"code":0,"data":{"orders":[{"id":"original-remote","status":%q,"failureReason":"supplier detail"}]}}`, tc.result)
					return
				}
				creates.Add(1)
				if r.Header.Get("Idempotency-Key") != "pro-original-recovery" {
					t.Error("created a new payment key")
				}
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "sessionToken") {
					t.Error("legacy replay was altered")
				}
				fmt.Fprintf(w, `{"code":0,"data":{"id":"original-remote","status":%q}}`, tc.result)
			})
			configureProAutoFixture(t, s)
			if tc.name == "paid proxy fails" {
				primary, _ := st.SaveProxy(model.ProxyProfile{Name: "primary", URL: "http://primary.example:8080"})
				backup, _ := st.SaveProxy(model.ProxyProfile{Name: "backup", URL: "http://backup.example:8080"})
				settings := st.Settings()
				settings.ProxyURL = primary.URL
				settings.OAuthProxyMode = "global"
				settings.ProxyFailover = model.DefaultGlobalProxyPolicy()
				settings.ProxyFailover.BackupIDs = []string{backup.ID}
				if err := st.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			captureProNotifications(s)
			cfg, key, _ := st.GPTPaySettings()
			p, c, _ := st.MailAccountCredential(email)
			session, err := gptPaySession(p, c)
			if err != nil {
				t.Fatal(err)
			}
			o := gptpay.Order{ID: "pro-original-recovery", Email: email, CardID: "test-card", PlanCode: "pro20", Status: tc.status, Remote: gptpay.RemoteOrder{ID: tc.remote, Status: tc.status}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			snap := gptpay.Snapshot{URL: cfg.URL, APIKey: key, Input: gptpay.CreateInput{PlanCode: "pro20", Session: session}}
			if err = st.CreateGPTPayOrder(o, snap); err != nil {
				t.Fatal(err)
			}
			_, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = model.ProAutoState{ID: "existing-flow", Status: "failed", Stage: "recharge", OrderID: o.ID, Steps: map[string]string{"login": "completed", "recharge": "failed"}}
			})
			if err != nil {
				t.Fatal(err)
			}
			s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
				logins.Add(1)
				assertRecoveryStage := func() {
					t.Helper()
					current, _, _ := st.MailAccountCredential(email)
					progress := current.ProStages()
					if current.ProAuto.Stage != "oauth" || progress.Steps["login"] != "completed" || progress.Steps["recharge"] != "completed" || progress.Steps["oauth"] != "running" || current.ProAuto.OrderID != o.ID || current.ProAuto.ID != "existing-flow" {
						t.Errorf("paid recovery went back to purchase/login stages: %+v", current.ProAuto)
					}
				}
				assertRecoveryStage()
				if tc.name == "paid proxy fails" {
					lease, err := s.acquireOAuthProxy()
					if err != nil {
						return err
					}
					defer lease.Release()
					if logins.Load() == 1 {
						if lease.label != "primary" {
							t.Error("recovery did not start on primary")
						}
						return s.recordLoginProxyFailure(lease, fmt.Errorf("CONNECT tunnel failed, response 504"))
					}
					if logins.Load() != 2 || lease.label != "backup" {
						t.Error("paid recovery did not use backup")
					}
				}
				if tc.name == "paid login fails" {
					return fmt.Errorf("fixture recovery login failed")
				}
				if _, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(email, "recovered")}}); err != nil {
					return err
				}
				assertRecoveryStage()
				_, err := rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(email, "after"), "refresh_token": "recovered-rt"}})
				return err
			}, Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
				pushes.Add(1)
				return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
			}, Quota: func(ctx context.Context, email string, _ bool) (model.MailAccountProfile, error) {
				return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 100} })
			}, Merge: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
				merges.Add(1)
				return st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.SpaceMergedOnce = true; p.RemoveStatus = "completed" })
			}}
			rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"retry_current": true})
			if tc.success {
				if rec.Code != 202 {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
				p = waitProAutoFinished(t, s, email)
				if tc.name == "paid login fails" {
					if p.ProAuto.Status != "failed" || p.ProAuto.Stage != "oauth" || p.ProAuto.Steps["login"] != "completed" || p.ProAuto.Steps["recharge"] != "completed" || p.ProAuto.OrderID != o.ID || logins.Load() != 1 || pushes.Load() != 0 || creates.Load() != 0 {
						t.Fatalf("paid login failure lost completed purchase: %+v", p.ProAuto)
					}
					return
				}
				wantLogins := int32(1)
				if tc.name == "paid proxy fails" {
					wantLogins = 2
				}
				if p.ProAuto.Status != "completed" || p.ProAuto.OrderID != o.ID || p.ProAuto.ID != "existing-flow" || p.ProAuto.Steps["login"] != "completed" || logins.Load() != wantLogins || pushes.Load() != 1 || merges.Load() != 1 {
					t.Fatalf("recovery failed: %+v", p.ProAuto)
				}
			} else {
				if rec.Code != 409 || logins.Load() != 0 || pushes.Load() != 0 || merges.Load() != 0 {
					t.Fatalf("unsafe continuation: %d %s", rec.Code, rec.Body.String())
				}
			}
			if creates.Load() != tc.creates {
				t.Fatalf("payment creates %d, want %d", creates.Load(), tc.creates)
			}
			want := ""
			if tc.success {
				want = "pro_pushed"
			}
			if got := proNotificationKinds(t, s); got != want {
				t.Fatalf("paid recovery notices %q, want %q (no new start)", got, want)
			}
		})
	}
}

func TestProPaidRecoveryRejectsWrongIdentityAndOrder(t *testing.T) {
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected payment") })
	p, c, _ := st.MailAccountCredential(email)
	session, _ := gptPaySession(p, c)
	o := gptpay.Order{ID: "pro-wrong-identity", Email: email, CardID: "test-card", Status: "success", CreatedAt: time.Now()}
	if err := st.CreateGPTPayOrder(o, gptpay.Snapshot{Input: gptpay.CreateInput{Session: session}}); err != nil {
		t.Fatal(err)
	}
	p.ProAuto.OrderID = o.ID
	if _, err := s.confirmProPaidSession(p, map[string]any{"session": map[string]any{"access_token": proAutoToken("other@example.com", "wrong")}}); err == nil {
		t.Fatal("accepted wrong identity")
	}
	p.Email = "other@example.com"
	if _, err := s.confirmProPaidSession(p, map[string]any{"session": map[string]any{"access_token": proAutoToken(p.Email, "wrong")}}); err == nil {
		t.Fatal("accepted order belonging to another account")
	}
}

func TestProRetryPaymentStopAndConcurrency(t *testing.T) {
	for _, mode := range []string{"stop", "duplicate", "shutdown", "stop_failed", "duplicate_failed"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var requests, logins atomic.Int32
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !strings.HasSuffix(r.URL.Path, "/status") {
					t.Error("unexpected creation")
				}
				close(entered)
				<-release
				result := "success"
				if strings.HasSuffix(mode, "_failed") {
					result = "failed"
				}
				fmt.Fprintf(w, `{"code":0,"data":{"orders":[{"id":"remote","status":%q}]}}`, result)
			})
			configureProAutoFixture(t, s)
			cfg, key, _ := st.GPTPaySettings()
			p, c, _ := st.MailAccountCredential(email)
			session, _ := gptPaySession(p, c)
			o := gptpay.Order{ID: "pro-recovery-guard", Email: email, CardID: "test-card", Status: "manual_review", Remote: gptpay.RemoteOrder{ID: "remote"}, CreatedAt: time.Now()}
			if err := st.CreateGPTPayOrder(o, gptpay.Snapshot{URL: cfg.URL, APIKey: key, Input: gptpay.CreateInput{Session: session}}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = model.ProAutoState{ID: "flow", OrderID: o.ID, Status: "failed", Steps: map[string]string{}}
			}); err != nil {
				t.Fatal(err)
			}
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				logins.Add(1)
				return fmt.Errorf("fixture ends login")
			}}
			if mode == "shutdown" {
				s.proAutoMu.Lock()
				s.proAutoClosed = true
				s.proAutoMu.Unlock()
				rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"retry_current": true})
				if rec.Code != 409 || requests.Load() != 0 {
					t.Fatal("shutdown admitted retry")
				}
				return
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- payCall(t, s.startProAutomation, "email", email, map[string]any{"retry_current": true})
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no status query")
			}
			if strings.HasPrefix(mode, "stop") {
				if rec := payCall(t, s.stopProAutomation, "email", email, nil); rec.Code != 200 {
					t.Fatal(rec.Body.String())
				}
			} else {
				if rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"retry_current": true}); rec.Code != 409 {
					t.Fatal("duplicate admitted")
				}
			}
			once.Do(func() { close(release) })
			var rec *httptest.ResponseRecorder
			select {
			case rec = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("retry stuck")
			}
			p = waitProAutoFinished(t, s, email)
			if requests.Load() != 1 {
				t.Fatal("duplicate query/payment")
			}
			if strings.HasPrefix(mode, "stop") && (rec.Code != 409 || logins.Load() != 0 || !p.ProAuto.StopRequested) {
				t.Fatal("stop lost to recovery")
			}
			if strings.HasPrefix(mode, "duplicate") && (rec.Code != 202 || logins.Load() != 1) {
				t.Fatal("recovery not serialized")
			}
		})
	}
}

func TestProReviewPaymentProjection(t *testing.T) {
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected network") })
	o := gptpay.Order{ID: "pro-review-projection", Email: email, CardID: "test-card", Status: "manual_review", Error: "sessionToken required", CreatedAt: time.Now()}
	if err := st.CreateGPTPayOrder(o, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	p, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "flow", OrderID: o.ID, Status: "failed", Stage: "recharge", Steps: map[string]string{"recharge": "failed"}}
		p.ProManualStages = map[string]model.ProStageProgress{"recharge": {ID: o.ID, Status: "running"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.proAccountListWithCards([]model.MailAccountProfile{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].StageProgress.Steps["recharge"] != "unknown" || rows[0].StageProgress.Errors["recharge"] != o.Error {
		t.Fatal("stale running list state")
	}
	req := httptest.NewRequest("GET", "/test", nil)
	req.SetPathValue("email", email)
	rec := httptest.NewRecorder()
	s.getProAutomation(rec, req)
	var env struct {
		Data struct {
			model.ProAutoState
			StageProgress model.ProStageDisplay `json:"stage_progress"`
		} `json:"data"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.StageProgress.Steps["recharge"] != "unknown" || env.Data.Error != o.Error {
		t.Fatalf("stale automation projection: %s", rec.Body.String())
	}
	s.updateProPaymentStage(o)
	p, _, _ = st.MailAccountCredential(email)
	if p.ProStages().Steps["recharge"] != "unknown" {
		t.Fatal("background overwrote failure as running")
	}
}
