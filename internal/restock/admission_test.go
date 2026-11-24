package restock

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func productionFixture(t testing.TB, count, used int) (Config, Snapshot, time.Time) {
	t.Helper()
	raw, e := os.ReadFile("testdata/production_20261121.json")
	if e != nil {
		t.Fatal(e)
	}
	var c Config
	if e = json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 11, 21, 15, 23, 27, 0, time.FixedZone("CST", 28800))
	s := Snapshot{ID: "production-replay", At: now, Valid: true, LoadValid: true, GroupID: 6, Binding: "test-target", NewPlan: "pro20"}
	for i := 0; i < count; i++ {
		a := Account{ID: int64(i + 1), Ready: true, Capacity: 60, Plan: "pro20", Used: min(60, used)}
		used -= a.Used
		s.Accounts = append(s.Accounts, a)
	}
	return c, s, now
}
func observeDecision(c Config, s, previous Snapshot, tasks []Task) (Snapshot, Decision) {
	s = ObserveAdmission(c, s, previous, tasks, 60, 4.1076, s.At)
	return s, Evaluate(c, s, tasks, Limits{}, s.ConcurrencyConfirmations(), 60, 4.1076, s.At)
}
func refreshReplay(s Snapshot, delta time.Duration, money bool) Snapshot {
	s.At = s.At.Add(delta)
	s.Accounts = append([]Account(nil), s.Accounts...)
	for i := range s.Accounts {
		if money && s.Accounts[i].Money != nil {
			m := *s.Accounts[i].Money
			m.At = s.At
			m.BudgetAt = s.At
			s.Accounts[i].Money = &m
		}
	}
	return s
}
func setDemand(s *Snapshot, used int) {
	for i := range s.Accounts {
		s.Accounts[i].Used = min(60, used)
		used -= s.Accounts[i].Used
	}
}
func replayMoney(now time.Time, percent, cost, rate float64) *AccountMoney {
	m := testMoney(now, percent, cost, rate)
	m.WindowMinutes = 1
	m.Windows[0].Seconds = 604800
	return m
}

