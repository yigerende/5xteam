package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func mailActionFixture(t *testing.T, mode string) *proExecutionFixture {
	t.Helper()
	f := newProExecutionFixture(t)
	f.s.stopProAutomations()
	cfg, _, _, _ := f.s.store.ProSettings()
	cfg.AutoMergeEnabled, cfg.AutoDeleteEnabled = mode == "merge", mode == "deactivate"
	cfg.AutoMerchantEnabled, cfg.AutoMergeV2Enabled = mode == "merchant", mode == "merge_v2"
	cfg.PostMergeGroupIDs = []int64{123}
	if _, err := f.s.store.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{}
		p.Sub2AccountID, p.PushStatus, p.PushProvider, p.CPAAuthFileName = 0, "", "", ""
	}); err != nil {
		t.Fatal(err)
	}
	p, c, _ := f.s.store.MailAccountCredential(f.email)
	c.GptPassword, c.TotpSecret = "fixture-password", "JBSWY3DPEHPK3PXP"
	if _, err := f.s.store.SaveMailAccount(p, c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.UpdateMailAccountManagementScope(f.email, "mail"); err != nil {
		t.Fatal(err)
	}
	return f
}

func assertMailOnly(t *testing.T, f *proExecutionFixture) model.MailAccountProfile {
	t.Helper()
	p, _, err := f.s.store.MailAccountCredential(f.email)
	if err != nil || p.ManagementScope != "mail" || p.ProManagedAt != nil || p.ProAuto.ID != "" || p.ProAuto.OrderID != "" || p.PushStatus != "" || p.ProRestockTaskID != "" {
		t.Fatalf("mailbox entered provisioning/Pro: err=%v scope=%s auto=%+v", err, p.ManagementScope, p.ProAuto)
	}
	if len(f.s.store.MailAccountsByManagementScope("pro")) != 0 || len(f.s.store.MailAccountsByManagementScope("mail")) != 1 || len(f.s.store.FreeAccounts()) != 0 {
		t.Fatal("account moved or created a rotation record")
	}
	return p
}

func TestMailFinalStepAllModesUseExistingWorkers(t *testing.T) {
	for _, mode := range model.ProDispositionModes {
		t.Run(mode, func(t *testing.T) {
			f := mailActionFixture(t, mode)
			var submissions atomic.Int32
			deletion := "confirmed"
			f.s.proDeleteHooks = &proDeactivationHooks{Login: deletionLoginFixture, Protocol: deletionProtocolFixture(t, &submissions, &deletion)}
			f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
				submissions.Add(1)
				return mockMergeV2Submit(ctx)
			}
			merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					submissions.Add(1)
					fmt.Fprintf(w, `{"items":[{"id":"job","email":%q,"status":"queued"}]}`, f.email)
					return
				}
				fmt.Fprintf(w, `{"id":"job","email":%q,"status":"succeeded"}`, f.email)
			}))
			defer merchant.Close()
			f.s.proMerchantClient = &refundmerchant.Client{BaseURL: merchant.URL}
			if mode == "merchant" {
				cfg, _, _, _ := f.s.store.ProSettings()
				if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, "", "fixture-merchant-key"); err != nil {
					t.Fatal(err)
				}
			}
			w := f.request(f.s.startMailFinalStep, "POST", "/final-step", fmt.Sprintf(`{"mode":%q}`, mode))
			if w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			f.s.proMergeWG.Wait()
			p := assertMailOnly(t, f)
			if p.ProLastError != "" || p.ProWorkflowRunning {
				t.Fatal(p.ProLastError, p.ProWorkflowRunning)
			}
			if mode == "merchant" || mode == "deactivate" {
				w = f.request(f.s.startMailFinalStep, "POST", "/final-step", fmt.Sprintf(`{"mode":%q}`, mode))
				if w.Code != 202 {
					t.Fatal(w.Code, w.Body.String())
				}
				f.s.proMergeWG.Wait()
				p = assertMailOnly(t, f)
			}
			if !p.OAIConfirmationPrerequisite(mode) {
				t.Fatalf("not complete: %s %s", mode, p.ProLastError)
			}
			if p.ProPostMergeGroups != nil {
				t.Fatal("external account tried to update a nonexistent Sub2 account")
			}
			if mode != "merge" && submissions.Load() != 1 {
				t.Fatal("duplicate submission", submissions.Load())
			}
			w = f.request(f.s.startMailFinalStep, "POST", "/final-step", fmt.Sprintf(`{"mode":%q}`, mode))
			if w.Code != 409 {
				t.Fatal("completed task replayed", w.Code)
			}
			w = f.request(f.s.mailProActionState, "GET", "/pro-actions", "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"final_completed":true`) || strings.Contains(w.Body.String(), "fixture-password") || strings.Contains(w.Body.String(), "child-token") {
				t.Fatal("bad projection", w.Body.String())
			}
		})
	}
}

