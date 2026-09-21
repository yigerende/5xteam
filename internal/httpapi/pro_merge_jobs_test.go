package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

func (f *proExecutionFixture) waitMerge(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { f.s.proMergeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("background merge did not finish")
	}
}

// Existing workflow assertions now wait for completion after the 202 receipt.
func (f *proExecutionFixture) mergeAndWait(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	out := f.request(f.s.mergeProAccount, "POST", "/merge", "")
	if out.Code != http.StatusAccepted {
		return out
	}
	f.waitMerge(t)
	p, _, err := f.s.store.MailAccountCredential(f.email)
	if err != nil {
		t.Fatal(err)
	}
	out = httptest.NewRecorder()
	if p.ProLastError != "" {
		writeAPI(out, 400, nil, p.ProLastError)
	} else {
		writeAPI(out, 200, p, "")
	}
	return out
}

func TestProMergeSurvivesBrowserDisconnect(t *testing.T) {
	f := newProExecutionFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(f.release) })
	ctx, cancel := context.WithCancel(t.Context())
	r := httptest.NewRequest("POST", "/merge", nil).WithContext(ctx)
	r.SetPathValue("email", f.email)
	out := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.s.mergeProAccount(out, r); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP response waits for remote workflow")
	}
	if out.Code != 202 || !strings.Contains(out.Body.String(), "\"pro_workflow_running\":true") {
		t.Fatal(out.Code, out.Body.String())
	}
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("remote transfer not reached")
	}
	cancel() // A page refresh / reverse proxy disconnect must not cancel this job.
	release.Do(func() { close(f.release) })
	f.waitMerge(t)
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if !p.SpaceMergedOnce || p.TransferStatus != "completed" || p.RemoveStatus != "completed" || p.ProLastError != "" || p.ProWorkflowRunning {
		t.Fatalf("incorrect final state: %+v", p)
	}
	for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
		if f.calls[stage] != 1 {
			t.Fatal("duplicate stage", stage)
		}
	}
}

func TestProTransferAmbiguousResponseDoesNotRetryOrRemove(t *testing.T) {
	for _, code := range []int{0, 200, 408, 500, 502, 504} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newProExecutionFixture(t)
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			v, _, _, _ := f.s.store.ProSettings()
			v.RetryCount = 3
			calls := 0
			err := f.s.runProMergeStep(t.Context(), p, v, "lost-response", "transfer", nil, func(context.Context) (workflow.Response, error) {
				calls++ // Simulate the remote side committing before the response is lost.
				return workflow.Response{StatusCode: code}, errors.New("response lost after remote commit")
			})
			if !errors.Is(err, errProTransferUncertain) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			p, _, _ = f.s.store.MailAccountCredential(f.email)
			if p.TransferStatus != "unknown" || p.SpaceMergedOnce || p.RemoveStatus == "completed" {
				t.Fatalf("unsafe state: %+v", p)
			}
			if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 400 {
				t.Fatal("uncertain merge resubmitted")
			}
			if _, err := f.s.performProMerge(t.Context(), f.email); !errors.Is(err, errProTransferUncertain) {
				t.Fatal("automatic path ignored uncertainty")
			}
			if len(f.calls) != 0 {
				t.Fatal("unknown result retried remotely")
			}
			if err := f.s.flushAuditEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			events, _ := f.s.store.ExecutionLogEvents(p.ID, "", "")
			found := false
			for _, e := range events {
				if e.Stage == "transfer" && e.ToStatus == "unknown" && e.Details["will_retry"] == false {
					found = true
				}
			}
			if !found {
				t.Fatal("missing uncertain-result diagnostics")
			}
			// User verifies actual completion: only the mother cleanup can follow.
			_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.InviteStatus = "completed"
				p.AcceptStatus = "completed"
				p.TargetAdminID = f.admin.ID
				p.TargetTeamID = "original-team"
			})
			if out := f.stage("transfer", "completed", "unknown"); out.Code != 200 {
				t.Fatal(out.Body.String())
			}
			if out := f.mergeAndWait(t); out.Code != 200 || f.calls["remove"] != 1 || f.calls["transfer"] != 0 {
				t.Fatal("correction repeated merge", out.Body.String(), f.calls)
			}
		})
	}
}

