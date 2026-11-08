package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestRestockMonitorDoesNotWaitForAdmissionDuringNewDemand(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier request") })
	configureProAutoFixture(t, s)
	s.proAutoCancel()
	s.proAutoWG.Wait()
	p, _, _, _ := st.ProSettings()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID, c.EmptyGroupCount = true, "auto", 1, 1
	if _, err := st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	pay, _, _ := st.GPTPaySettings()
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		return restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}, nil
	}
	s.proCardAdmissionMu.Lock()
	defer s.proCardAdmissionMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.tickProRestock(context.Background(), true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new-demand monitor waited for card admission")
	}
	if !strings.Contains(s.proRestock.decision.Reason, "银行卡准备正在执行") || s.proRestock.decision.Started != 0 {
		t.Fatal(s.proRestock.decision.Reason)
	}
	if tasks, err := st.ProRestockTasks(); err != nil || len(tasks) != 0 {
		t.Fatal("busy admission consumed account", tasks, err)
	}
}

func TestCardPreparationLockBoundedAndCancelled(t *testing.T) {
	var lock sync.Mutex
	lock.Lock()
	defer lock.Unlock()
	start := time.Now()
	err := lockProCardPreparation(context.Background(), &lock, "waiting")
	var waiting *proCardPreparationWait
	if !errors.As(err, &waiting) || time.Since(start) > 3*time.Second {
		t.Fatal("unbounded admission", err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = lockProCardPreparation(ctx, &lock, "waiting"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEJHPreparationWaitKeepsSameRestockAndResumesAfterOpeningReview(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	s.proAutoCancel()
	s.proAutoWG.Wait()
	p, _, _, _ := s.store.ProSettings()
	p.Sub2.URL, p.Sub2.Email, p.Sub2.GroupIDs = "http://sub2.invalid", "admin@example.com", []int64{1}
	p.TargetAdminID = "fixture-mother"
	p.RetryIntervalSeconds = 37
	if _, err := s.store.SaveProSettings(p, "test-password", ""); err != nil {
		t.Fatal(err)
	}
	op := ejh.Operation{ID: "unconfirmed-opening", Method: "open", Status: "unknown", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.store.EJHPut("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err := s.store.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	pay, _, _ := s.store.GPTPaySettings()
	snap := restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}
	tasks, err := s.store.ReserveProRestock(restock.Decision{ID: "wait-review", At: time.Now(), Version: c.Version, Suggested: 1, Snapshot: snap}, "", 0)
	if err != nil || len(tasks) != 1 || tasks[0].Email != email {
		t.Fatal(tasks, err)
	}
	task := tasks[0]
	task.Status = "submitting"
	if err = s.store.ClaimProRestockDispatch(task); err != nil {
		t.Fatal(err)
	}
	s.startClaimedProRestock(context.Background(), task, pay, "")
	current, err := s.store.ProRestockTask(task.ID)
	if err != nil || current.Status != "reserved" || current.NextCardCheckAt == nil || current.NextCardCheckAt.Before(time.Now().Add(35*time.Second)) || current.SubmittedAt != nil {
		t.Fatal("resource hold became failure", current, err)
	}
	limits, _ := s.store.ProRestockLimits(time.Now())
	if limits.Failures != 0 || limits.DayCount != 1 || issued.Load() != 0 || paid.Load() != 0 {
		t.Fatal("hold consumed account or money", limits, issued.Load(), paid.Load())
	}
	// A scheduler pass before the deadline must leave the same intent untouched.
	s.dispatchProRestock(context.Background(), c, p, snap)
	again, _ := s.store.ProRestockTask(task.ID)
	if !again.UpdatedAt.Equal(current.UpdatedAt) {
		t.Fatal("retried before configured interval")
	}
	// Reconciliation must also keep the queued task without adding failures.
	if _, _, err = s.restockTasks(snap, p, c); err != nil {
		t.Fatal(err)
	}
	again, _ = s.store.ProRestockTask(task.ID)
	if again.Status != "reserved" {
		t.Fatal("monitor terminalized hold", again)
	}
	// Test-only issuer evidence confirms the original opening was not executed.
	// The same queued task may now issue once; old payment occupancy stays held.
	op.Status, op.Resolution = "failed", "not_executed"
	if err = s.store.EJHPut("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
	again.Status = "submitting"
	again.NextCardCheckAt = nil
	if err = s.store.ClaimProRestockDispatch(again); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), proRestockContextKey{}, task.ID)
	card, _, err := s.reserveProStartCard(ctx, email, "", true, false)
	if err != nil || card.ID == "test-card" || issued.Load() != 1 || paid.Load() != 0 {
		t.Fatal("original task cannot resume", card, err)
	}
	_ = s.store.ReleaseProCard(email)
}

func TestEJHManualReviewDoesNotRestartFailureCooldown(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "resolved"}[resolved], func(t *testing.T) {
			s, issued, paid, _ := allocationFixture(t)
			op := ejh.Operation{ID: "failed-open", Method: "open", Automatic: true, Status: "failed", Error: "issuer unavailable", CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now()}
			if resolved {
				op.Resolution = "not_executed"
				op.Error = ""
			}
			if err := s.store.EJHPut("operation", op.ID, op); err != nil {
				t.Fatal(err)
			}
			_, err := s.ensureEJHCard(context.Background())
			var waiting *proCardPreparationWait
			if resolved {
				if err != nil || issued.Load() != 1 {
					t.Fatal("resolution added another cooldown", err)
				}
			} else if !errors.As(err, &waiting) || issued.Load() != 0 {
				t.Fatal("failed issuance lost cooldown", err)
			}
			if paid.Load() != 0 {
				t.Fatal("test unexpectedly submitted payment")
			}
		})
	}
}

func TestManualReplacementPreparationUsesConfiguredRetryDelay(t *testing.T) {
	s, _, _, email := allocationFixture(t)
	cfg, _, _, _ := s.store.ProSettings()
	cfg.RetryIntervalSeconds = 41
	if _, err := s.store.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	p, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto.Replacement = &model.ProRechargeReplacement{RootEmail: email, Queued: true, Limit: 2}
	})
	if err != nil {
		t.Fatal(err)
	}
	s.rechargeReplacementWait(p, "waiting for original review")
	p, _, _ = s.store.MailAccountCredential(email)
	if p.ProAuto.RetryAt == nil || p.ProAuto.RetryAt.Before(time.Now().Add(39*time.Second)) || p.ProAuto.Replacement.Attempts != 0 {
		t.Fatal(p.ProAuto)
	}
}