func TestMailActionsImportedAliasUsesConfiguredFinalMode(t *testing.T) {
	f := mailActionFixture(t, "merge_v2")
	const email = "external+alias@example.com"
	f.email = email // The simulated team roster must identify this new mailbox.
	if _, _, err := f.s.store.ImportMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{
		Email: email, GptPassword: "fixture-password", TotpSecret: "JBSWY3DPEHPK3PXP",
	}, false); err != nil {
		t.Fatal(err)
	}
	// Use the actual authenticated router, including the browser's encoded +.
	handler := f.s.Handler()
	cookie := &http.Cookie{Name: authCookieName, Value: f.s.createSession()}
	request := func(method, suffix, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/mail/accounts/"+url.QueryEscape(email)+suffix, strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/pro-actions", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"merge_v2"`) || !strings.Contains(w.Body.String(), email) {
		t.Fatal("fresh mail import not found by action route", w.Code, w.Body.String())
	}
	w = request("POST", "/oai-confirmation", `{"mode":"merge_v2","action":"retry"}`)
	if w.Code != 200 {
		t.Fatal("standalone external OAI rejected", w.Code, w.Body.String())
	}
	p, _, err := f.s.store.MailAccountCredential(email)
	if err != nil || p.DispositionStarted() || p.ProOAIConfirmations["merge_v2"] == nil || !p.ProOAIConfirmations["merge_v2"].Manual {
		t.Fatal("independent OAI needs or fabricates a completed final step")
	}
	if w = request("POST", "/oai-confirmation", `{"mode":"merge_v2","action":"stop"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.s.proMergeV2Login = func(ctx context.Context, gotEmail string) (map[string]any, error) {
		if gotEmail != email {
			t.Errorf("alias was altered: %q", gotEmail)
		}
		return mockMergeV2Submit(ctx)
	}
	w = request("POST", "/final-step", `{"mode":"merge_v2"}`)
	if w.Code != 202 {
		t.Fatal("configured final step rejected", w.Code, w.Body.String())
	}
	f.s.proMergeWG.Wait()
	p, _, err = f.s.store.MailAccountCredential(email)
	if err != nil || !p.MergeV2Completed() || p.ManagementScope != "mail" || p.ProManagedAt != nil || p.ProAuto.OrderID != "" || p.PushStatus != "" {
		t.Fatalf("external final step did not finish in mail scope: %v %s", err, p.ProLastError)
	}
}

func TestMailFinalStepNoSwitchStaleSwitchAndConcurrentClicks(t *testing.T) {
	f := mailActionFixture(t, "")
	if w := f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge"}`); w.Code != 409 {
		t.Fatal(w.Code)
	}
	cfg, _, _, _ := f.s.store.ProSettings()
	cfg.AutoMergeV2Enabled = true
	if _, err := f.s.store.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	if w := f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge"}`); w.Code != 409 {
		t.Fatal("stale switch accepted", w.Code)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		close(entered)
		<-release
		return mockMergeV2Submit(ctx)
	}
	if w := f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge_v2"}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	<-entered
	w := f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge_v2"}`)
	if w.Code != 409 {
		t.Error("duplicate admission", w.Code)
	}
	if err := f.s.store.DeleteMailAccount(f.email); err == nil {
		t.Error("deleted running mailbox")
	}
	if _, err := f.s.store.UpdateMailAccountManagementScope(f.email, "pro"); err == nil {
		t.Error("moved running mailbox")
	}
	close(release)
	f.s.proMergeWG.Wait()
	assertMailOnly(t, f)
}