func TestProMergeCancellationBeforeSendIsNotAmbiguous(t *testing.T) {
	f := newProExecutionFixture(t)
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	v, _, _, _ := f.s.store.ProSettings()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	_ = f.s.runProMergeStep(ctx, p, v, "before-send", "transfer", nil, func(context.Context) (workflow.Response, error) { called = true; return workflow.Response{}, nil })
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if called || p.TransferStatus != "pending" || proTransferNeedsConfirmation(p) {
		t.Fatal("unsent operation marked ambiguous")
	}
}

func TestProMergeRemoteCommitWithLostResponse(t *testing.T) {
	f := newProExecutionFixture(t)
	f.loseTransferResponse = true
	v, _, _, _ := f.s.store.ProSettings()
	v.RetryCount = 3
	if _, err := f.s.store.SaveProSettings(v, "", ""); err != nil {
		t.Fatal(err)
	}
	if out := f.mergeAndWait(t); out.Code != 400 {
		t.Fatal("lost result reported as completed")
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if f.calls["transfer"] != 1 || f.calls["remove"] != 0 || p.TransferStatus != "unknown" || p.ProWorkflowRunning {
		t.Fatal("unsafe lost-response handling", f.calls, p.TransferStatus)
	}
}

func TestProMergeShutdownDuringTransferPreservesUncertainty(t *testing.T) {
	f := newProExecutionFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	defer close(f.release)
	if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer did not start")
	}
	f.s.stopProMergeJobs()
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.TransferStatus != "unknown" || p.ProWorkflowRunning || f.calls["remove"] != 0 {
		t.Fatal("shutdown discarded ambiguity", p.TransferStatus)
	}
}