func TestAdmissionProductionQuantitiesConfirmSeparately(t *testing.T) {
	c, s, _ := productionFixture(t, 3, 125)
	var previous Snapshot
	for i, used := range []int{125, 125, 145, 145, 145} {
		setDemand(&s, used)
		s = refreshReplay(s, 5*time.Second, false)
		next, d := observeDecision(c, s, previous, nil)
		want := []int{0, 0, 1, 1, 2}[i]
		if d.ConcurrencyNeed != want || d.Suggested != want {
			t.Fatalf("round %d need=%d suggested=%d state=%+v", i, d.ConcurrencyNeed, d.Suggested, next.Admission)
		}
		previous = next
	}
}
func TestAdmissionProduction1523StillReplenishesOne(t *testing.T) {
	c, s, now := productionFixture(t, 4, 87)
	threshold := 99.
	on := true
	for i, minutes := range []float64{7.610, 28, 105.71, 108.63} {
		deadline := now.Add(time.Duration(minutes * float64(time.Minute)))
		s.Accounts[i].ExitDeadline = &deadline
		s.Accounts[i].ForceMergeDeadline = &deadline
		s.Accounts[i].EJH = true
		s.Accounts[i].PushTimeoutEnabled = &on
		s.Accounts[i].ForceMergeKey = fmt.Sprint(i)
		s.Accounts[i].QuotaMergeThreshold = &threshold
	}
	s.Accounts[0].Money = replayMoney(now, 84, 840, 6.3)
	s.Accounts[1].Money = replayMoney(now, 87, 870, 19.7936)
	var prev Snapshot
	for i := 0; i < 3; i++ {
		s = refreshReplay(s, 15*time.Second, true)
		next, d := observeDecision(c, s, prev, nil)
		if i < 2 && d.Suggested != 0 {
			t.Fatalf("premature order: %+v", d)
		}
		if i == 2 && (d.ExitNeed != 1 || d.Suggested != 1 || d.Capacity != 240 || d.Exit.AfterCapacity != 120 || d.ForceMergeNeed != 0) {
			t.Fatalf("valid final replenishment lost: %+v", d)
		}
		prev = next
	}
}
func TestAdmissionExitCacheAndQuantityEvidence(t *testing.T) {
	c, s, now := productionFixture(t, 3, 72)
	s.Accounts[0].Money = replayMoney(now, 94, 940, 10)
	s.Accounts[1].Money = replayMoney(now, 94, 940, 10)
	var prev Snapshot
	for i := 0; i < 3; i++ {
		s = refreshReplay(s, 5*time.Second, false)
		next, d := observeDecision(c, s, prev, nil)
		if d.Suggested != 0 || next.Admission.Exit.Levels[0].Count != 1 {
			t.Fatal("cached evidence confirmed", d)
		}
		prev = next
	}
	s = refreshReplay(s, 5*time.Second, true)
	prev, _ = observeDecision(c, s, prev, nil)
	setDemand(&s, 84)
	s = refreshReplay(s, 5*time.Second, true)
	next, d := observeDecision(c, s, prev, nil)
	if d.ExitNeed != 1 || d.Suggested != 1 || next.Admission.Exit.RawNeed != 2 || next.Admission.Exit.Levels[1].Count != 1 {
		t.Fatalf("second quantity inherited confirmations: %+v", d)
	}
	prev = next
	for i := 0; i < 2; i++ {
		s = refreshReplay(s, 5*time.Second, true)
		next, d = observeDecision(c, s, prev, nil)
		prev = next
	}
	if d.ExitNeed != 2 || d.Suggested != 2 {
		t.Fatal("confirmed two delayed", d)
	}
}
func TestAdmissionDistributionAndHardDeadline(t *testing.T) {
	c, s, now := productionFixture(t, 2, 72)
	s.Accounts[0].Money = replayMoney(now, 94, 940, 20)
	s.Accounts[1].Money = replayMoney(now, 94, 940, 20)
	old, _ := observeDecision(c, s, Snapshot{}, nil)
	s = refreshReplay(s, 5*time.Second, false)
	s.Accounts = append(s.Accounts, Account{ID: 3, Ready: true, Capacity: 60})
	next, d := observeDecision(c, s, old, nil)
	if d.ExitNeed != 0 || d.Exit.AtRisk != 0 || next.SupplyChangedAt.IsZero() {
		t.Fatal("pre-arrival rate triggered another purchase", d)
	}
	// Fixed expiry keeps using fresh load samples during distribution, but
	// still needs the configured number of confirmations.
	deadline := s.At.Add(time.Minute)
	s.Accounts[0].ExitDeadline = &deadline
	s.Accounts[1].ExitDeadline = &deadline
	previous := next
	for i := 1; i <= c.Confirmations; i++ {
		s = refreshReplay(s, 5*time.Second, false)
		previous, d = observeDecision(c, s, previous, nil)
		want := 0
		if i == c.Confirmations {
			want = 1
		}
		if d.ExitNeed != want || d.Suggested != want || d.Exit.UrgentNeed != 1 {
			t.Fatalf("certain shortage confirmation %d: %+v", i, d)
		}
	}
	// New rates can be established in 30 seconds, without a universal minute wait.
	s.Accounts[0].ExitDeadline = nil
	s.Accounts[1].ExitDeadline = nil
	after := next.SupplyChangedAt
	s = refreshReplay(s, 30*time.Second, false)
	for i := 0; i < 2; i++ {
		m := replayMoney(s.At, 94, 940, 20)
		m.Samples = []MoneySample{{At: after, Cost: 0}, {At: s.At, Cost: 0}}
		s.Accounts[i].Money = m
	}
	_, d = observeDecision(c, s, next, nil)
	if d.Exit.AtRisk != 0 || d.Exit.Rows[0].PredictionPending {
		t.Fatal("did not use post-distribution rate", d.Exit)
	}
}
func TestAdmissionProductionEmergencyConfirmationAndBatchGuards(t *testing.T) {
	for _, kind := range []string{"empty", "queue", "overload", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := productionFixture(t, 2, 100)
			switch kind {
			case "empty":
				s.Accounts = nil
			case "queue":
				s.Accounts[0].Used = 57
				s.Accounts[0].Waiting = 3
			case "overload":
				s.Accounts[0].Used = 100
				s.Accounts[1].Used = 40
			case "deadline":
				deadline := now.Add(time.Minute)
				s.Accounts[0].ExitDeadline = &deadline
			}
			var previous Snapshot
			var d Decision
			for i := 1; i <= c.Confirmations; i++ {
				s = refreshReplay(s, 5*time.Second, false)
				previous, d = observeDecision(c, s, previous, nil)
				if kind != "empty" && i < c.Confirmations && d.Suggested != 0 {
					t.Fatal("emergency bypassed confirmation", d)
				}
			}
			s = previous
			now = s.At
			if d.Suggested < 1 {
				t.Fatal("confirmed shortage delayed", d)
			}
			if kind == "overload" && d.Suggested != 2 {
				t.Fatal("real shortage of two reduced", d)
			}
			for _, status := range []string{"reserved", "submitting", "running", "verifying", "unknown", "attention"} {
				tasks := []Task{{ID: "existing", Status: status, Binding: s.Binding, GroupID: s.GroupID}}
				_, blocked := observeDecision(c, s, Snapshot{}, tasks)
				if blocked.Suggested != 0 || blocked.BatchWait == nil {
					t.Fatalf("%s bypassed batch barrier", status)
				}
			}
			last := now.Add(-30 * time.Second)
			d = Evaluate(c, s, nil, Limits{LastBatch: &last}, 0, 60, 4.1076, now)
			if d.Suggested == 0 || *d.Trace.CooldownSeconds != 20 {
				t.Fatal("used ordinary cooldown after emergency confirmation", d)
			}
			for _, limits := range []Limits{{HourCount: 10}, {DayCount: 100}, {Failures: 2}} {
				if Evaluate(c, s, nil, limits, 0, 60, 4.1076, now).Suggested != 0 {
					t.Fatal("confirmed emergency bypassed hard cap")
				}
			}
		})
	}
}
func TestAdmissionProductionTimeAndForceRemainIndependent(t *testing.T) {
	for _, kind := range []string{"time", "force"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := productionFixture(t, 2, 10)
			if kind == "time" {
				for i := range s.Accounts {
					s.Accounts[i].Money = replayMoney(now, 90, 900, 20)
				}
			} else {
				on := true
				for i := range s.Accounts {
					at := now.Add(7 * time.Minute)
					s.Accounts[i].EJH = true
					s.Accounts[i].PushTimeoutEnabled = &on
					s.Accounts[i].ForceMergeDeadline = &at
					s.Accounts[i].ForceMergeKey = fmt.Sprint(i)
				}
			}
			_, d := observeDecision(c, s, Snapshot{}, nil)
			if kind == "time" && (d.TimeNeed != 1 || d.Suggested != 1) {
				t.Fatal(d)
			}
			if kind == "force" && (d.ForceMergeNeed != 2 || d.Suggested != 2) {
				t.Fatal(d)
			}
		})
	}
}
func TestAdmissionStaleFailureRestartAndScope(t *testing.T) {
	c, s, now := productionFixture(t, 3, 125)
	var prev Snapshot
	for i := 0; i < 2; i++ {
		s = refreshReplay(s, 5*time.Second, false)
		prev, _ = observeDecision(c, s, prev, nil)
	}
	raw, _ := json.Marshal(prev)
	var restored Snapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	s = refreshReplay(s, 5*time.Second, false)
	next, d := observeDecision(c, s, restored, nil)
	if d.Suggested != 1 {
		t.Fatal("restart lost valid confirmation state", d)
	}
	for _, kind := range []string{"invalid", "stale", "future", "binding", "version", "load_failure"} {
		t.Run(kind, func(t *testing.T) {
			cc, ss := c, s
			ss = refreshReplay(ss, 5*time.Second, false)
			switch kind {
			case "invalid":
				ss.Valid = false
			case "stale":
				ss.At = now.Add(-time.Hour)
			case "future":
				ss.At = now.Add(time.Hour)
			case "binding":
				ss.Binding = "other"
			case "version":
				cc.Version++
			case "load_failure":
				ss.LoadValid = false
			}
			ss = ObserveAdmission(cc, ss, next, nil, 60, 4.1076, s.At)
			dd := Evaluate(cc, ss, nil, Limits{}, ss.ConcurrencyConfirmations(), 60, 4.1076, s.At)
			if dd.Suggested != 0 {
				t.Fatal("invalid/scope-changed evidence reused", dd)
			}
		})
	}
}
func TestQuotaHistoryUsesRealTimeAndRejectsCachedReplays(t *testing.T) {
	_, _, now := productionFixture(t, 0, 0)
	base := Window{Seconds: 604800, ResetAt: now.Add(time.Hour).Unix(), Used: 80, At: now}
	windows := ObserveWindows([]Window{base}, nil, true, 3*time.Minute)
	for i := 1; i <= 6; i++ {
		n := base
		n.At = now.Add(time.Duration(i) * 10 * time.Second)
		n.Used = 80 + float64(i)
		windows = ObserveWindows([]Window{n}, windows, true, 3*time.Minute)
	}
	w := windows[0]
	if w.Rate != 6 || w.SampleSeconds != 60 {
		t.Fatal(w)
	}
	for i := 0; i < 10; i++ {
		windows = ObserveWindows([]Window{w}, windows, true, 3*time.Minute)
	}
	if windows[0].Samples != w.Samples || windows[0].Rate != w.Rate {
		t.Fatal("cache advanced samples")
	}
	late := base
	late.Used = 99
	got := ObserveWindows([]Window{late}, windows, true, 3*time.Minute)[0]
	if !got.At.Equal(w.At) || got.Used != w.Used {
		t.Fatal("out-of-order overwritten", got)
	}
}
func TestAdmissionConcurrentEvaluationsDoNotMutateSnapshots(t *testing.T) {
	c, s, now := productionFixture(t, 4, 87)
	s.Accounts[0].Money = replayMoney(now, 94, 940, 10)
	s.Accounts[1].Money = replayMoney(now, 94, 940, 10)
	previous, _ := observeDecision(c, s, Snapshot{}, nil)
	before, _ := json.Marshal(previous)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ss := refreshReplay(s, 15*time.Second, true)
				_, d := observeDecision(c, ss, previous, nil)
				if d.Suggested != 0 {
					t.Error("second observation triggered")
				}
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(previous)
	if string(before) != string(after) {
		t.Fatal("shared snapshot mutated")
	}
}

