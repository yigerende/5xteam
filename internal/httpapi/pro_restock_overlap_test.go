package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestProRestockOverlapStartsOneWorkflowAcrossTicks(t *testing.T) {
	var payments, launches atomic.Int32
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { payments.Add(1) })
	configureProAutoFixture(t, s)
	s.proAutoCancel()
	s.proAutoWG.Wait()
	for i := 0; i < 3; i++ {
		e := fmt.Sprintf("overlap-spare-%d@example.com", i)
		if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: e, ManagementScope: "pro"}, model.MailAccountCredentials{Email: e}); err != nil {
			t.Fatal(err)
		}
	}
	p, password, _, err := st.ProSettings()
	if err != nil {
		t.Fatal(err)
	}
	p.Sub2.AccountConcurrency, p.Concurrency = 50, 2
	if _, err = st.SaveProSettings(p, password, ""); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c.CapacityPercent, c.MinBatch, c.MaxBatch, c.MaxInflight = 100, 1, 3, 4
	c.TimeEnabled, c.ForceMergeEnabled, c.ExitEnabled = true, true, true
	c.ForceMergeMinutes, c.ExitMinutes = 5, 5
	c, err = st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	pay, _, _ := st.GPTPaySettings()
	deadline := time.Now().Add(4 * time.Minute)
	on := true
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		now := time.Now()
		rate := 1.
		money := &restock.AccountMoney{At: now, RateUSD: &rate, Windows: []restock.MoneyWindow{{Seconds: 604800, ResetAt: now.Add(time.Hour), UsedPercent: 96, UsedUSD: 96}}}
		return restock.Snapshot{At: now, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 99, Ready: true, Capacity: 50, Used: 30, Plan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Money: money, EJH: true, PushTimeoutEnabled: &on, ForceMergeDeadline: &deadline, ExitDeadline: &deadline, ForceMergeKey: "old-cycle"}}}, nil
	}
	// One earlier qualifying sample already exists. All three rules must then
	// trigger in the first tick, with enough spare accounts/slots to expose duplicates.
	s.proRestock.lastVersion = c.Version
	previous, err := s.proRestock.monitor(context.Background(), c, p, password)
	if err != nil {
		t.Fatal(err)
	}
	previous.At = previous.At.Add(-time.Second)
	s.proRestock.lastSnapshot = restock.ObserveAdmission(c, previous, restock.Snapshot{}, nil, p.Sub2.AccountConcurrency, 5, time.Now())
	if s.proRestock.lastSnapshot.ExitConfirmations != c.Confirmations-1 {
		t.Fatal("fixture did not preserve the earlier independent exit sample")
	}
	started := make(chan string, 4)
	release := make(chan struct{})
	s.proAutoHooks = &proAutomationHooks{Session: func(_ context.Context, e string, _ func(string, map[string]any) (any, error)) error {
		launches.Add(1)
		started <- e
		<-release
		return errors.New("fixture stops before payment")
	}}
	defer func() { close(release); waitProAutoFinished(t, s, email) }()
	if err = s.tickProRestock(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	d := s.proRestock.decision
	if d.TimeNeed != 1 || d.ForceMergeNeed != 1 || d.ExitNeed != 1 || d.Started != 1 {
		t.Fatalf("three triggers did not reserve one account: %+v", d)
	}
	select {
	case e := <-started:
		email = e
	case <-time.After(5 * time.Second):
		t.Fatal("full automation workflow was not started")
	}
	for i := 0; i < 4; i++ {
		if err = s.tickProRestock(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if d = s.proRestock.decision; d.Suggested != 0 || d.Started != 0 || d.Inflight != 1 {
			t.Fatal("later tick ordered the same shortage", d)
		}
	}
	tasks, err := st.ProRestockTasks()
	if err != nil || len(tasks) != 1 || launches.Load() != 1 || payments.Load() != 0 {
		t.Fatal("duplicate or real supplier call", tasks, launches.Load(), payments.Load(), err)
	}
}