func TestProMergeParallelDuplicateRequests(t *testing.T) {
	f := newProExecutionFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(f.release) })
	codes := make(chan int, 20)
	for range 20 {
		go func() { codes <- f.request(f.s.mergeProAccount, "POST", "/merge", "").Code }()
	}
	accepted := 0
	for range 20 {
		select {
		case code := <-codes:
			if code == 202 {
				accepted++
			} else if code != 409 {
				t.Fatal("unexpected response", code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("duplicate request blocked")
		}
	}
	if accepted != 1 {
		t.Fatal("multiple workers accepted", accepted)
	}
	release.Do(func() { close(f.release) })
	f.waitMerge(t)
	for stage, count := range f.calls {
		if count != 1 {
			t.Fatal("duplicate mutation", stage, count)
		}
	}
}

func TestProMergeShutdownCancelsQueuedLockWait(t *testing.T) {
	f := newProExecutionFixture(t)
	lock, _ := f.s.autoAdminLocks.LoadOrStore("original-team", &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()
	if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	done := make(chan struct{})
	go func() { f.s.stopProMergeJobs(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stuck waiting for mother lock")
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.ProWorkflowRunning || len(f.calls) != 0 {
		t.Fatal("queued job did not stop safely")
	}
	if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 400 {
		t.Fatal("accepted new job during shutdown")
	}
}

func TestProMergeQueuesAfterQuotaAccountLock(t *testing.T) {
	f := newProExecutionFixture(t)
	unlock := f.s.lockProAccount(f.email)
	p, err := f.s.startProMergeJob(f.email, nil)
	if err != nil || !p.ProWorkflowRunning {
		unlock()
		t.Fatal(err)
	}
	if _, err := f.s.startProMergeJob(f.email, nil); err == nil {
		unlock()
		t.Fatal("duplicate queue accepted")
	}
	unlock()
	f.waitMerge(t)
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if p.RemoveStatus != "completed" {
		t.Fatal("quota-triggered workflow did not complete")
	}
}

func TestProQuotaTriggersDetachedMergeAndPreservesUncertainty(t *testing.T) {
	for _, mode := range []string{"exhausted", "available", "uncertain", "legacy", "custom_reached", "custom_below"} {
		t.Run(mode, func(t *testing.T) {
			f := newProExecutionFixture(t)
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-session"}}`)
				case "/api/v1/admin/openai/accounts/42/quota":
					used := 100
					if mode == "available" {
						used = 50
					}
					if mode == "custom_reached" {
						used = 85
					}
					if mode == "custom_below" {
						used = 84
					}
					fmt.Fprintf(w, `{"code":0,"data":{"rate_limit":{"secondary_window":{"used_percent":%d,"limit_window_seconds":604800}}}}`, used)
				default:
					t.Errorf("unexpected quota request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer downstream.Close()
			v, _, _, _ := f.s.store.ProSettings()
			v.Provider = "sub2"
			v.AutoMergeEnabled = true
			if mode == "custom_reached" || mode == "custom_below" {
				v.QuotaUsedThreshold = 85
			}
			v.Sub2.URL = downstream.URL
			v.Sub2.Email = "admin@example.com"
			if _, err := f.s.store.SaveProSettings(v, "fixture-password", ""); err != nil {
				t.Fatal(err)
			}
			_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.PushProvider = "sub2"
				if mode == "uncertain" {
					p.TransferStatus = "unknown"
					p.ProLastError = errProTransferUncertain.Error()
				}
				if mode == "legacy" {
					p.TransferStatus = "failed"
					p.ProLastError = "context canceled"
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			r := httptest.NewRequest("POST", "/quota", nil).WithContext(ctx)
			r.SetPathValue("email", f.email)
			out := httptest.NewRecorder()
			f.s.checkProAccountQuota(out, r)
			cancel()
			if out.Code != 200 {
				t.Fatal(out.Body.String())
			}
			f.waitMerge(t)
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if mode == "exhausted" || mode == "custom_reached" {
				if p.RemoveStatus != "completed" || f.calls["transfer"] != 1 {
					t.Fatal("quota job cancelled with request", p.ProLastError, f.calls)
				}
			} else {
				if len(f.calls) != 0 {
					t.Fatal("unexpected merge", f.calls)
				}
				if mode != "available" && mode != "custom_below" && (p.TransferStatus != "unknown" || p.ProLastError == "") {
					t.Fatal("quota refresh erased uncertainty")
				}
			}
		})
	}
}

func TestProMergeConcurrencyLimitAndCancellation(t *testing.T) {
	f := newProExecutionFixture(t)
	v, _, _, _ := f.s.store.ProSettings()
	v.Concurrency = 1
	if _, err := f.s.store.SaveProSettings(v, "", ""); err != nil {
		t.Fatal(err)
	}
	release, err := f.s.proMergeSlot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if unexpected, err := f.s.proMergeSlot(ctx); err == nil {
		unexpected()
		t.Fatal("exceeded configured concurrency")
	}
}

func TestProMergeRestartAndLegacyCancelledTransferNeedConfirmation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := newProExecutionFixture(t)
		_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
			p.InviteStatus = "completed"
			p.AcceptStatus = "completed"
			p.TransferStatus = "running"
			p.ProWorkflowRunning = true
			if legacy {
				p.TransferStatus = "failed"
				p.ProLastError = "context canceled"
				p.ProWorkflowRunning = false
			}
		})
		if err := f.s.store.RecoverProWorkflows(); err != nil {
			t.Fatal(err)
		}
		if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 400 {
			t.Fatal("ambiguous transfer restarted")
		}
		p, _, _ := f.s.store.MailAccountCredential(f.email)
		if p.TransferStatus != "unknown" || p.ProWorkflowRunning || len(f.calls) != 0 {
			t.Fatal("unsafe recovery")
		}
		// Explicit operator confirmation that it did NOT merge permits a retry.
		if out := f.stage("transfer", "pending", "unknown"); out.Code != 200 {
			t.Fatal(out.Body.String())
		}
		if out := f.mergeAndWait(t); out.Code != 200 || f.calls["transfer"] != 1 {
			t.Fatal("manual retry failed", out.Body.String())
		}
	}
}
