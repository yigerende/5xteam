package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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

func quotaResumeFixture(t *testing.T, orderStatus string) *proExecutionFixture {
	t.Helper()
	f := newProExecutionFixture(t)
	settings, _, _, _ := f.s.store.ProSettings()
	settings.Sub2.URL, settings.Sub2.Email, settings.Sub2.GroupIDs = "http://127.0.0.1:1", "admin@example.com", []int64{1}
	if _, err := f.s.store.SaveProSettings(settings, "password", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := f.s.store.CreateGPTPayOrder(gptpay.Order{ID: "paid-order", Email: f.email, CardID: "original-card", Status: orderStatus, CreatedAt: now.Add(-time.Hour)}, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		oauthAt, pushAt := now.Add(-2*time.Minute), now.Add(-time.Minute)
		p.OAuthStatus, p.PushStatus, p.PushProvider = "completed", "completed", "sub2"
		p.OAuthAuthorizedAt, p.ProPushStartedAt = &oauthAt, &pushAt
		enabled := true
		p.ProPushTimeoutEnabled, p.ProPushTimeoutMinutes = &enabled, 105
		p.ProAuto = model.ProAutoState{ID: "stopped-flow", OrderID: "paid-order", CardID: "original-card", Status: "stopped", Stage: "login", StopRequested: true, StopVersion: 1, Error: "用户已停止全自动流程", StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-3 * time.Minute), QuotaUsedThreshold: 100, QuotaIntervalSeconds: 60, Steps: map[string]string{"login": "interrupted", "recharge": "completed", "oauth": "failed"}}
		p.ProManualStages = map[string]model.ProStageProgress{"oauth": {Status: "completed", StartedAt: oauthAt.Add(-time.Minute), UpdatedAt: oauthAt}, "push": {Status: "completed", StartedAt: pushAt.Add(-time.Second), UpdatedAt: pushAt}}
		p.ProStageOverrides = map[string]model.ProStageOverride{"quota": {Status: "waiting", UpdatedAt: now}}
		// A stopped replacement chain can retain a successor now outside Pro.
		// Recover only this paid account, preserving the successor's stop/history.
		p.ProAuto.Replacement = &model.ProRechargeReplacement{RootEmail: f.email, NextEmail: "stopped-successor@example.com", Limit: 1, Message: "用户已停止整条换号流程"}
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

const resumeQuotaBody = `{"resume_quota":true,"expected_flow_id":"stopped-flow","expected_stop_version":1}`

func TestProQuotaResumeOnlyMonitorsOriginalPaidAccount(t *testing.T) {
	for _, body := range []string{resumeQuotaBody, `{"retry_current":true}`} {
		t.Run(body, func(t *testing.T) {
			f := quotaResumeFixture(t, "success")
			var queries, merges atomic.Int32
			var percent atomic.Int32
			percent.Store(19)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture"}}`)
				case r.URL.Path == "/api/v1/admin/accounts/42/stats":
					fmt.Fprint(w, `{"code":0,"data":{"summary":{"total_standard_cost":0}}}`)
				case strings.HasSuffix(r.URL.Path, "/quota"):
					queries.Add(1)
					fmt.Fprintf(w, `{"code":0,"data":{"rate_limit":{"secondary_window":{"used_percent":%d,"limit_window_seconds":604800}}}}`, percent.Load())
				default:
					t.Errorf("unexpected provisioning call: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer upstream.Close()
			settings, _, _, _ := f.s.store.ProSettings()
			settings.Sub2.URL = upstream.URL
			if _, err := f.s.store.SaveProSettings(settings, "password", ""); err != nil {
				t.Fatal(err)
			}
			f.s.proAutoHooks = &proAutomationHooks{
				Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
					t.Error("restarted login/payment")
					return fmt.Errorf("unexpected provisioning")
				},
				Push: func(context.Context, string) (model.MailAccountProfile, error) {
					t.Error("repeated push")
					return model.MailAccountProfile{}, fmt.Errorf("unexpected push")
				},
				Merge: func(_ context.Context, email string) (model.MailAccountProfile, error) {
					merges.Add(1)
					return f.s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
						p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "completed", "completed", "completed", "completed"
						p.SpaceMergedOnce = true
					})
				},
			}
			before, _, _ := f.s.store.MailAccountCredential(f.email)
			if !before.CanResumeProQuota() {
				t.Fatal("fixture cannot resume")
			}
			// Manual push success alone must not silently lift a deliberate stop.
			if before.ReconcileProAutoManual(time.Now()) || !before.ProAuto.StopRequested {
				t.Fatal("manual push cleared stop")
			}
			get := f.request(f.s.getProAutomation, "GET", "/auto-pro", "")
			var response struct {
				Data struct {
					Available bool `json:"quota_resume_available"`
				} `json:"data"`
			}
			if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil || !response.Data.Available {
				t.Fatal(get.Body.String(), err)
			}
			rows, err := f.s.proAccountListWithCards([]model.MailAccountProfile{before})
			if err != nil || len(rows) != 1 || !rows[0].QuotaResumeAvailable {
				t.Fatal("missing list action", err)
			}
			out := f.request(f.s.startProAutomation, "POST", "/auto-pro", body)
			if out.Code != 202 {
				t.Fatal(out.Code, out.Body.String())
			}
			if duplicate := f.request(f.s.startProAutomation, "POST", "/auto-pro", resumeQuotaBody); duplicate.Code != 409 {
				t.Fatal("duplicate resume accepted")
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); f.s.queueDueProAutomation(f.email) }()
			}
			wg.Wait()
			p := waitProAutoFinished(t, f.s, f.email)
			if queries.Load() != 1 || merges.Load() != 0 || p.ProAuto.Status != "waiting_quota" || p.ProAuto.StopRequested || p.ProAuto.NextCheckAt == nil || p.ProAuto.Stage != "quota" || p.QuotaCheckedAt == nil {
				t.Fatalf("did not monitor: %+v queries=%d", p.ProAuto, queries.Load())
			}
			if d := time.Until(*p.ProAuto.NextCheckAt); d < 50*time.Second || d > 61*time.Second {
				t.Fatal("lost interval", d)
			}
			if p.ProAuto.ID != before.ProAuto.ID || p.ProAuto.OrderID != before.ProAuto.OrderID || p.ProAuto.CardID != before.ProAuto.CardID || !p.ProPushStartedAt.Equal(*before.ProPushStartedAt) || !p.ProPushDeadline().Equal(*before.ProPushDeadline()) {
				t.Fatal("changed original flow/payment/deadline")
			}
			if p.ProAuto.Replacement.NextEmail != before.ProAuto.Replacement.NextEmail || p.ProAuto.Replacement.Pending || p.ProAuto.Replacement.Queued {
				t.Fatal("changed replacement history or restarted replacement work")
			}
			if _, ok := p.ProStageOverrides["quota"]; ok {
				t.Fatal("stale quota display override")
			}
			percent.Store(100)
			if _, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { past := time.Now().Add(-time.Second); p.ProAuto.NextCheckAt = &past }); err != nil {
				t.Fatal(err)
			}
			f.s.queueDueProAutomation(f.email)
			p = waitProAutoFinished(t, f.s, f.email)
			if queries.Load() != 2 || merges.Load() != 1 || p.ProAuto.Status != "completed" {
				t.Fatal("normal threshold continuation broken", p.ProAuto)
			}
		})
	}
}

