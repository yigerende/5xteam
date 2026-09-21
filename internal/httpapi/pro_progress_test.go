package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func assertProProgress(t *testing.T, s *Server, email, stage, want string) {
	t.Helper()
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ProStages().Steps[stage]; got != want {
		t.Fatalf("%s: got %q want %q", stage, got, want)
	}
	if p.ProAuto.ID != "" {
		t.Fatal("manual operation created automatic task")
	}
	w := httptest.NewRecorder()
	s.listProAccounts(w, httptest.NewRequest("GET", "/api/pro-accounts?page=1&page_size=10", nil))
	var envelope struct {
		Data struct {
			Items []proAccountListItem `json:"items"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, item := range envelope.Data.Items {
		if item.Email == email {
			if item.StageProgress.Steps[stage] != want {
				t.Fatal("list projection missing current status")
			}
			return
		}
	}
	t.Fatal("account absent from list")
}

func TestProManualLoginAndOAuthFinalStates(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const email = "progress@example.com"
	st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{})
	st.UpdateMailAccountManagementScope(email, "pro")
	s := &Server{store: st, registrationJobs: map[string]map[string]any{}, oauthJobs: map[string]map[string]any{}}
	for _, failed := range []bool{true, false} {
		st.StartProManualStage(email, "login", "login-id")
		s.registrationJobs["login-id"] = map[string]any{"email": email, "pro_stage": "login", "logs": []any{}}
		assertProProgress(t, s, email, "login", "running")
		status, message, want := "success", "AT saved", "completed"
		if failed {
			status, message, want = "failed", "fixture login error", "failed"
		}
		s.finishRegistration("login-id", status, message)
		assertProProgress(t, s, email, "login", want)
		st.StartProManualStage(email, "oauth", "oauth-id")
		s.oauthJobs["oauth-id"] = map[string]any{"email": email, "status": "running"}
		assertProProgress(t, s, email, "oauth", "running")
		var runErr error
		if failed {
			runErr = errors.New("fixture oauth error")
		}
		s.finishMailAccountOAuthJob("oauth-id", email, map[string]any{"success": true, "access_token": "new-at", "refresh_token": "new-rt"}, runErr)
		assertProProgress(t, s, email, "oauth", want)
	}
}

func TestProManualPaymentProgressAndOldOrder(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/gpt-recharge/orders/status" {
			fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"manual-order","status":"success","planCode":"pro20"}]}}`)
			return
		}
		close(entered)
		<-release
		fmt.Fprint(w, `{"code":0,"data":{"id":"manual-order","status":"processing","planCode":"pro20"}}`)
	})
	defer once.Do(func() { close(release) })
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- payCall(t, s.createGPTPayOrder, "email", email, payInput("manual-progress-order")) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("payment not dispatched")
	}
	assertProProgress(t, s, email, "recharge", "running")
	once.Do(func() { close(release) })
	order := payOrder(t, <-result)
	assertProProgress(t, s, email, "recharge", "running")
	payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", order.ID, map[string]any{}))
	assertProProgress(t, s, email, "recharge", "completed")
	s.updateProPaymentStage(gptpay.Order{ID: "older", Email: email, CreatedAt: order.CreatedAt.Add(-time.Hour), Status: "failed", Error: "old failure"})
	assertProProgress(t, s, email, "recharge", "completed")
	p, _, _ := st.MailAccountCredential(email)
	if p.ProManualStages["recharge"].ID != order.ID {
		t.Fatal("old order replaced latest")
	}
}

func TestProManualMergeProgressUntilRemoved(t *testing.T) {
	f := newProExecutionFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(f.release) })
	w := f.request(f.s.mergeProAccount, "POST", "/merge", "")
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("merge not dispatched")
	}
	assertProProgress(t, f.s, f.email, "merge", "running")
	once.Do(func() { close(f.release) })
	f.waitMerge(t)
	assertProProgress(t, f.s, f.email, "merge", "completed")
	if w = f.stage("remove", "pending", "completed"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertProProgress(t, f.s, f.email, "merge", "pending")
}