func TestAdmissionProductionSingleAccountLifecycle(t *testing.T) {
	for _, kind := range []string{"idle", "soft_pressure", "queue", "deadline", "quota_reached", "quota_prediction", "quota_error", "quota_stale", "time", "force", "after_exit_empty"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := productionFixture(t, 1, 30)
			want := 0
			confirm := false
			switch kind {
			case "idle":
				s.Accounts[0].Used = 0
			case "soft_pressure":
				s.Accounts[0].Used = 45
				want = 1
				confirm = true
			case "queue":
				s.Accounts[0].Waiting = 3
				s.Accounts[0].Used = 57
				want = 1
				confirm = true
			case "deadline":
				at := now.Add(time.Minute)
				s.Accounts[0].ExitDeadline = &at
				want = 1
				confirm = true
			case "quota_reached":
				threshold := 99.
				s.Accounts[0].QuotaMergeThreshold = &threshold
				s.Accounts[0].Windows = []Window{{Seconds: 604800, ResetAt: now.Add(time.Hour).Unix(), Used: 99, At: now}}
				want = 1
				confirm = true
			case "quota_prediction":
				s.Accounts[0].Money = replayMoney(now, 95, 950, 10)
				want = 1
				confirm = true
				c.TimeEnabled = false
			case "quota_error":
				s.Accounts[0].Money = &AccountMoney{Error: "HTTP 503", At: now}
			case "quota_stale":
				s.Accounts[0].Money = replayMoney(now.Add(-time.Hour), 95, 950, 10)
			case "time":
				s.Accounts[0].Money = replayMoney(now, 90, 900, 20)
				want = 1
			case "force":
				on := true
				at := now.Add(7 * time.Minute)
				s.Accounts[0].EJH = true
				s.Accounts[0].PushTimeoutEnabled = &on
				s.Accounts[0].ForceMergeKey = "single"
				s.Accounts[0].ForceMergeDeadline = &at
				want = 1
			case "after_exit_empty":
				s.Accounts = nil
				want = 1
			}
			var prev Snapshot
			var d Decision
			for i := 0; i < 3; i++ {
				if kind == "quota_prediction" {
					s = refreshReplay(s, 5*time.Second, true)
				} else {
					s = refreshReplay(s, time.Second, false)
				}
				if kind == "quota_reached" {
					s.Accounts[0].Windows = append([]Window(nil), s.Accounts[0].Windows...)
					s.Accounts[0].Windows[0].At = s.At
				}
				next, decision := observeDecision(c, s, prev, nil)
				prev, d = next, decision
				if confirm && i < 2 && d.Suggested != 0 {
					t.Fatal("single account reused confirmation", d)
				}
				if !confirm && d.Suggested != want {
					t.Fatalf("single account decision=%d want=%d", d.Suggested, want)
				}
			}
			if d.Suggested != want {
				t.Fatalf("final decision=%d want=%d", d.Suggested, want)
			}
		})
	}
}

