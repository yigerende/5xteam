package restock

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEmptyGroupStartup(t *testing.T) {
	c, s, now := fixture()
	s.Accounts = nil
	c.AllowSmall = false
	check := func(want int, tasks ...Task) Decision {
		t.Helper()
		d := Evaluate(c, s, tasks, Limits{}, 0, 20, 5, now)
		if d.Suggested != want {
			t.Fatalf("want %d: %+v", want, d)
		}
		return d
	}
	check(0) // Existing configurations remain off.
	c.EmptyGroupCount = 1
	if d := check(1); d.EmptyGroupNeed != 1 || !d.EmptyGroupOnly() || d.SupplyMinutes != nil {
		t.Fatal(d)
	}
	for _, status := range []string{"reserved", "submitting", "running", "verifying", "unknown", "attention", "external"} {
		task := Task{Status: status, GroupID: s.GroupID, Binding: s.Binding}
		d := check(0, task) // Missing ETA must never cause a duplicate startup.
		if d.Inflight != 1 || d.Capacity != 0 || d.Usable != 0 {
			t.Fatal(d)
		}
		far := now.Add(time.Hour)
		task.ETA = &far
		check(0, task)
	}
	for _, status := range []string{"ready", "failed", "cancelled"} {
		check(1, Task{Status: status, GroupID: s.GroupID, Binding: s.Binding})
	}
	// The batch barrier also waits for work started before a group/target change.
	check(0, Task{Status: "running", GroupID: s.GroupID + 1, Binding: s.Binding})
	check(0, Task{Status: "running", GroupID: s.GroupID, Binding: "other"})
	c.EmptyGroupCount = 3
	check(0, Task{Status: "running", GroupID: s.GroupID, Binding: s.Binding}, Task{Status: "unknown", GroupID: s.GroupID, Binding: s.Binding})
	c.Reserve = 4
	if d := check(4); d.EmptyGroupNeed != 3 || d.ReserveNeed != 4 || d.EmptyGroupOnly() {
		t.Fatal(d)
	}
	c.Reserve = 0
	s.Accounts = []Account{{ID: 1, Ready: false}}
	if d := check(0); d.EmptyGroupNeed != 0 {
		t.Fatal("unusable member was treated as absent")
	}
}

func TestEmptyGroupStartupDataAndHardLimits(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Config, *Snapshot, *Limits)
		want   int
	}{
		{"invalid snapshot", func(c *Config, s *Snapshot, l *Limits) { s.Valid = false }, 0},
		{"invalid load", func(c *Config, s *Snapshot, l *Limits) { s.LoadValid = false }, 0},
		{"stale", func(c *Config, s *Snapshot, l *Limits) { s.At = s.At.Add(-time.Hour) }, 0},
		{"future", func(c *Config, s *Snapshot, l *Limits) { s.At = s.At.Add(time.Hour) }, 0},
		{"batch cap", func(c *Config, s *Snapshot, l *Limits) { c.MaxBatch = 3 }, 3},
		{"inflight cap", func(c *Config, s *Snapshot, l *Limits) { c.MaxInflight = 2 }, 2},
		{"pool cap", func(c *Config, s *Snapshot, l *Limits) { c.PoolLimit = 2 }, 2},
		{"hour cap", func(c *Config, s *Snapshot, l *Limits) { l.HourCount = c.HourLimit - 1 }, 1},
		{"day cap", func(c *Config, s *Snapshot, l *Limits) { l.DayCount = c.DayLimit }, 0},
		{"budget", func(c *Config, s *Snapshot, l *Limits) { c.UnitCost = 10; c.DayBudget = 10 }, 1},
		{"breaker", func(c *Config, s *Snapshot, l *Limits) { l.Failures = c.FailureLimit }, 0},
		{"cooldown", func(c *Config, s *Snapshot, l *Limits) { at := s.At.Add(-time.Second); l.LastBatch = &at }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, s, now := fixture()
			s.Accounts = nil
			c.EmptyGroupCount = 8
			c.AllowSmall = false
			l := Limits{}
			tc.change(&c, &s, &l)
			d := Evaluate(c, s, nil, l, 0, 20, 5, now)
			if d.Suggested != tc.want {
				t.Fatalf("want %d: %+v", tc.want, d)
			}
		})
	}
}

func TestEmptyGroupConfigCompatibility(t *testing.T) {
	c := DefaultConfig()
	if err := json.Unmarshal([]byte(`{"reserve":0}`), &c); err != nil || c.EmptyGroupCount != 0 {
		t.Fatal(c, err)
	}
	for _, count := range []int{-1, 1001} {
		c.EmptyGroupCount = count
		if c.Validate() == nil {
			t.Fatalf("accepted %d", count)
		}
	}
	for _, count := range []int{0, 1, 1000} {
		c.EmptyGroupCount = count
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmptyGroupUsesPrimaryOAuthCount(t *testing.T) {
	c, s, now := fixture()
	c.EmptyGroupCount = 1
	c.Reserve = 0
	c.TimeEnabled = false
	c.ConcurrencyEnabled = false
	zero, one := 0, 1
	s.Accounts = []Account{{ID: 1, Ready: true, Capacity: 100}} // API key or other takeover group.
	s.MonitorOAuthCount = &zero
	d := Evaluate(c, s, nil, Limits{}, 0, 20, 5, now)
	if d.EmptyGroupNeed != 1 || d.Suggested != 1 {
		t.Fatal("API key blocked OAuth startup", d)
	}
	s.MonitorOAuthCount = &one
	s.Accounts[0].Ready = false
	d = Evaluate(c, s, nil, Limits{}, 0, 20, 5, now)
	if d.EmptyGroupNeed != 0 {
		t.Fatal("disabled OAuth treated as absent", d)
	}
	s.MonitorOAuthCount = nil
	s.MonitorOAuthError = "unknown type"
	s.Accounts = nil
	if d = Evaluate(c, s, nil, Limits{}, 0, 20, 5, now); d.EmptyGroupNeed != 0 || d.Suggested != 0 {
		t.Fatal("unknown was treated as empty", d)
	}
}
