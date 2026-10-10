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

	"chatgpt-space-merge/internal/model"
)

func TestProManualRecoveryResumesQuotaWithoutRepurchaseOrRepush(t *testing.T) {
	f := newProExecutionFixture(t)
	var pushes, queries, merges atomic.Int32
	var quotaPercent atomic.Int32
	quotaPercent.Store(30)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-session"}}`)
		case r.Method == "POST" && r.URL.Path == "/api/v1/admin/accounts":
			pushes.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"fixture"}}`)
		case strings.HasSuffix(r.URL.Path, "/quota"):
			queries.Add(1)
			fmt.Fprintf(w, `{"code":0,"data":{"rate_limit":{"secondary_window":{"used_percent":%d,"limit_window_seconds":604800}}}}`, quotaPercent.Load())
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer upstream.Close()
	settings, _, _, _ := f.s.store.ProSettings()
	settings.Sub2.URL, settings.Sub2.Email, settings.Sub2.GroupIDs = upstream.URL, "admin@example.com", []int64{1}
	if _, err := f.s.store.SaveProSettings(settings, "password", ""); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.PushStatus = "pending"
		p.ProAuto = model.ProAutoState{ID: "paid-auto", OrderID: "original-order", Status: "failed", Stage: "oauth", Error: "session expired", StartedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Minute), QuotaUsedThreshold: 85, QuotaIntervalSeconds: 45, Steps: map[string]string{"login": "completed", "recharge": "completed", "oauth": "failed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.store.StartProManualStage(f.email, "oauth", "retry"); err != nil {
		t.Fatal(err)
	}
	getState := func() struct {
		model.ProAutoState
		StageProgress model.ProStageDisplay `json:"stage_progress"`
	} { w := f.request(f.s.getProAutomation, "GET", "/auto-pro", ""); var response struct {
		Data struct {
			model.ProAutoState
			StageProgress model.ProStageDisplay `json:"stage_progress"`
		} `json:"data"`
	}; if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal(w.Body.String())
	}; return response.Data }
	state := getState()
	if state.StageProgress.Steps["oauth"] != "running" || state.Error != "" {
		t.Fatal("dialog does not show manual retry", state)
	}
	f.s.finishMailAccountOAuthJob("retry", f.email, map[string]any{"success": true, "access_token": proAutoToken(f.email, "recovered"), "refresh_token": "saved-rt"}, nil)
	state = getState()
	if state.Status != "awaiting_push" || state.Steps["oauth"] != "completed" || state.Error != "" {
		t.Fatal("oauth not reconciled", state)
	}
	if due, e := f.s.store.DueProAutomations(time.Now()); e != nil || len(due) != 0 {
		t.Fatal("scheduled before push", e)
	}
	w := f.request(f.s.pushProAccount, "POST", "/push", "{}")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	state = getState()
	if state.Status != "waiting_quota" || state.Steps["push"] != "completed" || state.NextCheckAt == nil || state.Error != "" {
		t.Fatal("push did not restore quota schedule", state)
	}
	f.s.proAutoHooks = &proAutomationHooks{
		Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
			t.Error("repeated login/payment")
			return nil
		},
		Push: func(context.Context, string) (model.MailAccountProfile, error) {
			t.Error("repeated manual push")
			return model.MailAccountProfile{}, nil
		},
		Merge: func(_ context.Context, email string) (model.MailAccountProfile, error) {
			merges.Add(1)
			return f.s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "completed", "completed", "completed", "completed"
				p.SpaceMergedOnce = true
			})
		},
	}
	// The async login gate must prevent a due tail from racing another login.
	f.s.oauthMu.Lock()
	f.s.oauthJobs["another-login"] = map[string]any{"email": f.email, "status": "running"}
	f.s.oauthMu.Unlock()
	f.s.queueDueProAutomation(f.email)
	if f.s.proAutomationRunning(f.email) || queries.Load() != 0 {
		t.Fatal("tail raced async login")
	}
	f.s.oauthMu.Lock()
	delete(f.s.oauthJobs, "another-login")
	f.s.oauthMu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.s.queueDueProAutomation(f.email) }()
	}
	wg.Wait()
	p := waitProAutoFinished(t, f.s, f.email)
	if queries.Load() != 1 || pushes.Load() != 1 || merges.Load() != 0 || p.ProAuto.Status != "waiting_quota" || p.ProAuto.ProvisionedAt == nil || p.ProAuto.NextCheckAt == nil {
		t.Fatalf("bad first quota run: %+v, calls %d/%d/%d", p.ProAuto, pushes.Load(), queries.Load(), merges.Load())
	}
	if d := time.Until(*p.ProAuto.NextCheckAt); d < 35*time.Second || d > 46*time.Second {
		t.Fatal("lost configured interval", d)
	}
	quotaPercent.Store(85)
	_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { past := time.Now().Add(-time.Second); p.ProAuto.NextCheckAt = &past })
	if err != nil {
		t.Fatal(err)
	}
	f.s.queueDueProAutomation(f.email)
	p = waitProAutoFinished(t, f.s, f.email)
	if p.ProAuto.Status != "completed" || queries.Load() != 2 || pushes.Load() != 1 || merges.Load() != 1 || p.ProAuto.OrderID != "original-order" {
		t.Fatal("threshold continuation failed", p.ProAuto)
	}
}
