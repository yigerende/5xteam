package restock

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func emergencyFixture(t *testing.T, used []int, waiting int) (Config, Snapshot) {
	t.Helper()
	c, s, _ := productionFixture(t, len(used), 0)
	// The November 24 production settings, including the 55% sizing target.
	c.TriggerPercent, c.TargetPercent = 60, 55
	c.CheckSeconds, c.Confirmations, c.EmergencyQueue = 5, 3, 3
	c.TimeEnabled, c.ForceMergeEnabled = false, false
	for i, n := range used {
		s.Accounts[i].Used = n
	}
	s.Accounts[len(used)-1].Waiting = waiting
	return c, s
}

func TestEmergencyConfirmationNightSpikesAndSustainedDemand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		used    []int
		waiting int
		want    int
	}{
		{"04_09_queue", []int{60}, 3, 1},
		{"05_30_queue", []int{60}, 9, 2},
		{"06_09_partial_capacity_queue", []int{53, 60}, 3, 2},
		{"overload_below_queue_threshold", []int{60}, 1, 1},
		{"overload_without_queue", []int{65}, 0, 1},
	} {
		for _, required := range []int{1, 3, 5} {
			t.Run(fmt.Sprintf("%s/confirm_%d", tc.name, required), func(t *testing.T) {
				c, s := emergencyFixture(t, tc.used, tc.waiting)
				c.Confirmations = required
				var previous Snapshot
				for round := 1; round <= required; round++ {
					s = refreshReplay(s, 5*time.Second, false)
					next, d := observeDecision(c, s, previous, nil)
					want := 0
					if round == required {
						want = tc.want
					}
					if d.Suggested != want || next.ConcurrencyConfirmations() != round {
						t.Fatalf("round %d: suggested=%d want=%d confirmation=%+v", round, d.Suggested, want, next.Admission.Concurrency)
					}
					if want > 0 && d.Cooldown(c) != c.EmergencyCooldownSeconds {
						t.Fatal("confirmed emergency lost its configured cooldown")
					}
					// Re-reading the same sample cannot advance either quantity.
					cached, again := observeDecision(c, s, next, nil)
					if again.Suggested != want || cached.ConcurrencyConfirmations() != round {
						t.Fatal("cached load advanced confirmation")
					}
					previous = next
				}
			})
		}
		t.Run(tc.name+"/spike_then_recovery", func(t *testing.T) {
			c, s := emergencyFixture(t, tc.used, tc.waiting)
			previous, d := observeDecision(c, s, Snapshot{}, nil)
			if d.Suggested != 0 {
				t.Fatalf("one spike ordered %d accounts", d.Suggested)
			}
			s = refreshReplay(s, 6*time.Second, false)
			for i := range s.Accounts {
				s.Accounts[i].Used, s.Accounts[i].Waiting = 10, 0
			}
			previous, d = observeDecision(c, s, previous, nil)
			if d.Suggested != 0 || previous.ConcurrencyConfirmations() != 0 {
				t.Fatal("recovered load kept the spike confirmation", d)
			}
			for i, n := range tc.used {
				s.Accounts[i].Used = n
			}
			s.Accounts[len(tc.used)-1].Waiting = tc.waiting
			s = refreshReplay(s, 5*time.Second, false)
			previous, d = observeDecision(c, s, previous, nil)
			if d.Suggested != 0 || previous.ConcurrencyConfirmations() != 1 {
				t.Fatal("a later spike inherited the previous spike", d)
			}
		})
	}
}

func TestEmergencySecondQuantityNeedsItsOwnConfirmations(t *testing.T) {
	c, s := emergencyFixture(t, []int{60}, 3)
	var previous Snapshot
	for round := 1; round <= 5; round++ {
		if round == 3 {
			s.Accounts[0].Waiting = 9 // Need increases from one to two.
		}
		s = refreshReplay(s, 5*time.Second, false)
		next, d := observeDecision(c, s, previous, nil)
		want := 0
		if round >= 3 {
			want = 1
		}
		if round == 5 {
			want = 2
		}
		if d.Suggested != want {
			t.Fatalf("round %d: suggested=%d want=%d proof=%+v", round, d.Suggested, want, next.Admission.Concurrency)
		}
		previous = next
	}
}

