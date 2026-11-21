package restock

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecisionTracePreservesSizingEvidence(t *testing.T) {
	c, s, now := fixture()
	s.Accounts[0].Used = 100
	c.MaxBatch = 2
	c.MinBatch = 1
	eta := now.Add(time.Minute)
	tasks := []Task{
		{ID: "timely", Status: "running", Binding: s.Binding, GroupID: s.GroupID, Plan: s.NewPlan, ETA: &eta},
		{ID: "unknown", Status: "unknown", Binding: s.Binding, GroupID: s.GroupID, ETA: &eta},
		{ID: "foreign", Status: "running", Binding: "other", GroupID: s.GroupID, ETA: &eta},
	}
	d := Evaluate(c, s, tasks, Limits{}, 2, 20, 2, now)
	tr := d.Trace
	if tr == nil || tr.Version != 2 || !tr.TimeTriggered || !tr.ConcurrencyTriggered {
		t.Fatalf("missing dual trigger evidence: %+v", tr)
	}
	if d.Inflight != 3 || tr.GroupInflight != 2 || tr.TimelyInflight != 1 || tr.NewAccountCapacity != 20 || tr.FutureCapacity != 20 {
		t.Fatalf("inflight dedup evidence: %+v", tr)
	}
	if tr.MoneySizing == nil || tr.MoneySizing.Pending != 1 || tr.MoneySizing.NewBudgetUSD != 100 || tr.MoneySizing.GapUSD != -80 {
		t.Fatalf("money sizing: %+v", tr.MoneySizing)
	}
	if d.TimeNeed != 0 || d.ConcurrencyNeed != 5 || tr.NetNeed != 5 || *tr.AfterMinimum != 5 || *tr.HardLimit != 2 || d.Suggested != 0 || d.BatchWait == nil {
		t.Fatalf("quantity/cap trace does not match decision: %+v", d)
	}
	if len(tr.Caps) != 5 || tr.Caps[1].Remaining != 3 || tr.EvaluationReason != d.Reason || tr.StopStage != "batch" {
		t.Fatalf("missing limit explanation: %+v", tr)
	}
	// Execution errors may replace Reason, but cannot replace the rule result.
	d.Reason = "可领取的新账号不足批量要求"
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var restored Decision
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Trace.EvaluationReason == restored.Reason || restored.Trace.NetNeed != 5 {
		t.Fatal("execution result overwrote trigger evidence")
	}
}

func TestDecisionTraceBlockedAndMinimum(t *testing.T) {
	c, s, now := fixture()
	c.TimeEnabled = false
	c.AllowSmall = false
	d := Evaluate(c, s, nil, Limits{}, 2, 20, 2, now)
	if !d.Trace.StrictMinimum || d.Trace.NetNeed != 1 || *d.Trace.AfterMinimum != 3 || d.Suggested != 3 {
		t.Fatalf("minimum explanation: %+v", d)
	}
	c.DayBudget, c.UnitCost = 1, 2
	d = Evaluate(c, s, nil, Limits{}, 2, 20, 2, now)
	if d.Trace.StopStage != "limits" || *d.Trace.HardLimit != 0 || d.Trace.AfterMinimum != nil || d.Suggested != 0 {
		t.Fatalf("budget block: %+v", d)
	}
	c.DayBudget = 0
	last := now.Add(-10 * time.Second)
	d = Evaluate(c, s, nil, Limits{LastBatch: &last}, 2, 20, 2, now)
	if d.Trace.StopStage != "cooldown" || *d.Trace.CooldownSeconds != 60 || d.Trace.CooldownRemainingSeconds != 50 || d.Trace.HardLimit != nil || d.Trace.NetNeed != 1 {
		t.Fatalf("cooldown trace: %+v", d)
	}
	s.LoadValid = false
	d = Evaluate(c, s, nil, Limits{}, 2, 20, 2, now)
	if d.DemandKnown || d.Trace.TimeTriggered || d.Trace.ConcurrencyTriggered || d.Trace.StopStage != "data" || d.Trace.AfterMinimum != nil {
		t.Fatal("failed collection presented as evaluated rules")
	}
}

func TestDecisionTraceDailyPauseRetainsDemand(t *testing.T) {
	c, s, now := fixture()
	// All valid configurations are in this interval at local noon.
	now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s.At = now
	s.Accounts = nil
	c.NoRestockEnabled, c.NoRestockStart, c.NoRestockEnd = true, "11:00", "13:00"
	c.EmptyGroupCount = 2
	d := Evaluate(c, s, nil, Limits{}, 0, 10, 2, now)
	if d.Suggested != 0 || !d.NoRestockActive || d.Trace.NetNeed != 2 || d.Trace.StopStage != "daily_pause" || d.Trace.EvaluationReason != d.Reason {
		t.Fatalf("daily pause lost demand or reason: %+v", d)
	}
}
