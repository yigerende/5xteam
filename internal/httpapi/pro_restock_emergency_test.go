package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestProRestockEmergencySpikeDoesNotReserveBeforeConfirmation(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	p, password, _, err := st.ProSettings()
	if err != nil {
		t.Fatal(err)
	}
	p.Sub2.AccountConcurrency = 60
	if _, err = st.SaveProSettings(p, password, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: "second@example.invalid", ManagementScope: "pro"}, model.MailAccountCredentials{Email: "second@example.invalid"}); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c.TimeEnabled, c.ForceMergeEnabled, c.ExitEnabled = false, false, false
	c.CapacityPercent, c.TriggerPercent, c.TargetPercent = 100, 60, 55
	c.Confirmations, c.EmergencyQueue, c.CheckSeconds = 3, 3, 5
	c.MinBatch, c.MaxBatch, c.MaxInflight, c.Reserve = 1, 2, 2, 0
	if _, err = st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	pay, _, _ := st.GPTPaySettings()
	used, waiting := 60, 9
	at := time.Now()
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		return restock.Snapshot{At: at, Valid: true, LoadValid: true, Binding: restockBinding(p), GroupID: 1, NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Capacity: 60, Used: used, Waiting: waiting, Ready: true}}}, nil
	}
	// Hold execution slots: exercise the actual reservation transaction without
	// starting a login, card operation or payment.
	s.proMergeMu.Lock()
	s.proAutoRunning = p.Concurrency
	s.proMergeMu.Unlock()
	for i, tc := range []struct {
		used, waiting, count, tasks int
		cached                      bool
	}{{60, 9, 1, 0, false}, {60, 9, 1, 0, true}, {29, 0, 0, 0, false}, {60, 9, 1, 0, false}, {60, 9, 2, 0, false}, {60, 9, 3, 2, false}} {
		used, waiting = tc.used, tc.waiting
		if !tc.cached {
			at = time.Now()
		}
		if err = s.tickProRestock(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		tasks, err := st.ProRestockTasks()
		if err != nil || len(tasks) != tc.tasks || s.proRestock.confirmation != tc.count {
			t.Fatalf("tick %d: tasks=%d count=%d want tasks=%d count=%d error=%v reason=%s", i, len(tasks), s.proRestock.confirmation, tc.tasks, tc.count, err, s.proRestock.decision.Reason)
		}
		for _, task := range tasks {
			if task.Status != "reserved" || task.SubmittedAt != nil {
				t.Fatal("test must not execute a business task", task)
			}
		}
	}
}
