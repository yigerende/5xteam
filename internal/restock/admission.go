package restock

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type QuotaSample struct {
	At   time.Time `json:"at"`
	Used float64   `json:"used"`
}

// Every quantity has its own evidence and count. A confirmed need for one
// account cannot authorize a second account on its first observation.
type NeedProof struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}
type NeedLevel struct {
	Quantity int         `json:"quantity"`
	Count    int         `json:"count"`
	Proof    []NeedProof `json:"proof,omitempty"`
}
type NeedConfirmation struct {
	RawNeed       int         `json:"raw_need"`
	ConfirmedNeed int         `json:"confirmed_need"`
	Levels        []NeedLevel `json:"levels"`
	FastReason    string      `json:"fast_reason,omitempty"` // Historical fast-path records only.
}

// Old snapshots may claim a confirmed quantity after only one emergency
// sample. Validate the per-quantity counts when reusing those snapshots.
func (n NeedConfirmation) requireConfirmations(required int) NeedConfirmation {
	confirmed := 0
	for _, level := range n.Levels {
		if level.Count >= max(1, required) && len(level.Proof) > 0 {
			confirmed = max(confirmed, level.Quantity)
		}
	}
	n.ConfirmedNeed = min(n.ConfirmedNeed, n.RawNeed, confirmed)
	n.FastReason = ""
	return n
}

type AdmissionState struct {
	Version     int              `json:"version"`
	At          time.Time        `json:"at"`
	Concurrency NeedConfirmation `json:"concurrency"`
	Exit        NeedConfirmation `json:"exit"`
}

func concurrencyNeed(c Config, s Snapshot, tasks []Task, slots int, lead float64, now time.Time) int {
	if slots < 1 || !pressureAt(c, s, now) {
		return 0
	}
	used, capacity, waiting := exitLoad(s, now)
	// Future departures belong exclusively to ForecastExits.
	_, target := c.CapacityThresholds(capacity)
	ready := map[int64]bool{}
	for _, a := range s.Accounts {
		if availableAccount(a, now) {
			ready[a.ID] = true
		}
	}
	seen := map[string]bool{}
	seenAccounts := map[int64]bool{}
	pending := 0
	for _, t := range tasks {
		if !t.Predictable() || t.Binding != s.Binding || t.GroupID != s.GroupID || ready[t.AccountID] || (t.ID != "" && seen[t.ID]) || (t.AccountID > 0 && seenAccounts[t.AccountID]) || t.ETA.After(now.Add(time.Duration(lead*float64(time.Minute)))) {
			continue
		}
		seen[t.ID], seenAccounts[t.AccountID] = true, true
		n := t.Capacity
		if n < 1 {
			n = slots
		}
		pending += n
	}
	return max(0, int(math.Ceil((float64(used+waiting)*100/float64(max(1, target))-c.EffectiveCapacity(capacity+pending))/c.EffectiveCapacity(slots))))
}

func advanceNeed(raw, required int, previous NeedConfirmation, proof func(int) []NeedProof) NeedConfirmation {
	out := NeedConfirmation{RawNeed: raw, Levels: []NeedLevel{}}
	for n := 1; n <= min(raw, 100); n++ {
		p := proof(n)
		sort.Slice(p, func(i, j int) bool { return p[i].Key < p[j].Key })
		level := NeedLevel{Quantity: n, Count: 1, Proof: p}
		if len(p) == 0 {
			level.Count = 0
		}
		if n <= len(previous.Levels) {
			old := previous.Levels[n-1]
			same, fresh := len(p) == len(old.Proof) && len(p) > 0, true
			for i := range p {
				if i >= len(old.Proof) || p[i].Key != old.Proof[i].Key {
					same = false
					break
				}
				fresh = fresh && p[i].At.After(old.Proof[i].At)
			}
			if same {
				level.Count = old.Count
				if fresh {
					level.Count = min(required, old.Count+1)
				} else {
					level.Proof = old.Proof
				}
			}
		}
		out.Levels = append(out.Levels, level)
		if level.Count >= required {
			out.ConfirmedNeed = n
		}
	}
	return out
}