func TestProQuotaResumeGuards(t *testing.T) {
	for _, mode := range []string{"unpaid", "display_only", "stale_oauth", "stale_push", "merged", "changed_stop", "changed_flow", "busy", "automatic", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			status := "success"
			if mode == "unpaid" {
				status = "processing"
			}
			f := quotaResumeFixture(t, status)
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				switch mode {
				case "display_only":
					p.PushStatus = "pending"
					p.ProStageOverrides["push"] = model.ProStageOverride{Status: "completed", UpdatedAt: time.Now()}
				case "stale_oauth":
					x := p.ProAuto.StartedAt.Add(-time.Second)
					p.OAuthAuthorizedAt = &x
				case "stale_push":
					p.ProManualStages["push"] = model.ProStageProgress{Status: "completed", StartedAt: p.ProAuto.StartedAt.Add(-time.Minute), UpdatedAt: time.Now()}
				case "merged":
					p.SpaceMergedOnce = true
				case "changed_stop":
					p.ProAuto.StopVersion++
				case "changed_flow":
					p.ProAuto.ID = "another-flow"
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "busy" {
				v, _ := f.s.proLocks.LoadOrStore("account:"+f.email, &sync.Mutex{})
				lock := v.(*sync.Mutex)
				lock.Lock()
				defer lock.Unlock()
			}
			if mode == "shutdown" {
				f.s.proAutoClosed = true
			}
			r := httptest.NewRequest("POST", "/auto-pro", strings.NewReader(resumeQuotaBody))
			r.SetPathValue("email", f.email)
			if mode == "automatic" {
				r = r.WithContext(context.WithValue(r.Context(), proScheduledContextKey{}, true))
			}
			w := httptest.NewRecorder()
			f.s.startProAutomation(w, r)
			if w.Code != 409 {
				t.Fatal("unsafe resume admitted", w.Code, w.Body.String())
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if !p.ProAuto.StopRequested || p.ProAuto.NextCheckAt != nil {
				t.Fatal("changed stopped account")
			}
		})
	}
}
