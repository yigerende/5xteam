package restock

import (
	"testing"
	"time"
)

func TestAdmissionCooldownPreservesIndependentSettings(t *testing.T) {
	c := DefaultConfig()
	c.CooldownSeconds, c.EmergencyCooldownSeconds = 0, 120
	d := Decision{Capacity: 60, Used: 61}
	if d.Cooldown(c) != 120 {
		t.Fatal("emergency cooldown was silently clamped")
	}
	d.Used = 30
	if d.Cooldown(c) != 0 {
		t.Fatal("normal zero cooldown changed")
	}
}

func TestAdmissionFiveSecondTicksRequireNewQuota(t *testing.T) {
	for _, interval := range []int{5, 10, 120} {
		c, s, now := productionFixture(t, 1, 30)
		c.CheckSeconds, c.QuotaSeconds = 5, 5
		c.TimeEnabled, c.ForceMergeEnabled, c.ConcurrencyEnabled = false, false, false
		s.Accounts[0].Money = replayMoney(now, 95, 950, 10)
		var previous Snapshot
		// Replay an hour of five-second checks, preserving the upstream sample
		// timestamp between actual refreshes. No business orders are submitted.
		for tick := 0; tick < 720; tick++ {
			s.Accounts = append([]Account(nil), s.Accounts...)
			s.At = now.Add(time.Duration(tick) * 5 * time.Second)
			if tick*5%interval == 0 {
				m := *s.Accounts[0].Money
				m.At, m.BudgetAt = s.At, s.At
				m.Windows = append([]MoneyWindow(nil), m.Windows...)
				m.Windows[0].ResetAt = now.Add(24 * time.Hour)
				s.Accounts[0].Money = &m
			}
			next, d := observeDecision(c, s, previous, nil)
			if len(next.Admission.Exit.Levels) > 0 && len(previous.Accounts) > 0 && s.Accounts[0].Money.At.Equal(previous.Accounts[0].Money.At) && previous.Admission != nil && len(previous.Admission.Exit.Levels) > 0 && next.Admission.Exit.Levels[0].Count > previous.Admission.Exit.Levels[0].Count {
				t.Fatalf("%ds upstream cache advanced at 5s tick %d", interval, tick)
			}
			if tick*5 < 2*interval && d.Suggested != 0 {
				t.Fatalf("ordered before three independent samples: interval=%d tick=%d", interval, tick)
			}
			// Slow/failing quota collection doesn't stop fixed-deadline or load rules;
			// those rules confirm fresh load separately, without counting cache as fresh.
			previous = next
		}
	}
}
