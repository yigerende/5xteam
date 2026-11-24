package restock

import (
	"encoding/json"
	"testing"
	"time"
)

func exitFixture() (Config, Snapshot, time.Time) {
	c, s, now := fixture()
	c.TimeEnabled, c.ConcurrencyEnabled, c.ExitEnabled = false, false, true
	c.MinBatch, c.MaxBatch, c.ExitMinutes = 1, 5, 5
	s.Accounts = []Account{
		{ID: 1, Ready: true, Capacity: 50, Used: 30, Money: testMoney(now, 96, 96, 1)},
		{ID: 2, Ready: true, Capacity: 50, Used: 30, Money: testMoney(now, 20, 20, 1)},
	}
	s.ExitConfirmations = 2
	return c, s, now
}

func TestExitIndependentTriggerAndDemandDrop(t *testing.T) {
	c, s, now := exitFixture()
	d := Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
	if d.ExitNeed != 1 || d.Suggested != 1 || d.ConcurrencyNeed != 0 || d.TimeNeed != 0 || d.Exit.AfterCapacity != 50 || !d.Exit.Emergency {
		t.Fatalf("60/100 -> 60/50 did not independently trigger: %+v", d)
	}
	if *d.Exit.Rows[0].DepletionMinutes != 4 || *d.Exit.Rows[0].ExitMinutes != 4 {
		t.Fatal(d.Exit.Rows)
	}
	s.Accounts[0].Used, s.Accounts[1].Used = 10, 10
	d = Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
	if d.Suggested != 0 || d.Exit.Pressure {
		t.Fatal("low demand replaced every exiting account", d)
	}
	s.Accounts[0].Used, s.Accounts[1].Used = 30, 30
	c.ExitMinutes = 3
	d = Evaluate(c, s, nil, Limits{}, 0, 50, 10, now)
	if d.ExitNeed != 0 || d.Exit.AtRisk != 0 {
		t.Fatal("lead silently overrode the manual advance window", d)
	}
	c.ExitEnabled = false
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).Suggested != 0 {
		t.Fatal("disabled exit rule ordered")
	}
}

func TestExitThresholdDeadlineResetAndUnknown(t *testing.T) {
	c, s, now := exitFixture()
	a := s.Accounts[0]
	threshold := 90.
	a.QuotaMergeThreshold = &threshold
	a.Money = testMoney(now, 86, 86, 2)
	r := exitRow(c, a, now)
	if *r.ExitMinutes != 2 || *r.DepletionMinutes != 7 || r.Reason != "达到额度合并阈值" {
		t.Fatal(r)
	}
	deadline := now.Add(time.Minute)
	a.ExitDeadline = &deadline
	if *exitRow(c, a, now).ExitMinutes != 1 {
		t.Fatal("deadline ignored")
	}
	a.ExitDeadline = nil
	a.Money.Windows[0].ResetAt = now.Add(time.Minute)
	if exitRow(c, a, now).Risk {
		t.Fatal("window resetting first counted as exit")
	}
	a.Money.At = now.Add(-3 * time.Minute)
	r = exitRow(c, a, now)
	if r.Risk || r.Known {
		t.Fatal("stale sample triggered exit", r)
	}
	a.Money = nil
	a.Windows = []Window{{Seconds: 604800, ResetAt: now.Add(time.Hour).Unix(), Used: 86, At: now}}
	r = exitRow(c, a, now)
	if !r.Risk || !r.WarningOnly || r.ExitMinutes != nil {
		t.Fatal("missing rate should warn without inventing ETA", r)
	}
	a.Windows[0].Samples = 2
	if exitRow(c, a, now).Risk {
		t.Fatal("confirmed zero consumption must not imply imminent exhaustion")
	}
	a.Leaving = true
	if *exitRow(c, a, now).ExitMinutes != 0 {
		t.Fatal("actual exit not immediate")
	}
}

