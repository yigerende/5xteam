package restock

import (
	"math"
	"sort"
	"time"
)

// ExitDemand is a bounded memory of demand observed before capacity disappeared.
// It travels with the saved snapshot so restart cannot turn saturation into a
// false traffic drop. Values are never inferred from user concurrency limits.
type ExitDemand struct {
	Version    int       `json:"version"`
	Value      int       `json:"value"`
	At         time.Time `json:"at"`
	Capacity   int       `json:"capacity"`
	LowSamples int       `json:"low_samples"`
	Held       bool      `json:"held"`
}

type ExitEvidence struct {
	RateAt        time.Time `json:"rate_at,omitempty"`
	ResetAt       int64     `json:"reset_at,omitempty"`
	Certain       bool      `json:"certain,omitempty"`
	Source        string    `json:"source"`
	SampleAt      time.Time `json:"sample_at,omitempty"`
	WindowSeconds int64     `json:"window_seconds,omitempty"`
	Rate          *float64  `json:"rate,omitempty"`
	SampleSeconds float64   `json:"sample_seconds,omitempty"`
	Samples       int       `json:"samples,omitempty"`
}

type ExitRow struct {
	PredictionPending bool          `json:"prediction_pending,omitempty"`
	Evidence          *ExitEvidence `json:"evidence,omitempty"`
	ID                int64         `json:"id"`
	Name              string        `json:"name"`
	Capacity          float64       `json:"capacity"`
	DepletionMinutes  *float64      `json:"depletion_minutes,omitempty"`
	ExitMinutes       *float64      `json:"exit_minutes,omitempty"`
	Reason            string        `json:"reason"`
	Threshold         *float64      `json:"threshold,omitempty"`
	UsedPercent       *float64      `json:"used_percent,omitempty"`
	Risk              bool          `json:"risk"`
	WarningOnly       bool          `json:"warning_only"`
	Known             bool          `json:"known"`
	// Internal timeline, never reconstructed from rounded UI values.
	exitAt           *time.Time
	reportedCapacity int
}

type ExitPoint struct {
	sizingAt        time.Time
	TargetPercent   int     `json:"target_percent,omitempty"`
	Minutes         float64 `json:"minutes"`
	Capacity        float64 `json:"capacity"`
	Pending         int     `json:"pending"`
	PendingCapacity float64 `json:"pending_capacity"`
	SizingMinutes   float64 `json:"sizing_minutes"`
	SizingCapacity  float64 `json:"sizing_capacity"`
	LaterPending    int     `json:"later_pending"`
	Need            int     `json:"need"`
	Gap             bool    `json:"gap"`
}

type ExitForecast struct {
	UrgentNeed      int               `json:"urgent_need,omitempty"`
	Confirmation    *NeedConfirmation `json:"confirmation,omitempty"`
	Rows            []ExitRow         `json:"rows"`
	Points          []ExitPoint       `json:"points"`
	Valid           bool              `json:"valid"`
	Complete        bool              `json:"complete"`
	Demand          int               `json:"demand"`
	ObservedDemand  int               `json:"observed_demand"`
	DemandHeld      bool              `json:"demand_held"`
	CurrentCapacity float64           `json:"current_capacity"`
	AfterCapacity   float64           `json:"after_capacity"`
	AtRisk          int               `json:"at_risk"`
	Unknown         int               `json:"unknown"`
	Pressure        bool              `json:"pressure"`
	Emergency       bool              `json:"emergency"`
	Confirmed       bool              `json:"confirmed"`
	Confirmations   int               `json:"confirmations"`
	Need            int               `json:"need"`
	TransientGap    bool              `json:"transient_gap"`
	EarliestMinutes *float64          `json:"earliest_minutes,omitempty"`
}

func exitLoad(s Snapshot, now time.Time) (used, capacity, waiting int) {
	for _, a := range s.Accounts {
		if availableAccount(a, now) {
			capacity += a.Capacity
		}
		if availableAccount(a, now) || (a.SupplyExcluded && a.Ready) {
			used += a.Used
		}
		waiting += a.Waiting
	}
	return
}

func exitSnapshotValid(c Config, s Snapshot, now time.Time) bool {
	return s.Valid && s.LoadValid && !s.At.IsZero() && !s.At.After(now.Add(5*time.Second)) && now.Sub(s.At) <= time.Duration(c.FreshSeconds)*time.Second
}