func TestAdmissionProductionAccountScaleAndAllRuleSwitches(t *testing.T) {
	for _, count := range []int{1, 2, 4, 16, 64, 100} {
		for mask := 0; mask < 16; mask++ {
			t.Run(fmt.Sprintf("accounts_%d_rules_%04b", count, mask), func(t *testing.T) {
				c, s, now := productionFixture(t, count, count*48)
				c.ConcurrencyEnabled = mask&1 != 0
				c.TimeEnabled = mask&2 != 0
				c.ForceMergeEnabled = mask&4 != 0
				c.ExitEnabled = mask&8 != 0
				on := true
				for i := range s.Accounts {
					a := &s.Accounts[i]
					deadline := now.Add(7 * time.Minute)
					a.Money = replayMoney(now, 90, 900, 20)
					a.EJH = true
					a.PushTimeoutEnabled = &on
					a.ForceMergeDeadline = &deadline
					a.ExitDeadline = &deadline
					a.ForceMergeKey = fmt.Sprint(i)
				}
				var prev Snapshot
				var d Decision
				for j := 0; j < 3; j++ {
					s = refreshReplay(s, 5*time.Second, true)
					prev, d = observeDecision(c, s, prev, nil)
				}
				if (!c.ConcurrencyEnabled && d.ConcurrencyNeed != 0) || (!c.TimeEnabled && d.TimeNeed != 0) || (!c.ForceMergeEnabled && d.ForceMergeNeed != 0) || (!c.ExitEnabled && d.ExitNeed != 0) {
					t.Fatal("disabled rule created demand", d)
				}
				if d.Suggested > 2 || d.Suggested < 0 {
					t.Fatal("production batch limits bypassed", d)
				}
				if mask == 0 || count == 100 {
					if d.Suggested != 0 {
						t.Fatal("disabled rules or full pool admitted", d)
					}
				} else if d.Suggested == 0 {
					t.Fatal("needed stock was delayed", d)
				}
				if d.Trace.NetNeed != max(d.ConcurrencyNeed, d.TimeNeed, d.ForceMergeNeed, d.ExitNeed, d.ReserveNeed, d.EmptyGroupNeed) {
					t.Fatal("rule quantities were summed")
				}
			})
		}
	}
}