func TestExitEvidenceTracksTheWinningPrediction(t *testing.T) {
	c, s, now := exitFixture()
	a := s.Accounts[0]
	r := exitRow(c, a, now)
	if r.Evidence == nil || r.Evidence.Source != "money" || *r.Evidence.Rate != 1 || !r.Evidence.SampleAt.Equal(now) {
		t.Fatal("money forecast evidence missing", r)
	}
	deadline := now.Add(time.Minute)
	a.ExitDeadline = &deadline
	if r = exitRow(c, a, now); r.Evidence.Source != "deadline" || *r.ExitMinutes != 1 {
		t.Fatal("earlier deadline retained a different prediction's evidence", r)
	}
	a.ExitDeadline, a.Money = nil, nil
	a.Windows = []Window{{Seconds: 604800, ResetAt: now.Add(time.Hour).Unix(), Used: 80, Rate: 4, Samples: 3, SampleSeconds: 12, At: now}}
	r = exitRow(c, a, now)
	if r.Evidence.Source != "quota_percent" || *r.Evidence.Rate != 4 || r.Evidence.SampleSeconds != 12 {
		t.Fatal("percent fallback not distinguished", r)
	}
	a.Leaving = true
	if exitRow(c, a, now).Evidence.Source != "leaving" {
		t.Fatal("actual departure did not replace forecast evidence")
	}
	d := Evaluate(c, s, nil, Limits{}, 2, 50, 2, now)
	raw, err := json.Marshal(d)
	var saved Decision
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Exit.Rows[0].Evidence.Source != "money" || len(saved.Exit.Points) == 0 || saved.Exit.Points[0].TargetPercent == 0 {
		t.Fatal("decision persistence lost historical calculation inputs", err)
	}
}

func TestExitTimelinesDeduplicateInflightAndLateArrival(t *testing.T) {
	c, s, now := exitFixture()
	s.Accounts[0].Money = testMoney(now, 99, 99, 1) // leaves in one minute
	eta := now.Add(2 * time.Minute)
	task := Task{ID: "replacement", Status: "running", Binding: s.Binding, GroupID: s.GroupID, Capacity: 50, ETA: &eta}
	d := Evaluate(c, s, []Task{task, task}, Limits{}, 0, 50, 5, now)
	if d.ExitNeed != 0 || !d.Exit.TransientGap || d.Exit.Points[0].LaterPending != 1 || d.Exit.Points[0].SizingCapacity != 100 {
		t.Fatal("late but earlier-than-new arrival duplicated", d.Exit)
	}
	eta = now.Add(30 * time.Second)
	d = Evaluate(c, s, []Task{task}, Limits{}, 0, 50, 5, now)
	if d.ExitNeed != 0 || d.Exit.TransientGap || d.Exit.Points[0].Pending != 1 {
		t.Fatal("timely inflight not counted", d.Exit)
	}
	task.Status = "unknown"
	d = Evaluate(c, s, []Task{task}, Limits{}, 0, 50, 5, now)
	if d.ExitNeed != 1 || d.Inflight != 1 {
		t.Fatal("unknown payment promised supply or lost occupancy", d)
	}
	task.Status = "running"
	task.Binding = "foreign"
	if Evaluate(c, s, []Task{task}, Limits{}, 0, 50, 5, now).ExitNeed != 1 {
		t.Fatal("foreign task counted")
	}
	task.Binding = s.Binding
	task.AccountID = 2
	if Evaluate(c, s, []Task{task}, Limits{}, 0, 50, 5, now).ExitNeed != 1 {
		t.Fatal("ready account counted twice")
	}
	s.Accounts[1].Money = testMoney(now, 97, 97, 1)
	d = Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
	if d.ExitNeed != 2 || len(d.Exit.Points) != 2 {
		t.Fatal("multiple exits not combined by timeline", d.Exit)
	}
}

