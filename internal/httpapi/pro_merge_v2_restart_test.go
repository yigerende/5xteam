package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func requestMergeV2Restart(t *testing.T, f *proExecutionFixture, id string) {
	t.Helper()
	w := f.request(f.s.guardProAutomation(http.HandlerFunc(f.s.restartProMergeV2)).ServeHTTP, "POST", "/api/pro-accounts/"+f.email+"/merge-v2-restart", fmt.Sprintf(`{"request_id":%q}`, id))
	if w.Code != 202 {
		t.Fatalf("restart: %d %s", w.Code, w.Body.String())
	}
}

func TestProMergeV2RestartArchivesAndRunsFromInvite(t *testing.T) {
	for _, status := range []string{"completed", "exhausted", "stopped", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := mergeV2Fixture(t)
			old := time.Now().Add(-time.Hour)
			before, credentials, _ := f.s.store.MailAccountCredential(f.email)
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.ProMergeV2 = &model.ProMergeV2{Status: "failed", Stage: "login", AdminID: f.admin.ID, TeamID: "original-team", Steps: map[string]string{"invite": "completed", "remove": "completed"}, StartedAt: old}
				switch status {
				case "completed":
					p.ProMergeV2.AcceptedAt, p.ProMergeV2.CompletedAt = &old, &old
				case "unknown":
					p.ProMergeV2.SubmittedAt = &old
					p.ProMergeV2.VerificationOnly = true
				case "exhausted":
					p.ProMergeV2.Recovery = &model.ProMergeV2Recovery{Exhausted: true, Attempts: 9, Phase: "cleanup"}
				case "stopped":
					p.ProAuto.StopRequested = true
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			logins := 0
			f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) { logins++; return mockMergeV2Submit(ctx) }
			requestMergeV2Restart(t, f, "request-0001")
			requestMergeV2Restart(t, f, "request-0001")
			if err := f.s.store.RecoverProWorkflows(); err != nil {
				t.Fatal(err)
			}
			pending, err := f.s.store.PendingProMergeV2Restarts()
			if err != nil || len(pending) != 1 {
				t.Fatal("restart lost across recovery", pending, err)
			}
			f.s.tickProMergeV2Restarts(t.Context())
			f.s.proMergeWG.Wait()
			p, c, err := f.s.store.MailAccountCredential(f.email)
			if err != nil || !p.MergeV2Completed() || p.ProMergeV2Restart.Status != "started" || !p.ProMergeV2.StartedAt.After(old) || p.ProMergeV2.Recovery.Exhausted {
				t.Fatal(p.ProMergeV2, p.ProMergeV2Restart, err)
			}
			if logins != 1 || f.calls["invite"] != 1 || f.calls["remove"] != 1 {
				t.Fatal("did not rerun from invitation", logins, f.calls)
			}
			if c.AccessToken != credentials.AccessToken || c.RefreshToken != credentials.RefreshToken || p.ProAuto.OrderID != before.ProAuto.OrderID {
				t.Fatal("payment/login credentials changed")
			}
			archived := false
			for _, e := range f.s.store.AutoRotationEventsByAccount(p.ID) {
				if e.Type == "pro_merge_v2_restart" && e.ID == "merge-v2-restart:"+p.ID+":request-0001:archive" {
					archived = e.Details["previous_merge_v2"] != nil
				}
			}
			if !archived {
				t.Fatal("prior round not archived")
			}
			requestMergeV2Restart(t, f, "request-0001")
			f.s.tickProMergeV2Restarts(t.Context())
			f.s.proMergeWG.Wait()
			if logins != 1 {
				t.Fatal("duplicate request reran workflow")
			}
		})
	}
}

func TestProMergeV2RestartWaitsForOldWorkerAndLateWrites(t *testing.T) {
	f := mergeV2Fixture(t)
	entered, exit := make(chan struct{}), make(chan struct{})
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		close(entered)
		<-ctx.Done()
		<-exit
		return nil, ctx.Err()
	}
	if _, err := f.s.startProEndJob(f.email, nil, nil, "manual_merge_v2"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old worker not started")
	}
	old, _, _ := f.s.store.MailAccountCredential(f.email)
	requestMergeV2Restart(t, f, "request-0002")
	f.s.tickProMergeV2Restarts(t.Context())
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.ProMergeV2Restart.Status != "pending" || !p.ProMergeV2.StartedAt.Equal(old.ProMergeV2.StartedAt) {
		t.Fatal("replaced a live round")
	}
	close(exit)
	f.s.proMergeWG.Wait()
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) { return mockMergeV2Submit(ctx) }
	f.s.tickProMergeV2Restarts(t.Context())
	f.s.proMergeWG.Wait()
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if !p.MergeV2Completed() || !p.ProMergeV2.StartedAt.After(old.ProMergeV2.StartedAt) || f.calls["invite"] != 1 {
		t.Fatal("handoff failed", p.ProMergeV2, f.calls)
	}
}