func TestMailOAIStandaloneNoFabricatedCompletionAndRestart(t *testing.T) {
	f := mailActionFixture(t, "merge_v2")
	w := f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merge_v2","action":"retry"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := assertMailOnly(t, f)
	v := p.ProOAIConfirmations["merge_v2"]
	if !v.Manual || v.RequestedAt == nil || !v.CompletedAt.IsZero() || p.DispositionStarted() || !v.Due(time.Now()) {
		t.Fatal("standalone confirmation fabricated business completion")
	}
	if w = f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge_v2"}`); w.Code != 409 {
		t.Fatal("started final while OAI was queued")
	}
	items, err := f.s.store.ProOAIWork(time.Now(), false)
	if err != nil || len(items) != 1 || !items[0].Manual {
		t.Fatal("mail OAI absent from dispatcher", items, err)
	}
	f.s.proOAILogin = oaiHook(t, "该订阅的退款已处理。\n已成功")
	if err = f.s.runProOAI(t.Context(), f.email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	p = assertMailOnly(t, f)
	v = p.ProOAIConfirmations["merge_v2"]
	if v.Status != "confirmed" || v.Answer == "" || p.DispositionStarted() {
		t.Fatal("bad independent outcome")
	}
	if w = f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merge_v2","action":"restart"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if !p.ProOAIConfirmations["merge_v2"].FreshLogin || len(p.ProOAIConfirmations["merge_v2"].Replies) != 1 {
		t.Fatal("restart lost history")
	}
	if w = f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merge_v2","action":"stop"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	items, err = f.s.store.ProOAIWork(time.Now(), false)
	if err != nil || len(items) != 0 {
		t.Fatal("stopped inquiry still scheduled")
	}
}