func TestProWaitingQuotaAPIHidesLegacyNotice(t *testing.T) {
	f := newProExecutionFixture(t)
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "auto", Status: "waiting_quota", Stage: "quota", Error: "额度尚未达到阈值，等待下次检测", Steps: map[string]string{"quota": "waiting"}}
		p.ProManualStages = map[string]model.ProStageProgress{"quota": {StartedAt: time.Now(), Status: "completed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	w := f.request(f.s.getProAutomation, "GET", "/auto-pro", "")
	var response struct {
		Data model.ProAutoState `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.Error != "" || response.Data.Steps["quota"] != "waiting" {
		t.Fatalf("dialog: %s, err=%v", w.Body.String(), err)
	}
	w = f.request(f.s.listProAccounts, "GET", "/api/pro-accounts?page=1&page_size=10", "")
	var page struct {
		Data struct {
			Items []proAccountListItem `json:"items"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Data.Items) != 1 {
		t.Fatal("list response", err)
	}
	row := page.Data.Items[0]
	if row.StageProgress.Steps["quota"] != "waiting" || row.StageProgress.Errors["quota"] != "" || row.ProAuto.Error != "" {
		t.Fatal("list still shows a completed or erroneous quota wait")
	}
}

func TestProManualEarlyFailureAndBatch(t *testing.T) {
	f := newProExecutionFixture(t)
	f.s.store.SaveMailAccount(model.MailAccountProfile{Email: "second@example.com"}, model.MailAccountCredentials{})
	f.s.store.UpdateMailAccountManagementScope("second@example.com", "pro")
	results := f.s.runProBatch(t.Context(), []string{f.email, "second@example.com"}, func(ctx context.Context, email string) error {
		_, err := f.s.performProPush(ctx, email)
		return err
	})
	_ = results
	for _, email := range []string{f.email, "second@example.com"} {
		assertProProgress(t, f.s, email, "push", "failed")
	}
	if _, err := f.s.performProQuota(t.Context(), f.email, false); err == nil {
		t.Fatal("quota prerequisite should fail")
	}
	assertProProgress(t, f.s, f.email, "quota", "failed")
	r := httptest.NewRequest("POST", "/refresh", nil)
	r.SetPathValue("email", "second@example.com")
	w := httptest.NewRecorder()
	f.s.refreshProOAuth(w, r)
	if w.Code < 400 {
		t.Fatal("fixture refresh must fail")
	}
	assertProProgress(t, f.s, "second@example.com", "oauth", "failed")
}

func TestProManualPushAndQuotaRunningThenCompleted(t *testing.T) {
	for _, stage := range []string{"push", "quota"} {
		t.Run(stage, func(t *testing.T) {
			f := newProExecutionFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-session"}}`)
					return
				}
				close(entered)
				<-release
				if stage == "push" {
					fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"fixture"}}`)
				} else {
					fmt.Fprint(w, `{"code":0,"data":{"rate_limit":{"secondary_window":{"used_percent":50,"limit_window_seconds":604800}}}}`)
				}
			}))
			defer downstream.Close()
			defer once.Do(func() { close(release) })
			v, _, _, _ := f.s.store.ProSettings()
			v.Sub2.URL, v.Sub2.Email, v.Sub2.GroupIDs = downstream.URL, "admin@example.com", []int64{1}
			if _, err := f.s.store.SaveProSettings(v, "fixture-password", ""); err != nil {
				t.Fatal(err)
			}
			f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.PushProvider = "sub2" })
			done := make(chan error, 1)
			go func() {
				var err error
				if stage == "push" {
					_, err = f.s.performProPush(t.Context(), f.email)
				} else {
					_, err = f.s.performProQuota(t.Context(), f.email, false)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("downstream not called")
			}
			assertProProgress(t, f.s, f.email, stage, "running")
			once.Do(func() { close(release) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			assertProProgress(t, f.s, f.email, stage, "completed")
		})
	}
}

func TestProManualExistingPaymentDisplayUsesLatestOrder(t *testing.T) {
	f := newProExecutionFixture(t)
	now := time.Now()
	for _, order := range []gptpay.Order{
		{ID: "old-paid", Email: f.email, Status: "success", CreatedAt: now.Add(-time.Hour)},
		{ID: "latest-failed", Email: f.email, Status: "failed", Error: "latest failure", CreatedAt: now},
		{ID: "other-account", Email: "other@example.com", Status: "success", CreatedAt: now.Add(time.Hour)},
	} {
		if err := f.s.store.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
			t.Fatal(err)
		}
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	items, err := f.s.proAccountListWithCards([]model.MailAccountProfile{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].StageProgress.Steps["recharge"] != "failed" || items[0].StageProgress.Errors["recharge"] != "latest failure" {
		t.Fatal("stale success used for payment progress", items)
	}
	orders, err := f.s.store.ProLatestPaymentOrders([]string{f.email})
	if err != nil || len(orders) != 1 || orders[f.email].ID != "latest-failed" {
		t.Fatal("payment query exceeded requested accounts", err)
	}
}