func TestProMergeV2RestartQueuesBehindAccountLockAndCanBeStopped(t *testing.T) {
	f := mergeV2Fixture(t)
	value, _ := f.s.proLocks.LoadOrStore("account:"+f.email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	requestMergeV2Restart(t, f, "request-0003")
	f.s.tickProMergeV2Restarts(t.Context())
	lock.Unlock()
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if !p.MergeV2RestartPending() {
		t.Fatal("busy account rejected restart")
	}
	w := f.request(f.s.stopProAutomation, "POST", "/stop", "")
	if w.Code != 200 && w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.s.tickProMergeV2Restarts(t.Context())
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if p.ProMergeV2Restart.Status != "cancelled" || len(f.calls) != 0 {
		t.Fatal("later user stop ignored", p.ProMergeV2Restart, f.calls)
	}
}

func TestProMergeV2OldRoundCannotAcceptNewRound(t *testing.T) {
	f := mergeV2Fixture(t)
	old := time.Now().Add(-time.Minute)
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMergeV2 = &model.ProMergeV2{TeamID: "original-team", StartedAt: time.Now(), SubmittedAt: &old, Steps: map[string]string{"submit": "running"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), mergeV2GenerationKey{}, old)
	if _, err := f.s.mergeV2Checkpoint(ctx, f.email, "original-team", "merge_v2_accepted", nil); err == nil {
		t.Fatal("old generation accepted new round")
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.MergeV2Accepted() {
		t.Fatal("late callback changed new round")
	}
}

func TestProMergeV2ManualRestartAndRetryOverrideAutomaticPause(t *testing.T) {
	f := mergeV2Fixture(t)
	setAutoDeletePaused(t, f.s, true)
	cfg, _, _, _ := f.s.store.ProSettings()
	rule := cfg.MergeV2RecoveryConfig()
	rule.Enabled = false
	cfg.MergeV2Recovery = &rule
	cfg.RetryCount = 2
	if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		calls++
		if calls == 1 {
			return map[string]any{"success": false, "retryable": true, "error_code": "login_session_expired"}, nil
		}
		return mockMergeV2Submit(ctx)
	}
	requestMergeV2Restart(t, f, "request-pause-01")
	f.s.tickProMergeV2Restarts(t.Context())
	f.s.proMergeWG.Wait()
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if calls != 1 || p.ProMergeV2.Recovery.NextAt == nil {
		t.Fatal("manual first round blocked", p.ProMergeV2)
	}
	dueRecovery(t, f)
	f.s.tickProMergeV2Recovery(t.Context())
	f.s.proMergeWG.Wait()
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if calls != 2 || !p.MergeV2Completed() {
		t.Fatal("manual retry blocked by automatic pause", p.ProMergeV2, calls)
	}
}

func TestProMergeV2ManualRestartDeadResultStillCleansMother(t *testing.T) {
	f := mergeV2Fixture(t)
	f.s.proMergeV2Login = func(context.Context, string) (map[string]any, error) {
		return map[string]any{"success": false, "dead": true, "error_code": "account_deactivated", "error": "account_deactivated"}, nil
	}
	requestMergeV2Restart(t, f, "request-dead-01")
	f.s.tickProMergeV2Restarts(t.Context())
	f.s.proMergeWG.Wait()
	dueRecovery(t, f)
	f.s.tickProMergeV2Recovery(t.Context())
	f.s.proMergeWG.Wait()
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if !p.IsProDead() || !p.ProMergeV2.Recovery.Exhausted || p.ProMergeV2.Steps["remove"] != "completed" || p.MergeV2Completed() {
		t.Fatal("manual failure left member behind", p.ProMergeV2)
	}
}