func ObserveExitDemand(c Config, current, previous Snapshot, now time.Time) *ExitDemand {
	if !c.ExitEnabled {
		return nil
	}
	if !exitSnapshotValid(c, current, now) {
		p := previous.ExitDemand
		if p != nil && p.Version == c.Version && previous.Binding == current.Binding && previous.GroupID == current.GroupID && !p.At.After(now) && now.Sub(p.At) <= time.Duration(c.ExitDemandHoldSeconds)*time.Second {
			copy := *p
			return &copy
		}
		return nil
	}
	u, capacity, waiting := exitLoad(current, now)
	d := &ExitDemand{Version: c.Version, Value: u + waiting, At: current.At, Capacity: capacity}
	p := previous.ExitDemand
	if p == nil || p.Version != c.Version || previous.Binding != current.Binding || previous.GroupID != current.GroupID || p.At.IsZero() || p.At.After(now.Add(5*time.Second)) || now.Sub(p.At) > time.Duration(c.ExitDemandHoldSeconds)*time.Second {
		return d
	}
	if !current.At.After(previous.At) {
		copy := *p
		return &copy
	}
	if p.Value <= d.Value {
		return d
	}
	if capacity >= p.Capacity && !p.Held {
		return d
	}
	low := 0
	if capacity > 0 && waiting == 0 && (u == 0 || u < c.ConcurrencyTrigger(capacity)) {
		low = p.LowSamples + 1
	}
	if low >= 2 {
		return d
	}
	return &ExitDemand{Version: c.Version, Value: p.Value, At: p.At, Capacity: p.Capacity, LowSamples: low, Held: true}
}