func TestExitConfirmationsLimitsAndHistoricalEvidence(t *testing.T) {
	c, s, now := exitFixture()
	s.Accounts[0].Used, s.Accounts[1].Used = 22, 23 // 45/50=90%, no hard shortage
	s.ExitConfirmations = 0
	d := Evaluate(c, s, nil, Limits{}, 100, 50, 2, now)
	if !d.Exit.Pressure || d.Exit.Confirmed || d.ExitNeed != 0 {
		t.Fatal("current confirmations leaked into exit rule", d)
	}
	s.ExitConfirmations = 2
	d = Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
	if d.ExitNeed != 1 || d.Suggested != 1 {
		t.Fatal(d)
	}
	c.AllowSmall = false
	c.MinBatch = 3
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).Suggested != 3 {
		t.Fatal("minimum batch ignored")
	}
	c.DayBudget, c.UnitCost = 1, 2
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).Suggested != 0 {
		t.Fatal("exit bypassed budget")
	}
	c.DayBudget = 0
	if Evaluate(c, s, nil, Limits{Failures: c.FailureLimit}, 0, 50, 2, now).Suggested != 0 {
		t.Fatal("exit bypassed breaker")
	}
	s.LoadValid = false
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).Suggested != 0 {
		t.Fatal("missing load ordered")
	}
	s.LoadValid = true
	c.AllowSmall = true
	d = Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var restored Decision
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ExitNeed != 1 || restored.Exit.Points[0].Capacity != 50 || *restored.Exit.Rows[0].ExitMinutes != 4 || restored.Config.ExitMinutes != 5 {
		t.Fatal("missing saved evidence", restored)
	}
}

func TestExitDemandMemoryAndRecovery(t *testing.T) {
	c, s, now := exitFixture()
	s.ExitDemand = ObserveExitDemand(c, s, Snapshot{}, now)
	current := s
	current.Accounts = append([]Account(nil), s.Accounts[1:]...)
	current.Accounts[0].Used = 50
	current.At = now.Add(3 * time.Second)
	current.ExitDemand = ObserveExitDemand(c, current, s, current.At)
	if current.ExitDemand == nil || !current.ExitDemand.Held || current.ExitDemand.Value != 60 {
		t.Fatal("capacity-limited usage lost demand", current.ExitDemand)
	}
	d := Evaluate(c, current, nil, Limits{}, 0, 50, 2, current.At)
	current.ExitConfirmations = 2
	d = Evaluate(c, current, nil, Limits{}, 0, 50, 2, current.At)
	if d.ExitNeed != 1 || !d.Exit.DemandHeld {
		t.Fatal(d)
	}
	previous := current
	current.At = current.At.Add(3 * time.Second)
	current.Accounts = append([]Account(nil), current.Accounts...)
	current.Accounts[0].Used = 20
	current.ExitDemand = ObserveExitDemand(c, current, previous, current.At)
	if !current.ExitDemand.Held {
		t.Fatal("one low sample cleared demand")
	}
	previous = current
	current.At = current.At.Add(3 * time.Second)
	current.ExitDemand = ObserveExitDemand(c, current, previous, current.At)
	if current.ExitDemand.Held || current.ExitDemand.Value != 20 {
		t.Fatal("sustained lower demand did not release memory")
	}
	current.Binding = "changed"
	current.ExitDemand = ObserveExitDemand(c, current, s, current.At)
	if current.ExitDemand.Held {
		t.Fatal("foreign binding retained demand")
	}
	current = s
	current.At = now.Add(61 * time.Second)
	current.Accounts = append([]Account(nil), s.Accounts[1:]...)
	current.Accounts[0].Used = 50
	if ObserveExitDemand(c, current, s, current.At).Held {
		t.Fatal("expired memory retained")
	}
}

func TestExitDynamicPressureNeedsConsecutiveSamples(t *testing.T) {
	c, s, now := exitFixture()
	s.ExitConfirmations = 0
	for _, checks := range []int{0, 1} {
		s.ExitConfirmations = checks
		d := Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
		if d.ExitNeed != 0 || !d.Exit.Pressure {
			t.Fatal("single demand spike ordered", d)
		}
	}
	s.ExitConfirmations = 2
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).ExitNeed != 1 {
		t.Fatal("sustained pressure ignored")
	}
	s.ExitConfirmations = 0
	s.Accounts[0].Waiting = c.EmergencyQueue
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).ExitNeed != 0 {
		t.Fatal("actual account queue bypassed confirmation")
	}
	s.ExitConfirmations = c.Confirmations
	if Evaluate(c, s, nil, Limits{}, 0, 50, 2, now).ExitNeed == 0 {
		t.Fatal("confirmed account queue ignored")
	}
}