func TestMailActionsAuthenticationAndImportPreservesCheckpoint(t *testing.T) {
	f := mailActionFixture(t, "merge_v2")
	for _, path := range []string{"pro-actions", "final-step", "final-step/stop", "oai-confirmation", "events", "events/export"} {
		method := "POST"
		if path == "pro-actions" || strings.HasPrefix(path, "events") {
			method = "GET"
		}
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/api/mail/accounts/"+f.email+"/"+path, nil))
		if w.Code != 401 {
			t.Fatal("anonymous access", path, w.Code)
		}
	}
	if _, err := f.s.store.EnableMailProActions(f.email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProWorkflowRunning = true
		p.ProMergeV2 = &model.ProMergeV2{Status: "running", Stage: "submit", SubmittedAt: &now, Steps: map[string]string{"submit": "running"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.RecoverProWorkflows(); err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.ProWorkflowRunning || p.ProMergeV2.Status != "unknown" || p.ProMergeV2.SubmittedAt == nil {
		t.Fatal("restart lost unknown checkpoint")
	}
	before, _ := json.Marshal(p.ProMergeV2)
	if _, err := f.s.store.SaveMailAccount(model.MailAccountProfile{Email: f.email}, model.MailAccountCredentials{Email: f.email, GptPassword: "updated-fixture"}); err != nil {
		t.Fatal(err)
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	after, _ := json.Marshal(p.ProMergeV2)
	if string(before) != string(after) || p.MailProActions.FinalMode != "merge_v2" {
		t.Fatal("re-import lost workflow")
	}
	cfg, _, _, _ := f.s.store.ProSettings()
	cfg.AutoMergeV2Enabled, cfg.AutoDeleteEnabled = false, true
	if _, err := f.s.store.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	if mailFinalMode(p, cfg) != "merge_v2" {
		t.Fatal("inflight mode changed with global setting")
	}
	if w := f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge_v2"}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.s.proMergeWG.Wait()
	p = assertMailOnly(t, f)
	if !strings.Contains(p.ProLastError, "待确认") || len(f.calls) != 0 {
		t.Fatal("unknown request replayed", p.ProLastError, f.calls)
	}
}

func TestMailOAIStoppedFinalRemainsStoppedAndNewCompletionRechecks(t *testing.T) {
	f := mailActionFixture(t, "merge_v2")
	if _, err := f.s.store.EnableMailProActions(f.email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	if w := f.request(f.s.stopMailFinalStep, "POST", "/final-step/stop", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merge_v2","action":"retry"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	items, err := f.s.store.ProOAIWork(time.Now(), false)
	if err != nil || len(items) != 1 || items[0].Blocked {
		t.Fatal("standalone query blocked by final stop", items, err)
	}
	if err := f.s.store.DeleteMailAccount(f.email); err == nil {
		t.Fatal("deleted queued independent inquiry")
	}
	f.s.proOAILogin = oaiHook(t, "退款已处理。\n已成功")
	f.s.dispatchProOAI(t.Context())
	f.s.proAutoWG.Wait()
	p := assertMailOnly(t, f)
	if !p.ProAuto.StopRequested || p.ProOAIConfirmations["merge_v2"].Status != "confirmed" {
		t.Fatal("inquiry resumed final step or failed", p.ProOAIConfirmations)
	}
	// A later actual final step must get its own delayed confirmation.
	cfg, _, _, _ := f.s.store.ProSettings()
	rule := cfg.OAIConfirmation["merge_v2"]
	rule.Enabled, rule.DelaySeconds = true, 300
	cfg.OAIConfirmation["merge_v2"] = rule
	if _, err := f.s.store.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) { return mockMergeV2Submit(ctx) }
	if w := f.request(f.s.startMailFinalStep, "POST", "/final-step", `{"mode":"merge_v2"}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.s.proMergeWG.Wait()
	p = assertMailOnly(t, f)
	v := p.ProOAIConfirmations["merge_v2"]
	if !p.MergeV2Completed() || v.CompletedAt.IsZero() || v.RequestedAt != nil || v.Manual || v.Status != "waiting" || v.NotBefore == nil || v.NotBefore.Sub(v.CompletedAt) != 300*time.Second || len(v.Replies) != 1 {
		t.Fatalf("old inquiry reused for a new outcome: %+v", v)
	}
}

func TestMailOAIAdmissionAndLoginSerialization(t *testing.T) {
	f := mailActionFixture(t, "merge_v2")
	if w := f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merchant","action":"retry"}`); w.Code != 409 {
		t.Fatal("wrong confirmation mode admitted", w.Code)
	}
	p, c, _ := f.s.store.MailAccountCredential(f.email)
	c.TotpSecret = ""
	if _, err := f.s.store.SaveMailAccount(p, c); err != nil {
		t.Fatal(err)
	}
	if w := f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merge_v2","action":"retry"}`); w.Code != 409 {
		t.Fatal("missing credentials admitted", w.Code)
	}
	c.TotpSecret = "JBSWY3DPEHPK3PXP"
	if _, err := f.s.store.SaveMailAccount(p, c); err != nil {
		t.Fatal(err)
	}
	if w := f.request(f.s.manageMailOAI, "POST", "/oai-confirmation", `{"mode":"merge_v2","action":"retry"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.s.oauthMu.Lock()
	f.s.oauthJobs["mail-login-fixture"] = map[string]any{"email": f.email, "status": "running"}
	f.s.oauthMu.Unlock()
	var calls atomic.Int32
	f.s.proOAILogin = func(context.Context, string) (map[string]any, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected login")
	}
	f.s.dispatchProOAI(t.Context())
	f.s.proAutoWG.Wait()
	if calls.Load() != 0 {
		t.Fatal("OAI overlapped mail login")
	}
	f.s.oauthMu.Lock()
	delete(f.s.oauthJobs, "mail-login-fixture")
	f.s.oauthMu.Unlock()
	value, _ := f.s.proLocks.LoadOrStore("account:"+f.email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	guard := f.s.guardProAutomation(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("mail login bypassed account lock") }))
	w := httptest.NewRecorder()
	guard.ServeHTTP(w, httptest.NewRequest("POST", "/api/mail/accounts/"+f.email+"/oauth", nil))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}
