package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

type proExecutionFixture struct {
	s                    *Server
	email                string
	admin                model.AdminAccountProfile
	mu                   sync.Mutex
	calls                map[string]int
	failStage            string
	failCount            int
	loseTransferResponse bool
	entered, release     chan struct{}
}

func newProExecutionFixture(t *testing.T) *proExecutionFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	f := &proExecutionFixture{s: s, email: "pro-log@example.com", calls: map[string]int{}}
	handler := func(mother bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			stage := ""
			switch {
			case r.Method == "DELETE":
				stage = "remove"
			case strings.HasSuffix(r.URL.Path, "/invites/accept"):
				stage = "accept"
			case strings.HasSuffix(r.URL.Path, "/invites"):
				stage = "invite"
			case r.URL.Path == "/accounts/transfer":
				stage = "transfer"
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				http.Error(w, "unexpected", 500)
				return
			}
			if mother != (stage == "invite" || stage == "remove") {
				t.Error("wrong dedicated/global proxy")
			}
			wantToken := "Bearer child-token"
			if mother {
				wantToken = "Bearer mother-token"
			}
			if r.Header.Get("Authorization") != wantToken || r.Header.Get("chatgpt-account-id") != "original-team" {
				t.Error("wrong credential/team")
			}
			f.mu.Lock()
			f.calls[stage]++
			reject := f.failStage == stage && f.calls[stage] <= f.failCount
			f.mu.Unlock()
			if stage == "transfer" && f.entered != nil {
				close(f.entered)
				select {
				case <-f.release:
				case <-r.Context().Done():
				}
			}
			if stage == "transfer" && f.loseTransferResponse {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if reject {
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":{"code":"rate_limited","message":"fixture remote detail","access_token":"private-response-token"}}`)
				return
			}
			fmt.Fprint(w, `{"success":true}`)
		}
	}
	mother := httptest.NewServer(handler(true))
	t.Cleanup(mother.Close)
	child := httptest.NewServer(handler(false))
	t.Cleanup(child.Close)
	settings := model.DefaultSettings()
	settings.BaseURL, settings.ProxyURL = mother.URL, child.URL
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	f.admin = saveTestAdmin(t, st, model.AdminAccountProfile{Label: "pro-mother", TeamAccountID: "original-team"}, "mother-token", mother.URL)
	p := model.DefaultProSettings()
	p.TargetAdminID, p.RetryCount, p.RetryIntervalSeconds = f.admin.ID, 0, 1
	if _, err := st.SaveProSettings(p, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: f.email}, model.MailAccountCredentials{Email: f.email, AccessToken: "child-token", RefreshToken: "child-rt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateMailAccountManagementScope(f.email, "pro"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.OAuthUserID = "pro-user"
		p.OAuthAccountID = "personal"
		p.Sub2AccountID = 42
		p.CPAAuthFileName = "pro.json"
		p.PushStatus = "completed"
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *proExecutionFixture) request(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("email", f.email)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func (f *proExecutionFixture) stage(stage, status, expected string) *httptest.ResponseRecorder {
	return f.request(f.s.updateProAccountStage, "PUT", "/stage", fmt.Sprintf(`{"stage":%q,"status":%q,"expected_status":%q}`, stage, status, expected))
}

func TestProExecutionLogsAndManualRecovery(t *testing.T) {
	for _, failedStage := range []string{"invite", "accept", "transfer", "remove"} {
		t.Run(failedStage, func(t *testing.T) {
			f := newProExecutionFixture(t)
			f.failStage, f.failCount = failedStage, 99
			out := f.mergeAndWait(t)
			if out.Code != 400 {
				t.Fatalf("wanted failed merge: %d %s", out.Code, out.Body.String())
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if proStageStatus(p, failedStage) != "failed" || p.ProWorkflowRunning {
				t.Fatalf("wrong failed state %+v", p)
			}
			// The operator has verified success remotely. Correct only that stage.
			out = f.stage(failedStage, "completed", "failed")
			if out.Code != 200 {
				t.Fatal(out.Body.String())
			}
			if failedStage == "transfer" {
				// Lost child RT/AT cannot block a mother-only cleanup. Changing the
				// default mother also must not redirect an already-started workflow.
				p, _, _ := f.s.store.MailAccountCredential(f.email)
				if !p.SpaceMergedOnce || p.SpaceMergedAt == nil {
					t.Fatal("manual merge completion not persisted")
				}
				_, err := f.s.store.SaveMailAccount(p, model.MailAccountCredentials{Email: f.email, AccessToken: "expired-child-token"})
				if err != nil {
					t.Fatal(err)
				}
				settings, _, _, _ := f.s.store.ProSettings()
				settings.TargetAdminID = "different-mother"
				if _, err = f.s.store.SaveProSettings(settings, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			out = f.mergeAndWait(t)
			if out.Code != 200 {
				t.Fatalf("resume failed %d %s", out.Code, out.Body.String())
			}
			p, _, _ = f.s.store.MailAccountCredential(f.email)
			for _, step := range []string{"invite", "accept", "transfer", "remove"} {
				if f.calls[step] != 1 || proStageStatus(p, step) != "completed" {
					t.Fatalf("repeated/skipped wrong stage %s calls=%v state=%+v", step, f.calls, p)
				}
			}
			if p.Sub2AccountID != 42 || p.CPAAuthFileName != "pro.json" || p.PushStatus != "completed" {
				t.Fatal("removed downstream account")
			}
			exported := f.request(f.s.exportProAccountEvents, "GET", "/events/export", "")
			if exported.Code != 200 {
				t.Fatal(exported.Body.String())
			}
			var payload executionLogExport
			if err := json.Unmarshal(exported.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if !payload.Coverage.FlushCompleted || payload.ProAccount == nil || payload.Retention != "48h" {
				t.Fatal("missing flush/snapshot/retention")
			}
			failed, edited, requests := false, false, 0
			for i, e := range payload.Events {
				if i > 0 && e.CreatedAt.After(payload.Events[i-1].CreatedAt) {
					t.Fatal("not descending")
				}
				if e.Type == "manual_stage" && e.FromStatus == "failed" && e.ToStatus == "completed" {
					edited = true
				}
				if e.Type != "pro_request" {
					continue
				}
				requests++
				proxy, _ := e.Details["proxy"].(map[string]any)
				want := "global"
				if e.Stage == "invite" || e.Stage == "remove" {
					want = "admin_dedicated"
				}
				if proxy["source"] != want || e.Request["url"] == nil || e.Request["method"] == nil || e.Details["execution_id"] == "" {
					t.Fatal("missing request context", e)
				}
				if e.HTTPStatus == 429 && e.Stage == failedStage && e.Attempt == 1 {
					body, _ := json.Marshal(e.Response)
					failed = strings.Contains(string(body), "rate_limited") && strings.Contains(string(body), "fixture remote detail")
				}
			}
			if !failed || !edited || requests != 8 {
				t.Fatalf("missing diagnostic events failed=%v edited=%v requests=%d", failed, edited, requests)
			}
			for _, secret := range []string{"mother-token", "child-token", "child-rt", "private-response-token"} {
				if strings.Contains(exported.Body.String(), secret) {
					t.Fatalf("secret leaked: %s", secret)
				}
			}
			// Account-scoped, cursor-backed timeline can load every record without truncation.
			listed := f.request(f.s.listProAccountEvents, "GET", "/events?limit=2", "")
			var page struct {
				Data struct {
					Events []model.AutoRotationEvent `json:"events"`
					Cursor string                    `json:"next_cursor"`
					More   bool                      `json:"has_more"`
				} `json:"data"`
			}
			if json.Unmarshal(listed.Body.Bytes(), &page) != nil || len(page.Data.Events) != 2 || !page.Data.More {
				t.Fatal("bad log page")
			}
			second := f.request(f.s.listProAccountEvents, "GET", "/events?limit=2&cursor="+page.Data.Cursor, "")
			if second.Code != 200 || strings.Contains(second.Body.String(), page.Data.Events[0].ID) {
				t.Fatal("cursor duplicated an event")
			}
		})
	}
}

func TestProExecutionRetryDiagnostics(t *testing.T) {
	f := newProExecutionFixture(t)
	f.failStage, f.failCount = "transfer", 1
	v, _, _, _ := f.s.store.ProSettings()
	v.RetryCount = 1
	if _, err := f.s.store.SaveProSettings(v, "", ""); err != nil {
		t.Fatal(err)
	}
	if out := f.mergeAndWait(t); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if f.calls["transfer"] != 2 {
		t.Fatal("missing retry")
	}
	if err := f.s.flushAuditEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	events, err := f.s.store.ExecutionLogEvents(p.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	retrying, success := false, false
	for _, e := range events {
		if e.Stage == "transfer" && e.Details["will_retry"] == true {
			retrying = true
		}
		if e.Stage == "transfer" && e.Attempt == 2 && e.HTTPStatus == 200 {
			success = true
		}
	}
	if !retrying || !success {
		t.Fatal("retry count/result not logged")
	}
}

func TestProStageValidationAndConcurrency(t *testing.T) {
	f := newProExecutionFixture(t)
	for _, test := range []struct{ stage, status string }{{"oauth", "completed"}, {"transfer", "running"}, {"", "pending"}} {
		if out := f.stage(test.stage, test.status, ""); out.Code != 400 {
			t.Fatal("invalid update accepted")
		}
	}
	if out := f.stage("transfer", "completed", "failed"); out.Code != 409 {
		t.Fatal("stale state overwritten")
	}
	unlock := f.s.lockProAccount(f.email)
	if out := f.stage("transfer", "completed", ""); out.Code != 409 {
		t.Fatal("active job was modified")
	}
	unlock()
	_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProWorkflowRunning = true; p.TransferStatus = "running" })
	if out := f.stage("transfer", "completed", "running"); out.Code != 200 {
		t.Fatal("stale running flag not recoverable", out.Body.String())
	}
	if out := f.stage("transfer", "pending", "completed"); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if !p.SpaceMergedOnce || p.SpaceMergedAt == nil || p.ProWorkflowRunning {
		t.Fatal("historical success erased or stale flag kept")
	}
	if len(f.calls) != 0 {
		t.Fatal("stage editor sent network request")
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?cursor=invalid"} {
		if out := f.request(f.s.listProAccountEvents, "GET", "/events"+query, ""); out.Code != 400 {
			t.Fatal("invalid cursor accepted")
		}
	}
	f.email = "missing@example.com"
	if out := f.stage("transfer", "completed", ""); out.Code != 404 {
		t.Fatal("missing account accepted")
	}
}

func TestProStageRejectsEditsDuringActualMerge(t *testing.T) {
	f := newProExecutionFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(f.release) })
	if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("merge did not start")
	}
	if out := f.stage("transfer", "completed", "running"); out.Code != 409 {
		t.Fatal("concurrent edit accepted")
	}
	if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 409 {
		t.Fatal("duplicate merge accepted")
	}
	release.Do(func() { close(f.release) })
	f.waitMerge(t)
	for stage, count := range f.calls {
		if count != 1 {
			t.Fatalf("duplicate %s: %d", stage, count)
		}
	}
}

func TestProStageAllEditableStatuses(t *testing.T) {
	f := newProExecutionFixture(t)
	for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
		previous := ""
		for _, status := range []string{"not_started", "pending", "failed", "completed", "pending"} {
			if out := f.stage(stage, status, previous); out.Code != 200 {
				t.Fatalf("%s/%s: %s", stage, status, out.Body.String())
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if proStageStatus(p, stage) != status {
				t.Fatal("status not persisted")
			}
			previous = status
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("status edit contacted remote")
	}
}

func TestProPreparationFailureIsLogged(t *testing.T) {
	f := newProExecutionFixture(t)
	if _, err := f.s.store.UpdateAdminAccountProxy(f.admin.ID, ""); err != nil {
		t.Fatal(err)
	}
	out := f.mergeAndWait(t)
	if out.Code != 400 {
		t.Fatal("missing proxy accepted")
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.ProLastError == "" || len(f.calls) != 0 {
		t.Fatal("preparation error not saved, or remote contacted")
	}
	if err := f.s.flushAuditEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.store.ExecutionLogEvents(p.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].ToStatus != "failed" || events[0].Details["error"] == nil {
		t.Fatal("preparation failure not in timeline")
	}
}

func TestProRequestCancellationIsLogged(t *testing.T) {
	f := newProExecutionFixture(t)
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	v, _, _, _ := f.s.store.ProSettings()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := f.s.runProMergeStep(ctx, p, v, "cancelled", "transfer", nil, func(ctx context.Context) (workflow.Response, error) { return workflow.Response{}, ctx.Err() })
	if err == nil {
		t.Fatal("cancelled step succeeded")
	}
	if err = f.s.flushAuditEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.store.ExecutionLogEvents(p.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Stage == "transfer" && e.ToStatus == "failed" && e.Details["will_retry"] == false {
			found = true
		}
	}
	if !found {
		t.Fatal("cancellation not recorded")
	}
}