func TestExitCooldownPauseAndNewerQuota(t *testing.T) {
	c, s, now := exitFixture()
	last := now.Add(-30 * time.Second)
	d := Evaluate(c, s, nil, Limits{LastBatch: &last}, 0, 50, 2, now)
	if d.Suggested != 1 || *d.Trace.CooldownSeconds != c.EmergencyCooldownSeconds {
		t.Fatal("confirmed future shortage must use emergency cooldown", d)
	}
	s.Accounts[0].Used, s.Accounts[1].Used = 22, 23
	d = Evaluate(c, s, nil, Limits{LastBatch: &last}, 0, 50, 2, now)
	if d.Suggested != 0 || *d.Trace.CooldownSeconds != c.CooldownSeconds {
		t.Fatal("ordinary future pressure bypassed normal cooldown", d)
	}
	c.NoRestockEnabled = true
	c.NoRestockStart = now.In(scheduleLocation).Format("15:04")
	c.NoRestockEnd = now.Add(time.Minute).In(scheduleLocation).Format("15:04")
	d = Evaluate(c, s, nil, Limits{}, 0, 50, 2, now)
	if d.ExitNeed != 1 || d.Suggested != 0 || !d.NoRestockActive {
		t.Fatal("pause must retain evidence while suppressing new tasks", d)
	}
	a := s.Accounts[0]
	a.Windows = []Window{{Seconds: 604800, ResetAt: now.Add(time.Hour).Unix(), Used: 99, At: now.Add(-30 * time.Second)}}
	a.Money = testMoney(now, 1, 1, 1)
	a.Money.RateUSD = nil
	if r := exitRow(c, a, now); r.Risk {
		t.Fatal("old near-threshold sample overrode a newer reset sample", r)
	}
}

func TestExitDemandRestartInvalidAndRepeatedSamples(t *testing.T) {
	c, s, now := exitFixture()
	s.ExitDemand = ObserveExitDemand(c, s, Snapshot{}, now)
	raw, _ := json.Marshal(s)
	var previous Snapshot
	if err := json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	current := previous
	current.At = now.Add(3 * time.Second)
	current.Accounts = append([]Account(nil), s.Accounts[1:]...)
	current.Accounts[0].Used = 50
	current.ExitDemand = ObserveExitDemand(c, current, previous, current.At)
	if !current.ExitDemand.Held || current.ExitDemand.Value != 60 {
		t.Fatal("restart lost pre-exit demand", current.ExitDemand)
	}
	previous = current
	current.Accounts[0].Used = 10
	if got := ObserveExitDemand(c, current, previous, current.At); got.LowSamples != 0 || !got.Held {
		t.Fatal("same snapshot counted as a new low sample", got)
	}
	current.Valid = false
	current.At = current.At.Add(3 * time.Second)
	current.ExitDemand = ObserveExitDemand(c, current, previous, current.At)
	if current.ExitDemand.Value != 60 || ForecastExits(c, current, nil, 50, 2, current.At).Valid {
		t.Fatal("collection failure erased history or allowed decisions")
	}
}

func TestExitRefreshAndConfigCompatibility(t *testing.T) {
	c := DefaultConfig()
	if err := json.Unmarshal([]byte(`{"version":8,"enabled":false}`), &c); err != nil || c.ExitEnabled || c.ExitMinutes != 5 || c.ExitRefreshSeconds != 15 || c.Validate() != nil {
		t.Fatal("legacy config changed behavior", c, err)
	}
	c, s, now := exitFixture()
	if ExitRefreshInterval(c, s, now) != 15*time.Second {
		t.Fatal("near-exit budget did not refresh faster")
	}
	c.ExitEnabled = false
	if ExitRefreshInterval(c, s, now) != time.Minute {
		t.Fatal("disabled rule changed original cadence")
	}
	c.ExitRefreshSeconds = 61
	if c.Validate() == nil {
		t.Fatal("accepted refresh interval different from actual cadence")
	}
}