func exitProof(f ExitForecast, quantity int, loadAt time.Time) []NeedProof {
	for _, point := range f.Points {
		if point.Need < quantity {
			continue
		}
		proof := []NeedProof{{Key: "load", At: loadAt}}
		for _, r := range f.Rows {
			if !r.Risk || r.exitAt == nil || r.Evidence == nil || r.exitAt.After(point.sizingAt) {
				continue
			}
			e := r.Evidence
			at := e.SampleAt
			if e.RateAt.After(at) {
				at = e.RateAt
			}
			key := fmt.Sprintf("%d/%s/%d/%d", r.ID, e.Source, e.WindowSeconds, e.ResetAt)
			if e.Source == "deadline" || e.Source == "leaving" {
				at = loadAt
				if e.Source == "deadline" {
					key += "/" + r.exitAt.UTC().Format(time.RFC3339Nano)
				}
			}
			proof = append(proof, NeedProof{Key: key, At: at})
		}
		return proof
	}
	return nil
}

// ObserveAdmission is pure and its state travels with the persisted snapshot.
// Only new valid load samples advance counts. Batch/target/config changes reset
// confirmation, never the unaffected accounts' consumption history.
func ObserveAdmission(c Config, s, previous Snapshot, tasks []Task, slots int, lead float64, now time.Time) Snapshot {
	old := previous.Admission
	same := old != nil && old.Version == c.Version && previous.Binding == s.Binding && previous.GroupID == s.GroupID && previous.TakeoverToken == s.TakeoverToken
	if previous.Binding == s.Binding && previous.GroupID == s.GroupID && previous.TakeoverToken == s.TakeoverToken && !previous.At.IsZero() {
		s.SupplyChangedAt = previous.SupplyChangedAt
		before := map[int64]int{}
		for _, a := range previous.Accounts {
			if availableAccount(a, previous.At) {
				before[a.ID] = a.Capacity
			}
		}
		for _, a := range s.Accounts {
			if s.Valid && s.LoadValid && availableAccount(a, now) && a.Capacity > before[a.ID] {
				s.SupplyChangedAt = s.At
				break
			}
		}
	}
	s.Admission = &AdmissionState{Version: c.Version, At: s.At}
	if !exitSnapshotValid(c, s, now) || WaitForBatch(s, tasks) != nil {
		s.ExitConfirmations = 0
		return s
	}
	if same && !s.At.After(old.At) {
		copy := *old
		copy.Concurrency = copy.Concurrency.requireConfirmations(c.Confirmations)
		copy.Exit = copy.Exit.requireConfirmations(c.Confirmations)
		s.Admission = &copy
		s.ExitConfirmations = firstCount(copy.Exit)
		s.SupplyChangedAt = previous.SupplyChangedAt
		return s
	}
	if !same || s.At.Sub(old.At) > time.Duration(c.FreshSeconds)*time.Second {
		old = &AdmissionState{}
	}
	if c.ConcurrencyEnabled {
		raw := concurrencyNeed(c, s, tasks, slots, lead, now)
		s.Admission.Concurrency = advanceNeed(raw, c.Confirmations, old.Concurrency, func(int) []NeedProof { return []NeedProof{{Key: "load", At: s.At}} })
	}
	if c.ExitEnabled {
		f := ForecastExits(c, s, tasks, slots, lead, now)
		s.Admission.Exit = advanceNeed(f.Need, c.Confirmations, old.Exit, func(n int) []NeedProof { return exitProof(f, n, s.At) })
	}
	s.ExitConfirmations = firstCount(s.Admission.Exit)
	return s
}

func firstCount(n NeedConfirmation) int {
	if len(n.Levels) > 0 {
		return n.Levels[0].Count
	}
	return 0
}
func (s Snapshot) ConcurrencyConfirmations() int {
	if s.Admission == nil {
		return 0
	}
	return firstCount(s.Admission.Concurrency)
}

// Reuse the same cooling decision in evaluation and the reservation transaction.
func (d Decision) Cooldown(c Config) int {
	// Emergency changes the cooldown only; admission still requires every
	// configured confirmation for each requested quantity.
	overloaded := c.ConcurrencyEnabled && float64(d.Used+d.Waiting) > d.Capacity
	if overloaded || d.EmptyGroupNeed > 0 || d.Waiting >= c.EmergencyQueue || (c.ExitEnabled && d.ExitNeed > 0 && d.Exit != nil && d.Exit.Emergency) {
		return c.EmergencyCooldownSeconds
	}
	return c.CooldownSeconds
}