func TestEmergencyExitAlsoRequiresConsecutiveEvidence(t *testing.T) {
	for _, kind := range []string{"deadline", "quota_reached", "queued_prediction"} {
		t.Run(kind, func(t *testing.T) {
			c, s := emergencyFixture(t, []int{30}, 0)
			c.ConcurrencyEnabled = false
			switch kind {
			case "deadline":
				deadline := s.At.Add(time.Minute)
				s.Accounts[0].ExitDeadline = &deadline
			case "quota_reached":
				threshold := 99.
				s.Accounts[0].QuotaMergeThreshold = &threshold
				s.Accounts[0].Windows = []Window{{Seconds: 604800, ResetAt: s.At.Add(time.Hour).Unix(), Used: 99, At: s.At}}
			case "queued_prediction":
				s.Accounts[0].Waiting = 3
				s.Accounts[0].Money = replayMoney(s.At, 95, 950, 10)
			}
			var previous Snapshot
			for round := 1; round <= c.Confirmations; round++ {
				s = refreshReplay(s, 5*time.Second, true)
				if len(s.Accounts[0].Windows) > 0 {
					s.Accounts[0].Windows = append([]Window(nil), s.Accounts[0].Windows...)
					s.Accounts[0].Windows[0].At = s.At
				}
				next, d := observeDecision(c, s, previous, nil)
				want := 0
				if round == c.Confirmations {
					want = 1
				}
				if d.Suggested != want || d.ExitNeed != want {
					t.Fatalf("round %d: suggested=%d exit=%d want=%d", round, d.Suggested, d.ExitNeed, want)
				}
				previous = next
			}
			// A drop removes the previous confirmations even if the exit remains.
			s = refreshReplay(s, 5*time.Second, true)
			s.Accounts[0].Used, s.Accounts[0].Waiting = 0, 0
			previous, d := observeDecision(c, s, previous, nil)
			if d.Suggested != 0 || previous.ExitConfirmations != 0 {
				t.Fatal("recovered exit demand retained its confirmation", d)
			}
		})
	}
}

func TestEmergencyLegacyFastSnapshotCannotAuthorizePurchase(t *testing.T) {
	for _, kind := range []string{"concurrency", "exit"} {
		t.Run(kind, func(t *testing.T) {
			c, s := emergencyFixture(t, []int{60}, 9)
			if kind == "exit" {
				c.ConcurrencyEnabled = false
				s.Accounts[0].Used, s.Accounts[0].Waiting = 30, 0
				deadline := s.At.Add(time.Minute)
				s.Accounts[0].ExitDeadline = &deadline
			}
			previous, _ := observeDecision(c, s, Snapshot{}, nil)
			proof := &previous.Admission.Concurrency
			if kind == "exit" {
				proof = &previous.Admission.Exit
			}
			proof.ConfirmedNeed = proof.RawNeed // Persisted by the old fast path.
			proof.FastReason = "实际容量不足或紧急排队"
			raw, err := json.Marshal(previous)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &previous); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(previous)
			direct := Evaluate(c, previous, nil, Limits{}, 99, 60, 4, s.At)
			cached, d := observeDecision(c, s, previous, nil)
			if direct.Suggested != 0 || d.Suggested != 0 || cached.Admission.Concurrency.ConfirmedNeed != 0 || cached.Admission.Exit.ConfirmedNeed != 0 {
				t.Fatal("legacy fast approval survived upgrade", direct, d)
			}
			after, _ := json.Marshal(previous)
			if string(before) != string(after) {
				t.Fatal("evaluation mutated the persisted source snapshot")
			}
		})
	}
}
