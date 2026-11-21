package restock

import (
	"math"
	"time"
)

const MoneyMaxAge = 120 * time.Second
const DefaultMoneyWindowMinutes = 3
const MaxMoneyWindowMinutes = 60

type MoneySample struct {
	At   time.Time `json:"at"`
	Cost float64   `json:"cost"`
}
type MoneyWindow struct {
	Seconds     int64     `json:"seconds"`
	ResetAt     time.Time `json:"reset_at"`
	UsedPercent float64   `json:"used_percent"`
	UsedUSD     float64   `json:"used_usd"`
}
type AccountMoney struct {
	RateSeconds   float64       `json:"rate_seconds,omitempty"`
	WindowMinutes int           `json:"window_minutes"`
	At            time.Time     `json:"at"`
	BudgetAt      time.Time     `json:"budget_at"`
	Day           string        `json:"day"`
	Windows       []MoneyWindow `json:"windows"`
	Samples       []MoneySample `json:"samples"`
	RateUSD       *float64      `json:"rate_usd,omitempty"`
	Error         string        `json:"error,omitempty"`
}
type MoneyRow struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Seconds      int64    `json:"seconds"`
	TotalUSD     *float64 `json:"total_usd"`
	UsedUSD      *float64 `json:"used_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
	RateUSD      *float64 `json:"rate_usd"`
	Reason       string   `json:"reason"`
}
type MoneyForecast struct {
	WindowMinutes int        `json:"window_minutes"`
	State         string     `json:"state"`
	Coverage      float64    `json:"coverage"`
	TotalUSD      *float64   `json:"total_usd"`
	RemainingUSD  *float64   `json:"remaining_usd"`
	RateUSD       *float64   `json:"rate_usd"`
	ValidUntil    time.Time  `json:"valid_until"`
	Rows          []MoneyRow `json:"rows"`
}

func moneyNumber(n float64) bool  { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 }
func moneyPtr(n float64) *float64 { return &n }

// Records created before this option used a fixed three-minute window.
func moneyWindowMinutes(minutes int) int {
	if minutes == 0 {
		return DefaultMoneyWindowMinutes
	}
	return minutes
}

// Only consecutive samples of the SAME account, binding, plan and UTC day may
// be differenced. Keep a boundary sample for the configured rolling window.
func ObserveMoney(current AccountMoney, previous *AccountMoney, cost float64, minutes int) AccountMoney {
	current.WindowMinutes = moneyWindowMinutes(minutes)
	current.RateUSD = nil
	current.Samples = nil
	if current.WindowMinutes < 1 || current.WindowMinutes > MaxMoneyWindowMinutes {
		current.Error = "消耗计算窗口无效"
	}
	if current.Error != "" || !moneyNumber(cost) {
		return current
	}
	if previous != nil && previous.Error == "" && previous.Day == current.Day && len(previous.Samples) > 0 {
		last := previous.Samples[len(previous.Samples)-1]
		gap := current.At.Sub(last.At)
		if gap >= time.Second && gap <= MoneyMaxAge && cost >= last.Cost {
			current.Samples = append(current.Samples, previous.Samples...)
		}
	}
	current.Samples = append(current.Samples, MoneySample{At: current.At, Cost: cost})
	cut := current.At.Add(-time.Duration(current.WindowMinutes) * time.Minute)
	for len(current.Samples) > 2 && !current.Samples[1].At.After(cut) {
		current.Samples = current.Samples[1:]
	}
	current.RateUSD = sampledMoneyRate(current.Samples, current.At, current.WindowMinutes)
	if current.RateUSD != nil {
		current.RateSeconds = float64(current.WindowMinutes) * 60
	}
	return current
}

func sampledMoneyRate(samples []MoneySample, at time.Time, minutes int) *float64 {
	if minutes < 1 || minutes > MaxMoneyWindowMinutes {
		return nil
	}
	cut := at.Add(-time.Duration(minutes) * time.Minute)
	for len(samples) > 2 && !samples[1].At.After(cut) {
		samples = samples[1:]
	}
	if len(samples) < 2 || samples[0].At.After(cut) || !samples[len(samples)-1].At.Equal(at) {
		return nil
	}
	first, next := samples[0], samples[1]
	span := next.At.Sub(first.At).Seconds()
	if span <= 0 {
		return nil
	}
	baseline := first.Cost + (next.Cost-first.Cost)*cut.Sub(first.At).Seconds()/span
	rate := math.Max(0, samples[len(samples)-1].Cost-baseline) / float64(minutes)
	if moneyNumber(rate) {
		return moneyPtr(rate)
	}
	return nil
}

// One account contributes one budget: the limiting active window, never 5h+7d.
func accountMoneyRow(a Account, now time.Time, minutes int) MoneyRow {
	r := MoneyRow{ID: a.ID, Name: a.Name}
	m := a.Money
	if m == nil {
		r.Reason = "采样中"
		return r
	}
	if m.Error != "" {
		r.Reason = "费用读取失败"
		if m.Error == "数据过期" {
			r.Reason = m.Error
		}
		return r
	}
	budgetAt := m.BudgetAt
	if budgetAt.IsZero() {
		budgetAt = m.At
	}
	if m.At.IsZero() || m.At.After(now.Add(5*time.Second)) || now.Sub(m.At) > MoneyMaxAge || budgetAt.After(now.Add(5*time.Second)) || now.Sub(budgetAt) > MoneyMaxAge {
		r.Reason = "数据过期"
		return r
	}
	for _, w := range m.Windows {
		if w.Seconds <= 0 || !w.ResetAt.After(now) || w.UsedPercent == 0 {
			continue
		}
		if !moneyNumber(w.UsedUSD) || w.UsedUSD == 0 || !moneyNumber(w.UsedPercent) || w.UsedPercent > 100 {
			r.Reason = "费用数据无效"
			return r
		}
		total := w.UsedUSD * 100 / w.UsedPercent
		remaining := math.Max(0, total-w.UsedUSD)
		if !moneyNumber(total) {
			r.Reason = "费用数据无效"
			return r
		}
		if r.RemainingUSD == nil || remaining < *r.RemainingUSD {
			r.Seconds, r.TotalUSD, r.UsedUSD, r.RemainingUSD = w.Seconds, moneyPtr(total), moneyPtr(w.UsedUSD), moneyPtr(remaining)
		}
	}
	if r.TotalUSD == nil {
		r.Reason = "缺少费用或使用率"
		return r
	}
	rate := m.RateUSD
	if moneyWindowMinutes(m.WindowMinutes) != minutes {
		// Reuse sufficient history for shorter windows; never reuse the old rate.
		rate = sampledMoneyRate(m.Samples, m.At, minutes)
	}
	if rate == nil {
		r.Reason = "采样中"
		return r
	}
	if !moneyNumber(*rate) {
		r.Reason = "费用数据无效"
		return r
	}
	r.RateUSD = moneyPtr(*rate)
	return r
}

func ForecastMoney(c Config, s Snapshot, now time.Time) MoneyForecast {
	f := MoneyForecast{State: "empty", Rows: []MoneyRow{}, WindowMinutes: moneyWindowMinutes(c.MoneyWindowMinutes)}
	total, remaining, rate, covered := 0., 0., 0., 0
	missing, sampling, stale := false, false, false
	for _, a := range s.Accounts {
		if !availableAccount(a, now) && !(a.SupplyExcluded && a.Ready) {
			continue
		}
		r := accountMoneyRow(a, now, f.WindowMinutes)
		if a.SupplyExcluded && r.RemainingUSD != nil {
			r.RemainingUSD = moneyPtr(0)
			r.TotalUSD = moneyPtr(0)
		}
		f.Rows = append(f.Rows, r)
		if r.Reason != "" {
			if r.Reason == "数据过期" {
				stale = true
			} else if r.Reason == "采样中" {
				sampling = true
			} else {
				missing = true
			}
			continue
		}
		covered++
		total += *r.TotalUSD
		remaining += *r.RemainingUSD
		rate += *r.RateUSD
		until := a.Money.At.Add(MoneyMaxAge)
		if !a.Money.BudgetAt.IsZero() && a.Money.BudgetAt.Add(MoneyMaxAge).Before(until) {
			until = a.Money.BudgetAt.Add(MoneyMaxAge)
		}
		for _, w := range a.Money.Windows {
			if w.Seconds == r.Seconds && w.ResetAt.Before(until) {
				until = w.ResetAt
			}
		}
		if a.ExpiresAt != nil && a.ExpiresAt.Before(until) {
			until = *a.ExpiresAt
		}
		if f.ValidUntil.IsZero() || until.Before(f.ValidUntil) {
			f.ValidUntil = until
		}
	}
	if len(f.Rows) == 0 {
		return f
	}
	f.Coverage = float64(covered) * 100 / float64(len(f.Rows))
	// Never divide a partial cohort's remaining dollars by a different cohort's rate.
	if stale {
		f.State = "stale"
		return f
	}
	if missing {
		f.State = "missing"
		return f
	}
	if sampling {
		f.State = "sampling"
		return f
	}
	if !moneyNumber(total) || !moneyNumber(remaining) || !moneyNumber(rate) {
		f.State = "missing"
		return f
	}
	f.TotalUSD, f.RemainingUSD, f.RateUSD = moneyPtr(total), moneyPtr(remaining), moneyPtr(rate)
	f.State = "ready"
	if rate == 0 {
		f.State = "idle"
	}
	return f
}

func moneyTimeNeed(c Config, s Snapshot, f MoneyForecast, tasks []Task, target, lead float64, now time.Time) (int, string, *MoneySizing) {
	if f.State != "ready" || f.RateUSD == nil || *f.RateUSD <= 0 || f.RemainingUSD == nil {
		return 0, "", nil
	}
	newBudget := math.Inf(1)
	readyIDs := map[int64]bool{}
	for _, a := range s.Accounts {
		if !availableAccount(a, now) {
			continue
		}
		readyIDs[a.ID] = true
		if s.NewPlan == "" || first(a.PurchasePlan, a.Plan) != s.NewPlan {
			continue
		}
		r := accountMoneyRow(a, now, moneyWindowMinutes(c.MoneyWindowMinutes))
		if r.Reason == "" && r.TotalUSD != nil && *r.TotalUSD > 0 {
			newBudget = math.Min(newBudget, *r.TotalUSD)
		}
	}
	if math.IsInf(newBudget, 1) {
		return 0, "新开通套餐缺少美元额度样本，暂停时间侧新增", nil
	}
	pending := 0
	seenTasks, seenAccounts := map[string]bool{}, map[int64]bool{}
	for _, t := range tasks {
		if t.Predictable() && t.Binding == s.Binding && t.GroupID == s.GroupID && t.Plan == s.NewPlan && !readyIDs[t.AccountID] && (t.ID == "" || !seenTasks[t.ID]) && (t.AccountID == 0 || !seenAccounts[t.AccountID]) && !t.ETA.After(now.Add(time.Duration(math.Max(lead, target)*float64(time.Minute)))) {
			pending++
			seenTasks[t.ID], seenAccounts[t.AccountID] = true, true
		}
	}
	gap := *f.RateUSD*target - *f.RemainingUSD - float64(pending)*newBudget
	sizing := &MoneySizing{NewBudgetUSD: newBudget, Pending: pending, GapUSD: gap}
	if math.IsNaN(gap) || math.IsInf(gap, 0) {
		// Evidence must not make an otherwise serializable decision unsavable.
		sizing = nil
	}
	if gap <= 0 {
		return 0, "", sizing
	}
	return int(math.Min(float64(c.MaxInflight), math.Ceil(gap/newBudget))), "", sizing
}