func exitRow(c Config, a Account, now time.Time) ExitRow {
	r := ExitRow{ID: a.ID, Name: a.Name, Capacity: c.EffectiveCapacity(a.Capacity), reportedCapacity: a.Capacity, Reason: "额度样本不足", Threshold: a.QuotaMergeThreshold}
	setExit := func(at time.Time, reason string, warning bool, evidence *ExitEvidence) {
		if r.exitAt == nil || at.Before(*r.exitAt) || (at.Equal(*r.exitAt) && !warning) {
			r.exitAt = &at
			r.ExitMinutes = moneyPtr(math.Max(0, at.Sub(now).Minutes()))
			r.Reason = reason
			r.WarningOnly = warning
			r.Evidence = evidence
		}
	}
	// Dollars and utilization are paired within a single account/window, as in
	// the existing money forecast. A reset before depletion cancels that event.
	seen := map[int64]bool{}
	if a.Money != nil {
		for _, w := range a.Money.Windows {
			copyMoney := *a.Money
			copyMoney.Windows = []MoneyWindow{w}
			copyAccount := a
			copyAccount.Money = &copyMoney
			row := accountMoneyRow(copyAccount, now, moneyWindowMinutes(c.MoneyWindowMinutes))
			sampleAt := a.Money.BudgetAt
			if sampleAt.IsZero() {
				sampleAt = a.Money.At
			}
			evidence := &ExitEvidence{Source: "money", SampleAt: sampleAt, RateAt: a.Money.At, ResetAt: w.ResetAt.Unix(), WindowSeconds: w.Seconds, Rate: row.RateUSD, SampleSeconds: float64(moneyWindowMinutes(c.MoneyWindowMinutes)) * 60, Samples: len(a.Money.Samples)}
			if a.Money.RateSeconds > 0 {
				evidence.SampleSeconds = a.Money.RateSeconds
			}
			// Fresh usage can prove a reached threshold even before a rate exists.
			if row.Reason == "采样中" && row.TotalUSD != nil {
				at := a.Money.BudgetAt
				if at.IsZero() {
					at = a.Money.At
				}
				threshold := 100.
				if w.Seconds == 18000 && a.Quota5HThreshold != nil {
					threshold = *a.Quota5HThreshold
				}
				reason := "额度已耗完"
				if w.Seconds == 604800 && a.QuotaMergeThreshold != nil {
					threshold = *a.QuotaMergeThreshold
					reason = "达到额度合并阈值"
					r.UsedPercent = moneyPtr(w.UsedPercent)
				}
				if w.UsedPercent >= threshold {
					evidence.Certain = true
					r.Known = true
					setExit(now, reason, false, evidence)
				} else if c.ExitWarningPercent > 0 && threshold-w.UsedPercent <= float64(c.ExitWarningPercent) && now.Add(time.Duration(c.ExitMinutes)*time.Minute).Before(w.ResetAt) {
					setExit(now.Add(time.Duration(c.ExitMinutes)*time.Minute), "接近额度阈值，退出时间暂不可估", true, evidence)
				}
				// A newer budget/reset sample supersedes an older percent reading.
				for _, old := range a.Windows {
					if old.Seconds == w.Seconds && !old.At.After(at) {
						seen[w.Seconds] = true
					}
				}
			}
			if row.Reason != "" {
				continue
			}
			seen[w.Seconds] = true
			r.Known = true
			at := a.Money.BudgetAt
			if at.IsZero() {
				at = a.Money.At
			}
			threshold := 100.
			if w.Seconds == 18000 && a.Quota5HThreshold != nil {
				threshold = *a.Quota5HThreshold
			}
			reason := "额度预计耗完"
			if w.Seconds == 604800 && a.QuotaMergeThreshold != nil {
				threshold = *a.QuotaMergeThreshold
				reason = "达到额度合并阈值"
				r.UsedPercent = moneyPtr(w.UsedPercent)
			}
			if threshold <= 0 || threshold > 100 {
				continue
			}
			if *row.RateUSD > 0 && moneyNumber(*row.RemainingUSD / *row.RateUSD) && *row.RemainingUSD / *row.RateUSD < 525600 {
				depleted := at.Add(time.Duration(*row.RemainingUSD / *row.RateUSD * float64(time.Minute)))
				if depleted.Before(w.ResetAt) {
					left := math.Max(0, depleted.Sub(now).Minutes())
					if r.DepletionMinutes == nil || left < *r.DepletionMinutes {
						r.DepletionMinutes = moneyPtr(left)
					}
				}
			}
			gap := math.Max(0, *row.TotalUSD*threshold/100-*row.UsedUSD)
			if gap == 0 {
				evidence.Certain = true
				setExit(now, reason, false, evidence)
			} else if *row.RateUSD > 0 {
				minutes := gap / *row.RateUSD
				if moneyNumber(minutes) && minutes < 525600 {
					exit := at.Add(time.Duration(minutes * float64(time.Minute)))
					if exit.Before(w.ResetAt) {
						setExit(exit, reason, false, evidence)
					}
				}
			} else if r.exitAt == nil {
				r.Reason = "近期无消耗"
			}
		}
	}
	// Percent samples are a fallback, never mixed across plans or reset windows.
	for _, w := range a.Windows {
		if seen[w.Seconds] || !w.Fresh(now, c.QuotaFreshness()) {
			continue
		}
		evidence := &ExitEvidence{Source: "quota_percent", SampleAt: w.At, ResetAt: w.ResetAt, WindowSeconds: w.Seconds, SampleSeconds: w.SampleSeconds, Samples: w.Samples}
		if w.Samples > 0 {
			evidence.Rate = moneyPtr(w.Rate)
		}
		threshold := 100.
		if w.Seconds == 18000 && a.Quota5HThreshold != nil {
			threshold = *a.Quota5HThreshold
		}
		reason := "额度预计耗完"
		if w.Seconds == 604800 && a.QuotaMergeThreshold != nil {
			threshold = *a.QuotaMergeThreshold
			reason = "达到额度合并阈值"
			r.UsedPercent = moneyPtr(w.Used)
		}
		gap := math.Max(0, threshold-w.Used)
		if gap == 0 {
			evidence.Certain = true
			r.Known = true
			setExit(now, reason, false, evidence)
		} else if w.Samples > 0 && w.Rate > 0 {
			r.Known = true
			minutes := gap / w.Rate
			if moneyNumber(minutes) && minutes < 525600 {
				at := w.At.Add(time.Duration(minutes * float64(time.Minute)))
				if at.Unix() < w.ResetAt {
					setExit(at, reason, false, evidence)
				}
			}
			minutes = (100 - w.Used) / w.Rate
			if moneyNumber(minutes) && minutes < 525600 {
				at := w.At.Add(time.Duration(minutes * float64(time.Minute)))
				if at.Unix() < w.ResetAt {
					left := math.Max(0, at.Sub(now).Minutes())
					if r.DepletionMinutes == nil || left < *r.DepletionMinutes {
						r.DepletionMinutes = moneyPtr(left)
					}
				}
			}
		} else if w.Samples > 0 && w.Rate == 0 {
			r.Known = true
			if r.exitAt == nil {
				r.Reason = "近期无消耗"
			}
		} else if c.ExitWarningPercent > 0 && gap <= float64(c.ExitWarningPercent) && now.Add(time.Duration(c.ExitMinutes)*time.Minute).Unix() < w.ResetAt {
			setExit(now.Add(time.Duration(c.ExitMinutes)*time.Minute), "接近额度阈值，退出时间暂不可估", true, evidence)
		}
	}
	if a.ExitDeadline != nil {
		setExit(*a.ExitDeadline, "推送等待时限到达", false, &ExitEvidence{Source: "deadline", Certain: true})
	}
	if a.Leaving {
		r.Known = true
		setExit(now, "已进入合并移出流程", false, &ExitEvidence{Source: "leaving", Certain: true})
	}
	if r.exitAt != nil {
		r.Risk = !r.exitAt.After(now.Add(time.Duration(c.ExitMinutes) * time.Minute))
		if r.WarningOnly {
			r.ExitMinutes = nil
		}
	}
	return r
}

