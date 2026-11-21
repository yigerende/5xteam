package store

import (
	"chatgpt-space-merge/internal/restock"
	"fmt"
	"testing"
	"time"
)

func overlapMoney(now time.Time, used, rate float64) *restock.AccountMoney {
	return &restock.AccountMoney{At: now, RateUSD: &rate, Windows: []restock.MoneyWindow{{Seconds: 604800, ResetAt: now.Add(7 * 24 * time.Hour), UsedPercent: used, UsedUSD: used}}}
}

func TestRestockOverlapReservationAcrossRounds(t *testing.T) {
	s := transferStore(t)
	c, snap := forceRestockFixture(t, s)
	c.CapacityPercent, c.MinBatch, c.MaxBatch = 100, 1, 1
	c.TimeEnabled, c.ExitEnabled = true, true
	c.ForceMergeMinutes, c.ExitMinutes = 5, 5
	var err error
	c, err = s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	deadline := now.Add(4 * time.Minute)
	snap.At, snap.ExitConfirmations = now, c.Confirmations
	snap.Accounts[0].ForceMergeDeadline, snap.Accounts[0].ExitDeadline = &deadline, &deadline
	snap.Accounts[0].Capacity, snap.Accounts[0].Used = 50, 30
	snap.Accounts[0].Plan, snap.Accounts[0].Money = snap.NewPlan, overlapMoney(now, 96, 1)
	second := snap.Accounts[0]
	second.ID, second.ForceMergeKey = 2, "original-cycle-2"
	snap.Accounts = append(snap.Accounts, second)
	tasks := []restock.Task{}
	for round := 0; round < 4; round++ {
		at := now.Add(time.Duration(round) * time.Minute)
		if round == 2 {
			readyAt := at.Add(-time.Second)
			tasks[0].Status, tasks[0].AccountID, tasks[0].ETA, tasks[0].ReadyAt = "ready", 10, nil, &readyAt
			if err = s.SaveProRestockTask(tasks[0]); err != nil {
				t.Fatal(err)
			}
			snap.Accounts = append(snap.Accounts, restock.Account{ID: 10, Ready: true, Capacity: 50, Plan: snap.NewPlan})
		}
		snap.At = at
		for i := range snap.Accounts {
			snap.Accounts[i].Money = overlapMoney(at, 96, 1)
			if snap.Accounts[i].ID >= 10 {
				snap.Accounts[i].Money = overlapMoney(at, 1, 0)
			}
		}
		limits, e := s.ProRestockLimits(at)
		if e != nil {
			t.Fatal(e)
		}
		d := restock.Evaluate(c, snap, tasks, limits, c.Confirmations, 50, 2, at)
		d.ID = fmt.Sprintf("overlap-%d", round)
		want := 0
		if round == 0 || round == 2 {
			want = 1
		}
		if d.Suggested != want {
			t.Fatalf("round %d: want %d, got %+v", round, want, d)
		}
		if d.Suggested > 0 {
			// This fixture advances a synthetic clock by minutes. Live decision
			// freshness is covered separately against the actual wall clock.
			d.DemandKnown = false
			added, e := s.ReserveProRestock(d, "", d.Inflight)
			if e != nil || len(added) != want {
				t.Fatal(added, e)
			}
			for _, task := range added {
				task.Status = "running"
				eta := now.Add(3 * time.Minute)
				task.ETA = &eta
				if e = s.SaveProRestockTask(task); e != nil {
					t.Fatal(e)
				}
			}
			tasks, e = s.ProRestockTasks()
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(tasks) != 2 || tasks[0].ForceMergeSource == tasks[1].ForceMergeSource {
		t.Fatal("two outgoing accounts must have exactly two distinct replacements", tasks)
	}
	// Arrival must remain covered when reread from storage, even once cooldown
	// has expired; no second batch may be reserved for the same two lifecycles.
	at := now.Add(3 * time.Minute)
	for i := range tasks {
		tasks[i].Status, tasks[i].AccountID, tasks[i].ETA, tasks[i].ReadyAt = "ready", int64(10+i), nil, &at
		if err = s.SaveProRestockTask(tasks[i]); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			snap.Accounts = append(snap.Accounts, restock.Account{ID: int64(10 + i), Ready: true, Capacity: 50, Plan: snap.NewPlan, Money: overlapMoney(at, 1, 0)})
		}
	}
	tasks, err = s.ProRestockTasks()
	if err != nil {
		t.Fatal(err)
	}
	d := restock.Evaluate(c, snap, tasks, restock.Limits{}, c.Confirmations, 50, 2, at)
	if d.Suggested != 0 || d.ForceMerge.Ready != 2 || d.Inflight != 0 {
		t.Fatal("ready replacements counted again", d)
	}
}

func TestRestockOverlapReadyEligibility(t *testing.T) {
	for _, name := range []string{"different_binding", "different_group", "missing_ready_time", "previous_assignment", "missing_decision", "different_source_lifecycle", "already_present", "departed", "not_ready", "quota_exit", "deadline_exit"} {
		t.Run(name, func(t *testing.T) {
			s := transferStore(t)
			c, snap := forceRestockFixture(t, s)
			c.TimeEnabled, c.MinBatch, c.ForceMergeMinutes = true, 1, 5
			var err error
			c, err = s.SaveProRestockConfig(c)
			if err != nil {
				t.Fatal(err)
			}
			now := snap.At
			deadline := now.Add(6 * time.Minute)
			snap.Accounts[0].ForceMergeDeadline = &deadline
			snap.Accounts[0].Plan, snap.Accounts[0].Money = snap.NewPlan, overlapMoney(now, 96, 1)
			d := restock.Evaluate(c, snap, nil, restock.Limits{}, 0, 50, 2, now)
			d.ID = "earlier-capacity"
			tasks, err := s.ReserveProRestock(d, "", 0)
			if err != nil || len(tasks) != 1 {
				t.Fatal(tasks, err)
			}
			task := tasks[0]
			task.Status, task.AccountID, task.ReadyAt = "ready", 2, &now
			replacement := restock.Account{ID: 2, Ready: true, Capacity: 50, Plan: snap.NewPlan, Money: overlapMoney(now, 1, 0)}
			snap.Accounts = append(snap.Accounts, replacement)
			switch name {
			case "different_binding":
				task.Binding = "elsewhere"
			case "different_group":
				task.GroupID++
			case "missing_ready_time":
				task.ReadyAt = nil
			case "previous_assignment":
				task.ForceMergeAssigned = true
			case "missing_decision":
				task.DecisionID = "unrecorded"
			case "different_source_lifecycle":
				snap.Accounts[0].ForceMergeKey = "reopened"
			case "already_present":
				d.Snapshot.Accounts = append(append([]restock.Account{}, d.Snapshot.Accounts...), replacement)
				if err = s.SaveProRestockDecision(d); err != nil {
					t.Fatal(err)
				}
			case "departed":
				snap.Accounts = snap.Accounts[:1]
			case "not_ready":
				snap.Accounts[1].Ready = false
			case "quota_exit":
				snap.Accounts[1].Money = overlapMoney(now, 99, 1)
			case "deadline_exit":
				snap.Accounts[1].ForceMergeDeadline = &deadline
			}
			if err = s.SaveProRestockTask(task); err != nil {
				t.Fatal(err)
			}
			deadline = now.Add(4 * time.Minute)
			tasks, err = s.ReconcileForceMerge(c, snap, []restock.Task{task})
			if err != nil {
				t.Fatal(err)
			}
			if tasks[0].ForceMergeSource != "" || len(restock.ForceMergeSummary(c, snap, tasks, now).Sources) != 1 {
				t.Fatal("ineligible ready task suppressed a real replacement", tasks)
			}
		})
	}
}

func TestRestockOverlapReadyAssignmentSurvivesRestartAndCannotBeReused(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	c, snap := forceRestockFixture(t, s)
	c.TimeEnabled, c.MinBatch, c.ForceMergeMinutes = true, 1, 5
	c, err = s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	now := snap.At
	deadline := now.Add(6 * time.Minute)
	snap.Accounts[0].ForceMergeDeadline = &deadline
	snap.Accounts[0].Plan, snap.Accounts[0].Money = snap.NewPlan, overlapMoney(now, 96, 1)
	second := snap.Accounts[0]
	second.ID, second.ForceMergeKey = 3, "second-cycle"
	snap.Accounts = append(snap.Accounts, second)
	d := restock.Evaluate(c, snap, nil, restock.Limits{}, 0, 50, 2, now)
	d.ID = "capacity-before-deadline"
	tasks, err := s.ReserveProRestock(d, "", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	task := tasks[0]
	task.Status, task.AccountID, task.ReadyAt = "ready", 2, &now
	if err = s.SaveProRestockTask(task); err != nil {
		t.Fatal(err)
	}
	on := true
	newDeadline := now.Add(8 * time.Minute)
	snap.Accounts = append(snap.Accounts, restock.Account{ID: 2, Ready: true, Capacity: 50, Plan: snap.NewPlan, Money: overlapMoney(now, 1, 0), EJH: true, PushTimeoutEnabled: &on, ForceMergeKey: "new-cycle", ForceMergeDeadline: &newDeadline})
	deadline = now.Add(time.Minute)
	tasks, err = s.ReconcileForceMerge(c, snap, []restock.Task{task})
	if err != nil || tasks[0].ForceMergeSource != "original-cycle" || !tasks[0].ForceMergeAssigned {
		t.Fatal(tasks, err)
	}
	stats := restock.ForceMergeSummary(c, snap, tasks, now)
	if stats.Ready != 1 || len(stats.Sources) != 1 || stats.Sources[0] != "second-cycle" {
		t.Fatal(stats)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err = s.ProRestockTasks()
	if err != nil || len(tasks) != 1 || !tasks[0].ForceMergeAssigned || tasks[0].ForceMergeSource != "original-cycle" {
		t.Fatal("restart lost assignment", tasks, err)
	}
	// The first old account has left. Its replacement must not now be adopted
	// again for the second old account (whose lifecycle was in the same evidence).
	snap.Accounts = snap.Accounts[1:]
	for i := 0; i < 2; i++ {
		tasks, err = s.ReconcileForceMerge(c, snap, tasks)
		if err != nil {
			t.Fatal(err)
		}
		tasks, err = s.ProRestockTasks()
		if err != nil {
			t.Fatal(err)
		}
		stats = restock.ForceMergeSummary(c, snap, tasks, now)
		if tasks[0].ForceMergeSource != "" || !tasks[0].ForceMergeAssigned || len(stats.Sources) != 1 {
			t.Fatal("ready account recycled for a second lifecycle", tasks, stats)
		}
	}
}

func TestRestockOverlapReadyBeforeForceThreshold(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		shared         bool
	}{{"ejh", "ejh", false}, {"ejh_multi_group", "ejh", true}, {"nexovcc_multi_group", "nexovcc", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := transferStore(t)
			c, snap := forceRestockFixture(t, s)
			c.CapacityPercent, c.MinBatch = 100, 1
			c.TimeEnabled, c.ExitEnabled = true, true
			c.ForceMergeMinutes, c.ExitMinutes = 5, 5
			var err error
			c, err = s.SaveProRestockConfig(c)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			deadline := now.Add(6 * time.Minute)
			snap.At, snap.ExitConfirmations = now, c.Confirmations
			a := &snap.Accounts[0]
			a.EJH, a.CardProvider, a.Shared = tc.provider == "ejh", tc.provider, tc.shared
			a.ForceMergeDeadline, a.ExitDeadline = &deadline, &deadline
			a.Capacity, a.Used, a.Plan, a.Money = 50, 30, snap.NewPlan, overlapMoney(now, 96, 1)
			d := restock.Evaluate(c, snap, nil, restock.Limits{}, c.Confirmations, 50, 2, now)
			d.ID = "earlier-time-and-exit"
			if d.TimeNeed != 1 || d.ExitNeed != 1 || d.ForceMergeNeed != 0 || d.Suggested != 1 {
				t.Fatal(d)
			}
			tasks, err := s.ReserveProRestock(d, "", 0)
			if err != nil || len(tasks) != 1 {
				t.Fatal(tasks, err)
			}
			// The earlier replenishment has arrived before the average-deadline rule
			// starts. Its timeout is disabled, so it is genuine surviving capacity.
			tasks[0].Status, tasks[0].AccountID, tasks[0].ReadyAt = "ready", 2, &now
			if err = s.SaveProRestockTask(tasks[0]); err != nil {
				t.Fatal(err)
			}
			off := false
			snap.Accounts = append(snap.Accounts, restock.Account{ID: 2, Ready: true, Capacity: 50, Plan: snap.NewPlan,
				Money: overlapMoney(now, 1, 0), EJH: tc.provider == "ejh", CardProvider: tc.provider, Shared: tc.shared, PushTimeoutEnabled: &off})
			deadline = now.Add(4 * time.Minute)
			tasks, err = s.ReconcileForceMerge(c, snap, tasks)
			if err != nil {
				t.Fatal(err)
			}
			d = restock.Evaluate(c, snap, tasks, restock.Limits{}, c.Confirmations, 50, 2, now)
			if d.Suggested != 0 {
				t.Fatalf("ready account from prior time/exit trigger not reused: time=%d force=%d exit=%d suggested=%d", d.TimeNeed, d.ForceMergeNeed, d.ExitNeed, d.Suggested)
			}
		})
	}
}

func TestRestockOverlapReadyDeduplicatesUpstreamAccountAndReturnsOldHistory(t *testing.T) {
	for _, alreadyLinked := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_link_%v", alreadyLinked), func(t *testing.T) {
			s := transferStore(t)
			c, snap := forceRestockFixture(t, s)
			c.TimeEnabled, c.MinBatch, c.MaxBatch, c.ForceMergeMinutes = true, 1, 2, 5
			var err error
			c, err = s.SaveProRestockConfig(c)
			if err != nil {
				t.Fatal(err)
			}
			now := snap.At
			deadline := now.Add(6 * time.Minute)
			snap.Accounts[0].ForceMergeDeadline = &deadline
			snap.Accounts[0].Plan, snap.Accounts[0].Money = snap.NewPlan, overlapMoney(now, 96, 9)
			second := snap.Accounts[0]
			second.ID, second.ForceMergeKey = 3, "second-cycle"
			snap.Accounts = append(snap.Accounts, second)
			d := restock.Evaluate(c, snap, nil, restock.Limits{}, 0, 50, 2, now)
			d.ID = "same-upstream-account"
			// Exercise history older than ProRestockTasks' normal 48-hour window.
			d.At = now.Add(-72 * time.Hour)
			d.Snapshot.At = d.At
			// Seed historical records, not a live admission decision.
			d.DemandKnown = false
			tasks, err := s.ReserveProRestock(d, "", 0)
			if err != nil || len(tasks) != 2 {
				t.Fatal(tasks, err)
			}
			for i := range tasks {
				tasks[i].Status, tasks[i].AccountID, tasks[i].ReadyAt = "ready", 2, &now
				if err = s.SaveProRestockTask(tasks[i]); err != nil {
					t.Fatal(err)
				}
			}
			if alreadyLinked {
				if _, err = s.db.Exec("INSERT INTO pro_restock_replacements(source,task_id) VALUES(?,?)", "original-cycle", tasks[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			snap.Accounts = append(snap.Accounts, restock.Account{ID: 2, Ready: true, Capacity: 50, Plan: snap.NewPlan, Money: overlapMoney(now, 1, 0)})
			deadline = now.Add(4 * time.Minute)
			tasks, err = s.ProRestockTasks()
			if err != nil {
				t.Fatal(err)
			}
			if !alreadyLinked && len(tasks) != 0 {
				t.Fatal("fixture did not leave the history window")
			}
			tasks, err = s.ReconcileForceMerge(c, snap, tasks)
			if err != nil {
				t.Fatal(err)
			}
			stats := restock.ForceMergeSummary(c, snap, tasks, now)
			if len(tasks) != 1 || stats.Ready != 1 || len(stats.Sources) != 1 {
				t.Fatal("one upstream account covered two sources, or linked history missing from this evaluation", tasks, stats)
			}
		})
	}
}

func TestRestockOverlapMixedBatchSurplusCoversLaterSource(t *testing.T) {
	s := transferStore(t)
	c, snap := forceRestockFixture(t, s)
	c.TimeEnabled, c.ExitEnabled, c.CapacityPercent, c.MinBatch, c.ForceMergeMinutes = true, true, 100, 1, 5
	var err error
	c, err = s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	now := snap.At
	deadline := now.Add(time.Minute)
	secondDeadline := now.Add(8 * time.Minute)
	a := &snap.Accounts[0]
	a.ForceMergeDeadline = &deadline
	a.Plan, a.Money, a.Capacity, a.Used = snap.NewPlan, overlapMoney(now, 96, 7), 50, 30
	second := *a
	second.ID, second.ForceMergeKey, second.ForceMergeDeadline = 2, "second-cycle", &secondDeadline
	snap.Accounts = append(snap.Accounts, second)
	snap.ExitConfirmations = c.Confirmations
	d := restock.Evaluate(c, snap, nil, restock.Limits{}, c.Confirmations, 50, 2, now)
	d.ID = "mixed-capacity-batch"
	if d.TimeNeed != 3 || d.ForceMergeNeed != 1 || d.ExitNeed != 2 || d.Suggested != 3 {
		t.Fatal(d)
	}
	tasks, err := s.ReserveProRestock(d, "", 0)
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	for i := range tasks {
		tasks[i].Status, tasks[i].AccountID, tasks[i].ReadyAt = "ready", int64(10+i), &now
		if err = s.SaveProRestockTask(tasks[i]); err != nil {
			t.Fatal(err)
		}
		snap.Accounts = append(snap.Accounts, restock.Account{ID: int64(10 + i), Ready: true, Plan: snap.NewPlan, Capacity: 50, Money: overlapMoney(now, 1, 0)})
	}
	secondDeadline = now.Add(4 * time.Minute)
	tasks, err = s.ReconcileForceMerge(c, snap, tasks)
	if err != nil {
		t.Fatal(err)
	}
	d = restock.Evaluate(c, snap, tasks, restock.Limits{}, c.Confirmations, 50, 2, now)
	if d.Suggested != 0 || d.ForceMerge.Ready != 2 || d.Inflight != 0 {
		t.Fatal("surplus from the earlier mixed-rule batch was purchased again", d)
	}
}
