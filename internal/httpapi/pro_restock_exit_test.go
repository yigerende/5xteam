package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRestockExitPolicyUsesActualThreshold(t *testing.T) {
	c := model.DefaultProSettings()
	c.QuotaUsedThreshold = 99
	p := model.MailAccountProfile{ProAuto: model.ProAutoState{ID: "auto", Status: "waiting_quota", QuotaUsedThreshold: 85}}
	start := time.Now().Add(-time.Hour)
	on := true
	p.ProPushStartedAt = &start
	p.ProPushTimeoutEnabled = &on
	p.ProPushTimeoutMinutes = 65
	a := restock.Account{}
	applyRestockExitPolicy(&a, p, c)
	if a.QuotaMergeThreshold == nil || *a.QuotaMergeThreshold != 85 || a.ExitDeadline == nil || !a.ExitDeadline.Equal(start.Add(65*time.Minute)) {
		t.Fatal(a)
	}
	c.QuotaEnabled, c.AutoMergeEnabled = true, true
	c.QuotaUsedThreshold = 80
	a = restock.Account{}
	applyRestockExitPolicy(&a, p, c)
	if *a.QuotaMergeThreshold != 80 {
		t.Fatal("independent active quota sweep ignored", a)
	}
	p.ProAuto.StopRequested = true
	a = restock.Account{}
	applyRestockExitPolicy(&a, p, c)
	if a.QuotaMergeThreshold != nil || a.ExitDeadline != nil {
		t.Fatal("stopped account inherited automatic exit", a)
	}
}

func TestProRestockExitReservationDispatchDespiteDemandDrop(t *testing.T) {
	for _, cancelDemand := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatch", true: "demand_drop"}[cancelDemand], func(t *testing.T) {
			s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
			configureProAutoFixture(t, s)
			s.proAutoCancel()
			c := restock.DefaultConfig()
			c.Enabled = true
			c.Mode = "auto"
			c.GroupID = 1
			c.ExitEnabled = true
			c.ExitMinutes = 5
			c.TimeEnabled = false
			c.ConcurrencyEnabled = false
			c.MinBatch = 1
			c.MaxBatch = 1
			c, err := st.SaveProRestockConfig(c)
			if err != nil {
				t.Fatal(err)
			}
			p, _, _, _ := st.ProSettings()
			pay, _, _ := st.GPTPaySettings()
			load := 22
			fixedAt := time.Now()
			resetAt := fixedAt.Add(time.Hour)
			s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
				now := time.Now()
				if !fixedAt.IsZero() {
					now = fixedAt
				}
				rate := 1.
				money := func(used float64) *restock.AccountMoney {
					return &restock.AccountMoney{At: now, RateUSD: &rate, Windows: []restock.MoneyWindow{{Seconds: 604800, ResetAt: resetAt, UsedPercent: used, UsedUSD: used}}}
				}
				return restock.Snapshot{ID: "exit-sample", At: now, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 101, Ready: true, Capacity: 50, Used: load, Money: money(96)}, {ID: 102, Ready: true, Capacity: 50, Used: load, Money: money(20)}}}, nil
			}
			// Hold the work slots so the new intent must survive another snapshot.
			s.proMergeMu.Lock()
			s.proAutoRunning = p.Concurrency
			s.proMergeMu.Unlock()
			if err = s.tickProRestock(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if s.proRestock.decision.ExitNeed != 0 {
				t.Fatal("first sample bypassed confirmation")
			}
			if err = s.tickProRestock(context.Background(), true); err != nil || s.proRestock.decision.ExitNeed != 0 {
				t.Fatal("repeated snapshot advanced confirmations", err)
			}
			fixedAt = time.Time{}
			load = 5
			if err = s.tickProRestock(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			load = 22
			if err = s.tickProRestock(context.Background(), true); err != nil || s.proRestock.decision.ExitNeed != 0 {
				t.Fatal("non-consecutive high samples ordered", err)
			}
			if err = s.tickProRestock(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			tasks, err := st.ProRestockTasks()
			if err != nil || len(tasks) != 1 || tasks[0].Status != "reserved" {
				t.Fatalf("exit rule failed to reserve: %+v %v", tasks, err)
			}
			if err = s.tickProRestock(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			tasks, _ = st.ProRestockTasks()
			if tasks[0].Status != "reserved" {
				t.Fatal("low current utilization cancelled future capacity reservation", tasks)
			}
			var entered atomic.Int32
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				entered.Add(1)
				return errors.New("test ends before payment")
			}}
			if cancelDemand {
				load = 5
			}
			s.proMergeMu.Lock()
			s.proAutoRunning = 0
			s.proMergeMu.Unlock()
			if err = s.tickProRestock(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			waitProAutoFinished(t, s, email)
			if entered.Load() != 1 {
				t.Fatal("committed batch stopped dispatching after demand changed")
			}
			records, _ := st.ProRestockDecisions(10, 0)
			found := false
			for _, d := range records {
				if d.Started > 0 {
					found = true
					if d.Exit == nil || d.ExitNeed != 5 || d.Capacity != 60 || d.Exit.AfterCapacity != 30 || d.Trace.EffectiveNewAccountCapacity != 6 || len(d.Exit.Rows) != 2 {
						t.Fatal("missing decision evidence", d)
					}
				}
			}
			if !found {
				t.Fatal("reservation evidence not persisted")
			}
		})
	}
}