func ForecastExits(c Config, s Snapshot, tasks []Task, newSlots int, lead float64, now time.Time) ExitForecast {
	f := ExitForecast{Rows: []ExitRow{}, Points: []ExitPoint{}, Valid: exitSnapshotValid(c, s, now), Complete: true, Confirmations: s.ExitConfirmations}
	u, capacity, waiting := exitLoad(s, now)
	f.CurrentCapacity = c.EffectiveCapacity(capacity)
	f.AfterCapacity = f.CurrentCapacity
	afterCapacity := capacity
	f.Demand = u + waiting
	f.ObservedDemand = f.Demand
	if p := s.ExitDemand; p != nil && p.Version == c.Version && p.Held && !p.At.After(now) && now.Sub(p.At) <= time.Duration(c.ExitDemandHoldSeconds)*time.Second {
		f.Demand = max(f.Demand, p.Value)
		f.DemandHeld = f.Demand > f.ObservedDemand
	}
	if !f.Valid || newSlots < 1 || !moneyNumber(lead) {
		f.Complete = false
		return f
	}
	readyIDs := map[int64]bool{}
	events := []time.Time{}
	for _, a := range s.Accounts {
		if !availableAccount(a, now) {
			continue
		}
		readyIDs[a.ID] = true
		prepared, pending := postSupplyAccount(c, a, s.SupplyChangedAt)
		rowConfig := c
		if pending {
			rowConfig.ExitWarningPercent = 0
		}
		r := exitRow(rowConfig, prepared, now)
		r.PredictionPending = pending
		if pending && r.exitAt == nil {
			r.Reason = "新号入组，等待分流后的消耗样本"
		}
		if !r.Known {
			f.Unknown++
			f.Complete = false
		}
		if r.Risk {
			f.AtRisk++
			afterCapacity -= r.reportedCapacity
			f.AfterCapacity = c.EffectiveCapacity(afterCapacity)
			events = append(events, maxExitTime(now, *r.exitAt))
		}
		if r.ExitMinutes != nil && (f.EarliestMinutes == nil || *r.ExitMinutes < *f.EarliestMinutes) {
			f.EarliestMinutes = moneyPtr(*r.ExitMinutes)
		}
		f.Rows = append(f.Rows, r)
	}
	if f.DemandHeld {
		events = append(events, now)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Before(events[j]) })
	baseAt := func(at time.Time) (int, bool) {
		v := capacity
		certain := false
		for _, r := range f.Rows {
			if r.Risk && !r.exitAt.After(at) {
				v -= r.reportedCapacity
				certain = certain || !r.WarningOnly
			}
		}
		return max(0, v), certain
	}
	pendingAt := func(at time.Time) (count, slots int) {
		ids := map[string]bool{}
		accounts := map[int64]bool{}
		for _, t := range tasks {
			if !t.Predictable() || t.Binding != s.Binding || t.GroupID != s.GroupID || readyIDs[t.AccountID] || t.ETA.Before(now) || t.ETA.After(at) || (t.ID != "" && ids[t.ID]) || (t.AccountID > 0 && accounts[t.AccountID]) {
				continue
			}
			ids[t.ID] = true
			if t.AccountID > 0 {
				accounts[t.AccountID] = true
			}
			count++
			n := t.Capacity
			if n < 1 {
				n = newSlots
			}
			slots += n
		}
		return
	}
	arrival := now.Add(time.Duration(lead * float64(time.Minute)))
	for i, at := range events {
		if i > 0 && at.Equal(events[i-1]) {
			continue
		}
		base, certain := baseAt(at)
		pending, slots := pendingAt(at)
		point := ExitPoint{Minutes: math.Max(0, at.Sub(now).Minutes()), Capacity: c.EffectiveCapacity(base), Pending: pending, PendingCapacity: c.EffectiveCapacity(slots)}
		sizingAt := maxExitTime(at, arrival)
		sizingBase, _ := baseAt(sizingAt)
		later, laterSlots := pendingAt(sizingAt)
		point.SizingMinutes = sizingAt.Sub(now).Minutes()
		point.sizingAt = sizingAt
		point.SizingCapacity = c.EffectiveCapacity(sizingBase + laterSlots)
		point.LaterPending = later - pending
		if f.Demand > 0 && f.Demand >= c.ConcurrencyTrigger(base) {
			f.Pressure = true
			_, targetPercent := c.CapacityThresholds(base)
			point.TargetPercent = targetPercent
			point.Need = max(0, int(math.Ceil((float64(f.Demand)*100/float64(max(1, targetPercent))-point.SizingCapacity)/c.EffectiveCapacity(newSlots))))
			point.Gap = float64(f.Demand) > c.EffectiveCapacity(base+slots)
			f.TransientGap = f.TransientGap || point.Gap
			f.Emergency = f.Emergency || (point.Gap && (certain || f.DemandHeld))
			f.Need = max(f.Need, point.Need)
		}
		f.Points = append(f.Points, point)
		// A fixed deadline/reached threshold is not a burn-rate prediction.
		// If only those certain exits cause a real shortage inside the delivery
		// lead plus margin, waiting for three quota samples would be too late.
		if !at.After(arrival.Add(time.Duration(c.MarginMinutes) * time.Minute)) {
			hardBase := capacity
			for _, r := range f.Rows {
				if r.Risk && r.Evidence != nil && r.Evidence.Certain && !r.exitAt.After(at) {
					hardBase -= r.reportedCapacity
				}
			}
			if float64(f.Demand) > c.EffectiveCapacity(hardBase+slots) {
				_, target := c.CapacityThresholds(hardBase)
				n := max(0, int(math.Ceil((float64(f.Demand)*100/float64(max(1, target))-c.EffectiveCapacity(hardBase+laterSlots))/c.EffectiveCapacity(newSlots))))
				f.UrgentNeed = max(f.UrgentNeed, n)
			}
		}
	}
	// Future shortage is still a prediction: a single demand spike must not
	// order a batch. Only actual account-side emergency queues skip confirmation.
	f.Confirmed = f.Pressure && (waiting >= c.EmergencyQueue || s.ExitConfirmations >= c.Confirmations)
	if s.Admission != nil {
		f.Confirmation = &s.Admission.Exit
		f.Confirmed = s.Admission.Exit.ConfirmedNeed > 0
	}
	return f
}

func maxExitTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Refresh more often while approaching an exit or collecting initial samples;
// the normal money sampler stays at one minute when the pool is stable.
func ExitRefreshInterval(c Config, s Snapshot, now time.Time) time.Duration {
	if c.ExitEnabled {
		for _, a := range s.Accounts {
			if !availableAccount(a, now) {
				continue
			}
			r := exitRow(c, a, now)
			if !r.Known || r.Risk || (r.ExitMinutes != nil && *r.ExitMinutes <= float64(c.ExitMinutes)+2) {
				return time.Duration(min(60, max(5, c.ExitRefreshSeconds))) * time.Second
			}
		}
	}
	return time.Minute
}
