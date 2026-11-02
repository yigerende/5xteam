package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func planRecoveryFixture(t *testing.T, plan string) (*proExecutionFixture, *int) {
	t.Helper()
	f := mergeV2Fixture(t)
	removed := false
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mother-token" || r.Header.Get("chatgpt-account-id") != "original-team" {
			t.Error("wrong mother or identity")
		}
		switch {
		case r.Method == "DELETE":
			removed = true
			f.calls["remove"]++
			fmt.Fprint(w, `{"success":true}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/users"):
			if removed {
				fmt.Fprint(w, `{"items":[],"total":0}`)
			} else {
				fmt.Fprintf(w, `{"items":[{"id":"pro-user","email":%q,"role":"standard-user"}],"total":1}`, f.email)
			}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/invites"):
			fmt.Fprint(w, `{"items":[],"total":0}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/invites"):
			removed = false
			f.calls["invite"]++
			fmt.Fprint(w, `{"success":true}`)
		default:
			t.Errorf("unexpected recovery request %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(upstream.Close)
	settings := f.s.store.Settings()
	settings.BaseURL = upstream.URL
	if err := f.s.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	f.admin.Label = "recovery-mother"
	f.admin = saveTestAdmin(t, f.s.store, f.admin, "mother-token", upstream.URL)
	now := time.Now().Add(-time.Minute)
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMergeV2 = &model.ProMergeV2{Status: "unknown", Stage: "submit", AdminID: f.admin.ID, TeamID: "original-team", UserID: "pro-user", StartedAt: now, SubmittedAt: &now, Steps: map[string]string{"invite": "completed", "login": "completed", "submit": "unknown"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.proMergeV2Plan = func(ctx context.Context, p model.MailAccountProfile) (map[string]any, error) {
		calls++
		if !removed {
			t.Fatal("plan queried before removal")
		}
		v, _ := f.s.autoAdminLocks.LoadOrStore("original-team", &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if !lock.TryLock() {
			t.Error("personal check blocks other mother tasks")
		} else {
			lock.Unlock()
		}
		if plan == "error" {
			return nil, errors.New("fixture plan unavailable")
		}
		return map[string]any{"success": true, "plan": plan, "user_id": "pro-user"}, nil
	}
	return f, &calls
}

func dueRecovery(t *testing.T, f *proExecutionFixture) {
	t.Helper()
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		past := time.Now().Add(-time.Second)
		if p.ProMergeV2.Recovery == nil {
			p.ProMergeV2.Recovery = &model.ProMergeV2Recovery{Phase: "removing", Cycles: 1}
		}
		p.ProMergeV2.Recovery.NextAt = &past
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProMergeV2RecoveryFreeCompletesWithoutInventingAcceptance(t *testing.T) {
	f, calls := planRecoveryFixture(t, "free")
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if !errors.Is(err, errMergeV2Unknown) || p.ProMergeV2.Recovery.NextAt == nil || *calls != 0 {
		t.Fatal("initial unknown not scheduled", err)
	}
	if _, err = f.s.performProMergeV2(t.Context(), f.email); !errors.Is(err, errMergeV2Unknown) || *calls != 0 {
		t.Fatal("delay ignored", err)
	}
	dueRecovery(t, f)
	p, err = f.s.performProMergeV2(t.Context(), f.email)
	if err != nil || !p.MergeV2Completed() || !p.MergeV2Removed() || p.MergeV2Accepted() || p.ProMergeV2.CompletionBasis != "plan_free" || p.ProAuto.Status != "completed" {
		t.Fatal(p.ProMergeV2, err)
	}
	if *calls != 1 || f.calls["remove"] != 1 || f.calls["invite"] != 0 || f.s.proMergeRunning != 0 {
		t.Fatal("duplicate work or slot leaked", f.calls, *calls)
	}
	if _, err = f.s.performProMergeV2(t.Context(), f.email); err != nil || *calls != 1 {
		t.Fatal("completed rerun", err)
	}
}

func TestProMergeV2RecoveryPaidRequeuesSameAccountWithBoundedCycles(t *testing.T) {
	f, calls := planRecoveryFixture(t, "paid")
	dueRecovery(t, f)
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || p.ProMergeV2.Recovery.Phase != "reinvite" || p.ProMergeV2.Recovery.NextAt == nil || f.calls["invite"] != 0 {
		t.Fatal(p.ProMergeV2, err)
	}
	logins := 0
	f.s.proMergeV2Login = func(ctx context.Context, email string) (map[string]any, error) {
		logins++
		if email != f.email {
			t.Fatal("account changed")
		}
		o := ctx.Value(mergeV2LoginKey{}).(*mergeV2LoginOptions)
		for _, method := range []string{"merge_v2_ready", "merge_v2_submitting"} {
			if _, e := o.Call(method, map[string]any{"team_id": "original-team", "user_id": "pro-user"}); e != nil {
				return nil, e
			}
		}
		return nil, errors.New("fixture lost response")
	}
	dueRecovery(t, f)
	p, err = f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || logins != 1 || p.ProMergeV2.Recovery.Cycles != 2 || len(p.ProMergeV2.Recovery.Submissions) != 1 || p.ProMergeV2.Recovery.Phase != "removing" {
		t.Fatal(p.ProMergeV2, err)
	}
	dueRecovery(t, f)
	p, err = f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || !p.ProMergeV2.Recovery.Exhausted || p.ProMergeV2.Recovery.NextAt != nil || *calls != 2 || logins != 1 {
		t.Fatal("cycle budget ignored", p.ProMergeV2, err)
	}
	items, e := f.s.store.DueProMergeV2Recovery(time.Now().Add(time.Hour))
	if e != nil || len(items) != 0 {
		t.Fatal(items, e)
	}
}

func TestProMergeV2RecoveryFailuresStopDisableAndRestart(t *testing.T) {
	f, calls := planRecoveryFixture(t, "error")
	dueRecovery(t, f)
	cfg, _, _, _ := f.s.store.ProSettings()
	rule := cfg.MergeV2RecoveryConfig()
	rule.Enabled = false
	cfg.MergeV2Recovery = &rule
	if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.performProMergeV2(t.Context(), f.email); err == nil || *calls != 0 {
		t.Fatal("disabled recovery ran")
	}
	rule.Enabled = true
	if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		dueRecovery(t, f)
		p, err := f.s.performProMergeV2(t.Context(), f.email)
		if err == nil || p.ProMergeV2.Recovery.Attempts != attempt || *calls != attempt || f.s.proMergeRunning != 0 {
			t.Fatal(p.ProMergeV2, err)
		}
		if attempt == 3 && (!p.ProMergeV2.Recovery.Exhausted || p.ProMergeV2.Recovery.NextAt != nil) {
			t.Fatal("no terminal limit")
		}
	}
	if f.calls["remove"] != 1 {
		t.Fatal("repeated successful removal", f.calls)
	}
	// Restart retains terminal budgets; stop never starts a recovery request.
	if err := f.s.store.RecoverProWorkflows(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.performProMergeV2(t.Context(), f.email); err == nil || *calls != 3 {
		t.Fatal("restart resets budget")
	}
	_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
	if _, err := f.s.performProMergeV2(t.Context(), f.email); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestProMergeV2Configured404OnlyVerifiesAcrossRestart(t *testing.T) {
	for _, plan := range []string{"free", "paid"} {
		t.Run(plan, func(t *testing.T) {
			f, calls := planRecoveryFixture(t, plan)
			if _, err := f.s.mergeV2Checkpoint(t.Context(), f.email, "original-team", "merge_v2_verify", map[string]any{"http_status": 404}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.mergeV2Checkpoint(t.Context(), f.email, "original-team", "merge_v2_rejected", nil); err == nil {
				t.Fatal("verification checkpoint cleared")
			}
			cfg, _, _, _ := f.s.store.ProSettings()
			rule := cfg.MergeV2RecoveryConfig()
			rule.VerificationHTTPStatuses = "409"
			cfg.MergeV2Recovery = &rule
			if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
				t.Fatal(err)
			}
			f.s.proMergeV2Login = func(context.Context, string) (map[string]any, error) {
				t.Fatal("verification resubmitted")
				return nil, nil
			}
			for i := 1; i <= rule.MaxAttempts; i++ {
				if err := f.s.store.RecoverProWorkflows(); err != nil {
					t.Fatal(err)
				}
				dueRecovery(t, f)
				p, err := f.s.performProMergeV2(t.Context(), f.email)
				if p.ProMergeV2.SubmittedAt == nil || p.MergeV2Accepted() || !p.ProMergeV2.VerificationOnly || f.calls["invite"] != 0 {
					t.Fatal("lost original submission", p.ProMergeV2)
				}
				if plan == "free" {
					if err != nil || !p.MergeV2Completed() || p.ProMergeV2.CompletionBasis != "plan_free" {
						t.Fatal(p.ProMergeV2, err)
					}
					break
				}
				if err == nil || *calls != i || p.ProMergeV2.Recovery.Phase != "checking" {
					t.Fatal(p.ProMergeV2, err)
				}
				if i == rule.MaxAttempts && (!p.ProMergeV2.Recovery.Exhausted || p.ProMergeV2.Recovery.NextAt != nil) {
					t.Fatal("unbounded verification", p.ProMergeV2)
				}
			}
		})
	}
}

func TestProMergeV2AcceptedCleanupUsesDurableRetryOnly(t *testing.T) {
	f := mergeV2Fixture(t)
	f.failStage, f.failCount = "remove", 1
	logins := 0
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) { logins++; return mockMergeV2Submit(ctx) }
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || !p.MergeV2Accepted() || p.MergeV2Completed() || p.ProMergeV2.Recovery.Phase != "tail" || p.ProMergeV2.Recovery.NextAt == nil || f.s.proMergeRunning != 0 {
		t.Fatal(p.ProMergeV2, err)
	}
	if err = f.s.store.RecoverProWorkflows(); err != nil {
		t.Fatal(err)
	}
	dueRecovery(t, f)
	p, err = f.s.performProMergeV2(t.Context(), f.email)
	if err != nil || !p.MergeV2Completed() || logins != 1 || f.calls["invite"] != 1 || f.calls["remove"] != 2 {
		t.Fatal(p.ProMergeV2, err, f.calls, logins)
	}
}

func TestProMergeV2DeadAccountOnlyCleansMotherAndHonorsNewStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			f := mergeV2Fixture(t)
			f.s.proMergeV2Login = func(context.Context, string) (map[string]any, error) {
				return map[string]any{"success": false, "dead": true, "error_code": "account_deactivated", "error": "account_deactivated"}, nil
			}
			p, err := f.s.performProMergeV2(t.Context(), f.email)
			if !errors.Is(err, errProAccountDead) || !p.MergeV2DeadCleanupAllowed() || p.ProMergeV2.Recovery.NextAt == nil {
				t.Fatal("dead cleanup not scheduled", p.ProMergeV2, err)
			}
			dueRecovery(t, f)
			if stop {
				_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopVersion++ })
			}
			items, err := f.s.store.DueProMergeV2Recovery(time.Now())
			if err != nil || len(items) != map[bool]int{false: 1, true: 0}[stop] {
				t.Fatal(items, err)
			}
			p, err = f.s.performProMergeV2(t.Context(), f.email)
			if stop {
				if !errors.Is(err, context.Canceled) || f.calls["remove"] != 0 {
					t.Fatal(err, f.calls)
				}
			} else if err == nil || p.ProMergeV2.Steps["remove"] != "completed" || !p.ProMergeV2.Recovery.Exhausted || p.MergeV2Completed() || !p.IsProDead() {
				t.Fatal(p.ProMergeV2, err)
			}
		})
	}
}

func TestProMergeV2InvitationRetriesUsePersistedBudget(t *testing.T) {
	f := mergeV2Fixture(t)
	f.failStage, f.failCount = "invite", 99
	cfg, _, _, _ := f.s.store.ProSettings()
	cfg.RetryCount, cfg.RetryIntervalSeconds = 1, 19
	if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
		t.Fatal(err)
	}
	f.s.proMergeV2Login = func(context.Context, string) (map[string]any, error) {
		t.Fatal("login after failed invitation")
		return nil, nil
	}
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || p.ProMergeV2.Recovery.Phase != "invite" || p.ProMergeV2.Recovery.Attempts != 1 || f.calls["invite"] != 1 || time.Until(*p.ProMergeV2.Recovery.NextAt) < 18*time.Second {
		t.Fatal(p.ProMergeV2, err)
	}
	if _, err = f.s.performProMergeV2(t.Context(), f.email); err == nil || f.calls["invite"] != 1 {
		t.Fatal("immediate invitation retry")
	}
	dueRecovery(t, f)
	p, err = f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || p.ProMergeV2.Recovery.Phase != "cleanup" || f.calls["invite"] != 2 {
		t.Fatal(p.ProMergeV2, err, f.calls)
	}
}
