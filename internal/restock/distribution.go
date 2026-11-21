package restock

import "time"

// Keep the full history for the group money forecast, but don't extrapolate
// an individual account's pre-arrival traffic after new capacity joins. Two
// fresh observations spanning 30 seconds can establish the new rate; this is
// not a fixed one-minute stop on any other replenishment rule.
func postSupplyAccount(c Config, a Account, after time.Time) (Account, bool) {
	if after.IsZero() {
		return a, false
	}
	pending := false
	if a.Money != nil {
		m := *a.Money
		if m.At.Add(-time.Duration(moneyWindowMinutes(c.MoneyWindowMinutes)) * time.Minute).Before(after) {
			m.RateUSD = nil
			m.WindowMinutes = moneyWindowMinutes(c.MoneyWindowMinutes)
			var samples []MoneySample
			for _, s := range m.Samples {
				if !s.At.Before(after) {
					samples = append(samples, s)
				}
			}
			if len(samples) >= 2 {
				first, last := samples[0], samples[len(samples)-1]
				span := last.At.Sub(first.At)
				if span >= 30*time.Second && last.Cost >= first.Cost && last.At.Equal(m.At) {
					m.RateUSD = moneyPtr((last.Cost - first.Cost) / span.Minutes())
					m.RateSeconds = span.Seconds()
				}
			}
			pending = m.RateUSD == nil
		}
		a.Money = &m
	}
	a.Windows = append([]Window(nil), a.Windows...)
	for i := range a.Windows {
		w := &a.Windows[i]
		if w.RateStartAt.Before(after) {
			w.Rate, w.Samples = 0, 0
			var samples []QuotaSample
			for _, s := range w.History {
				if !s.At.Before(after) {
					samples = append(samples, s)
				}
			}
			if len(samples) >= 2 {
				first, last := samples[0], samples[len(samples)-1]
				span := last.At.Sub(first.At)
				if span >= 30*time.Second && last.Used >= first.Used && last.At.Equal(w.At) {
					w.Rate = (last.Used - first.Used) / span.Minutes()
					w.RateStartAt, w.SampleSeconds, w.Samples = first.At, span.Seconds(), len(samples)-1
				}
			}
			pending = pending || w.Samples == 0
		}
	}
	return a, pending
}
