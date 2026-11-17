package ejh

import (
	"strings"
	"time"
)

// A delayed issuer batch may exceed TradeAt's 30-minute corroboration window.
// Resolve its timezone only for reconciliation, using a unique same-card
// successful order. Never reinterpret a timestamp that already fits an order,
// a timestamp with an explicit timezone, or one later than issuer ingestion.
// Raw evidence and financial posting dates remain untouched.
func paymentTradeTimes(cases []Case, trades []TradeRecord) map[string]time.Time {
	byCard := map[string][]Case{}
	for _, c := range cases {
		if c.CardType != "ejh" || c.PaymentStatus != "success" || c.CreatedAt.IsZero() {
			continue
		}
		id := c.EJHCardID
		if id == "" {
			id = c.CardID
		}
		byCard[id] = append(byCard[id], c)
	}
	matches := func(c Case, at time.Time) bool {
		opened := c.CreatedAt
		if c.OpenedAt != nil {
			opened = *c.OpenedAt
		}
		return !at.Before(c.CreatedAt.Add(-2*time.Minute)) && !at.After(opened.Add(30*time.Minute))
	}
	times := make(map[string]time.Time, len(trades))
	for _, t := range trades {
		at := TradeAt(t.Trade)
		times[t.ID] = at
		p := t.Trade
		if p.Type != "1" || !ChargeUsable(p) || strings.Contains(p.TradeTime, "T") {
			continue
		}
		raw, recorded := parseTradeTime(p.TradeTime), parseTradeTime(p.CreatedAt)
		merchant := strings.ToLower(p.Merchant)
		if raw.IsZero() || recorded.IsZero() || !at.Equal(raw) || p.OrderNo == "" ||
			(!strings.Contains(merchant, "openai") && !strings.Contains(merchant, "chatgpt")) {
			continue
		}
		corrected := raw.Add(8 * time.Hour)
		if corrected.After(recorded) {
			continue
		}
		localMatches, utcMatches := 0, 0
		for _, c := range byCard[t.CardID] {
			if matches(c, raw) {
				localMatches++
			}
			if matches(c, corrected) {
				utcMatches++
			}
		}
		if localMatches == 0 && utcMatches == 1 {
			times[t.ID] = corrected
		}
	}
	return times
}
