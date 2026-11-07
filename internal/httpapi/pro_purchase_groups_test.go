package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func purchaseSub2Fixture(t *testing.T, want []int64) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	pushes := &atomic.Int32{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-session"}}`)
		case r.URL.Path == "/api/v1/admin/groups/all":
			fmt.Fprint(w, `{"code":0,"data":[{"id":1,"name":"Pro","platform":"openai"},{"id":15,"name":"0.15","platform":"openai"},{"id":16,"name":"Other","platform":"anthropic"}]}`)
		case r.Method == "POST" && r.URL.Path == "/api/v1/admin/accounts":
			var body struct {
				GroupIDs    []int64 `json:"group_ids"`
				Concurrency int     `json:"concurrency"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !slices.Equal(body.GroupIDs, want) || body.Concurrency < 1 {
				t.Errorf("unexpected push payload: %+v, want groups=%v, err=%v", body, want, err)
			}
			pushes.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"id":88,"name":"fixture"}}`)
		default:
			t.Errorf("unexpected Sub2 call: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(remote.Close)
	return remote, pushes
}

func TestProPurchaseGroupsValidationAndPersistence(t *testing.T) {
	s, _, _, _ := allocationFixture(t)
	remote, pushes := purchaseSub2Fixture(t, nil)
	p, _, _, _ := s.store.ProSettings()
	p.Sub2.URL = remote.URL
	p.PurchaseScheduleEnabled = true
	p.PurchaseWindows = []model.ProPurchaseWindow{activePurchaseWindow("ejh", "pro5")}
	p.EJHPlanTemplates = map[string]string{"pro5": "allocation"}
	cfg, _, _ := s.store.EJHSettings()
	cfg.Templates[0].PlanCode = "pro5"
	s.store.SaveEJHSettings(cfg, "")
	s.store.SaveProSettings(p, "", "")
	pay, _, _ := s.store.GPTPaySettings()
	for _, tc := range []struct {
		ids  []int64
		code int
	}{
		{[]int64{15}, 200}, {[]int64{999}, 400}, {[]int64{16}, 400}, {[]int64{-1}, 400},
	} {
		p.PurchaseWindows[0].ExtraSub2GroupIDs = tc.ids
		out := payCall(t, s.saveProAutomationSettings, "", "", map[string]any{"automation": p, "gptpay": pay})
		if out.Code != tc.code {
			t.Fatal(tc.ids, out.Code, out.Body.String())
		}
		saved, _, _, _ := s.store.ProSettings()
		if !slices.Equal(saved.PurchaseWindows[0].ExtraSub2GroupIDs, []int64{15}) || !slices.Equal(saved.Sub2.GroupIDs, []int64{1}) {
			t.Fatal("failed save changed selections or base groups", saved.PurchaseWindows)
		}
	}
	if pushes.Load() != 0 {
		t.Fatal("saving configuration created accounts")
	}
}

func TestProPurchaseGroupsKeepScheduledInflightVisible(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected purchase") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	p, password, _, _ := st.ProSettings()
	id, err := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: p.Sub2, Secret: password})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateProAccount(email, func(v *model.MailAccountProfile) {
		v.ProRestockTargetID = id
		v.ProAuto = model.ProAutoState{ID: "scheduled", Status: "running", StartedAt: time.Now(), RestockBinding: restockBinding(p), Sub2PushGroupIDs: []int64{1, 15}}
	})
	if err != nil {
		t.Fatal(err)
	}
	s.proAutoJobs = map[string]context.CancelFunc{email: func() {}}
	defer delete(s.proAutoJobs, email)
	tasks, _, err := s.restockTasks(restock.Snapshot{Binding: restockBinding(p), GroupID: 1}, p, restock.Config{GroupID: 1, LeadMinutes: 5, LeadSamples: 2})
	if err != nil || len(tasks) != 1 || tasks[0].Email != email {
		t.Fatal("scheduled task disappeared from restock inflight", tasks, err)
	}
}

func TestProPurchaseGroupsDoNotAffectManualPush(t *testing.T) {
	f := newProExecutionFixture(t)
	remote, pushes := purchaseSub2Fixture(t, []int64{1})
	p, _, _, _ := f.s.store.ProSettings()
	p.Sub2.URL, p.Sub2.Email, p.Sub2.GroupIDs = remote.URL, "fixture@example.com", []int64{1}
	p.PurchaseScheduleEnabled = true
	p.PurchaseWindows = []model.ProPurchaseWindow{activePurchaseWindow("nexovcc", "pro20")}
	p.PurchaseWindows[0].ExtraSub2GroupIDs = []int64{15}
	if _, err := f.s.store.SaveProSettings(p, "password", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.performProPush(context.Background(), f.email); err != nil {
		t.Fatal(err)
	}
	if pushes.Load() != 1 {
		t.Fatal("manual push missing")
	}
}

func TestProPurchaseExtraGroupsLeaveWithPostMergeGroups(t *testing.T) {
	f := newProGroupFixture(t)
	f.groups = []int64{1, 15}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	p, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProAuto.Sub2PushGroupIDs = []int64{1, 15}
		p.TransferStatus, p.RemoveStatus = "completed", "completed"
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, _, _, _ := f.s.store.ProSettings()
	p, err = f.s.prepareProPostMergeGroups(p, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.finishProPostMergeGroups(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.groups, []int64{7, 8}) {
		t.Fatal("extra live group retained after removal", f.groups)
	}
}

func TestProPurchasePushUsesLatestSiteAfterRestart(t *testing.T) {
	remote, oldPushes := purchaseSub2Fixture(t, []int64{1, 15})
	current, pushes := purchaseSub2Fixture(t, []int64{99})
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	p := model.DefaultProSettings()
	p.Sub2.URL, p.Sub2.Email, p.Sub2.GroupIDs = remote.URL, "fixture@example.com", []int64{1}
	p.Sub2.AccountConcurrency, p.Sub2.Models = 50, []string{"gpt-5"}
	target, err := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: p.Sub2, Secret: "password"})
	if err != nil {
		t.Fatal(err)
	}
	email := "restart@example.com"
	_, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email, AccessToken: proAutoToken(email, "fixture"), RefreshToken: "fixture-rt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateProAccount(email, func(v *model.MailAccountProfile) {
		v.ProRestockTargetID = target
		v.ProAuto = model.ProAutoState{ID: "started-in-global-window", Status: "interrupted", Stage: "push", Sub2PushGroupIDs: []int64{1, 15}}
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Sub2.URL, p.Sub2.GroupIDs, p.Sub2.AccountConcurrency = current.URL, []int64{99}, 2
	if _, err = st.SaveProSettings(p, "changed-password", ""); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, sub2: sub2.New()}
	profile, _, err := st.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _, err := s.proNewPushSettings(profile)
	if err != nil || base.Sub2.URL != current.URL || base.Sub2.AccountConcurrency != 2 || !slices.Equal(base.Sub2.GroupIDs, []int64{99}) {
		t.Fatal("unsubmitted task did not use current settings after restart", err)
	}
	if _, err = s.performProPush(context.Background(), email); err != nil {
		t.Fatal(err)
	}
	if pushes.Load() != 1 || oldPushes.Load() != 0 {
		t.Fatal("push did not use latest target")
	}
}
