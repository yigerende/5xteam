package ejh

import (
	"sort"
	"strings"
	"time"
)

// TradeAt normalizes issuer timestamps. Some issuer channels report tradeTime
// in UTC without an offset while createdAt is Shanghai time. Only correct the
// eight-hour offset when the issuer's own recording time corroborates it.
func parseTradeTime(s string) time.Time {
	if v, e := time.Parse(time.RFC3339Nano, s); e == nil {
		return v
	}
	v, _ := time.ParseInLocation("2006-01-02 15:04:05", s, time.FixedZone("CST", 8*3600))
	return v
}

func TradeAt(t Trade) time.Time {
	at, recorded := parseTradeTime(t.TradeTime), parseTradeTime(t.CreatedAt)
	if at.IsZero() {
		return recorded
	}
	if !strings.Contains(t.TradeTime, "T") && !recorded.IsZero() {
		lag := recorded.Sub(at.Add(8 * time.Hour))
		if lag >= 0 && lag <= 30*time.Minute {
			return at.Add(8 * time.Hour)
		}
	}
	return at
}

// Delayed issuer ingestion can exceed TradeAt's standalone timezone window.
// Recover only an otherwise impossible return-before-authorization timestamp,
// corroborated by a unique same-card authorization whose UTC offset is known.
// Raw issuer evidence is retained; these times are used only for reconciliation.
func returnTradeTimes(trades []TradeRecord) map[string]time.Time {
	return returnTradeTimesWithPayments(trades, nil)
}

func returnTradeTimesWithPayments(trades []TradeRecord, paymentTimes map[string]time.Time) map[string]time.Time {
	authorizations := map[string][]TradeRecord{}
	for _, t := range trades {
		if t.Trade.Type == "1" && ChargeUsable(t.Trade) && t.Trade.AuthCode != "" {
			k := chargeKey(t)
			authorizations[k] = append(authorizations[k], t)
		}
	}
	times := map[string]time.Time{}
	for _, t := range trades {
		at := TradeAt(t.Trade)
		times[t.ID] = at
		raw, recorded := parseTradeTime(t.Trade.TradeTime), parseTradeTime(t.Trade.CreatedAt)
		if !ReturnReceived(t.Trade) || t.Trade.AuthCode == "" || strings.Contains(t.Trade.TradeTime, "T") || raw.IsZero() || recorded.IsZero() || !at.Equal(raw) {
			continue
		}
		candidates := authorizations[chargeKey(t)]
		if len(candidates) != 1 {
			continue
		}
		p := candidates[0].Trade
		original, originalRaw := TradeAt(p), parseTradeTime(p.TradeTime)
		if resolved, ok := paymentTimes[candidates[0].ID]; ok {
			original = resolved
		}
		if strings.Contains(p.TradeTime, "T") || originalRaw.IsZero() || !original.Equal(originalRaw.Add(8*time.Hour)) || p.CardCurrency != t.Trade.CardCurrency || p.OrderNo != t.Trade.OrderNo {
			continue
		}
		if p.Merchant != "" && t.Trade.Merchant != "" && !strings.EqualFold(strings.TrimSpace(p.Merchant), strings.TrimSpace(t.Trade.Merchant)) {
			continue
		}
		corrected := raw.Add(8 * time.Hour)
		if raw.Before(original) && !corrected.Before(original) && !corrected.After(recorded) {
			times[t.ID] = corrected
		}
	}
	return times
}

func chargeKey(t TradeRecord) string {
	key := "trade:" + t.Trade.TradeNo
	if t.Trade.AuthCode != "" {
		key = "auth:" + t.Trade.AuthCode
	}
	return t.CardID + "|" + key
}

func ChargeUsable(t Trade) bool {
	return (t.Type == "1" || t.Type == "4") && (t.Status == "1" || t.Status == "2") && Positive(t.CardAmount) == nil && ValidCurrency(t.CardCurrency)
}

// Authorization reversal releases the original hold before settlement. EJH
// reports these reversals as status 1 even after the card balance is restored.
// Ordinary refunds still require a posted entry. Card-closure transfers are
// never account returns.
func ReturnReceived(t Trade) bool {
	return t.IsReturn() && (t.Status == "2" || t.Type == "2" && t.Status == "1") && Positive(t.CardAmount) == nil && ValidCurrency(t.CardCurrency)
}

// PaymentTrades returns one monetary event per authorization, preferring final
// clearing. Authorizations and clearing must never be added together.
func PaymentTrades(trades []TradeRecord) []TradeRecord {
	best := map[string]TradeRecord{}
	rank := func(t Trade) int {
		if t.Type == "4" && t.Status == "2" {
			return 3
		}
		if t.Type == "1" {
			return 2
		}
		return 1
	}
	for _, t := range trades {
		if t.ID == "" || t.CardID == "" || t.Trade.TradeNo == "" || !ChargeUsable(t.Trade) {
			continue
		}
		key := chargeKey(t)
		old, ok := best[key]
		if !ok || rank(t.Trade) > rank(old.Trade) || (rank(t.Trade) == rank(old.Trade) && t.ID < old.ID) {
			best[key] = t
		}
	}
	out := []TradeRecord{}
	for _, t := range best {
		out = append(out, t)
	}
	sortTrades(out)
	return out
}

func sortTrades(v []TradeRecord) {
	sort.Slice(v, func(i, j int) bool {
		a, b := TradeAt(v[i].Trade), TradeAt(v[j].Trade)
		if a.Equal(b) {
			return v[i].ID < v[j].ID
		}
		return a.Before(b)
	})
}

// AutoReconcile assigns saved issuer evidence only; it never submits financial
// operations. Existing manual confirmations survive.
func AutoReconcile(cases []Case, trades []TradeRecord, now time.Time) {
	oldCases := append([]Case(nil), cases...)
	oldTrades := append([]TradeRecord(nil), trades...)
	byID := map[string]TradeRecord{}
	for _, t := range trades {
		byID[t.ID] = t
	}
	manualReturns := map[string]bool{}
	for _, t := range trades {
		if t.OrderID != "" && !strings.HasPrefix(t.Source, "auto_") {
			manualReturns[t.OrderID] = true
		}
	}
	for i := range cases {
		if strings.HasPrefix(cases[i].ChargeEvidence, "auto:") && !manualReturns[cases[i].ID] {
			cases[i].OriginalTradeID = ""
			cases[i].ChargeEvidence = ""
			cases[i].ChargeLinkedAt = nil
		}
	}
	for i := range trades {
		if strings.HasPrefix(trades[i].Source, "auto_") {
			trades[i].OrderID = ""
			trades[i].OriginalTradeID = ""
			trades[i].Confirmed = false
			trades[i].ConfirmedAt = nil
			trades[i].Amount = ""
			trades[i].Currency = ""
			trades[i].Source = ""
			trades[i].Evidence = ""
		}
	}
	cardID := func(c Case) string {
		if c.EJHCardID != "" {
			return c.EJHCardID
		}
		return c.CardID
	}
	opened := func(c Case) time.Time {
		if c.OpenedAt != nil {
			return *c.OpenedAt
		}
		return c.CreatedAt
	}
	order := make([]int, len(cases))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := opened(cases[order[i]]), opened(cases[order[j]])
		if a.Equal(b) {
			return cases[order[i]].ID < cases[order[j]].ID
		}
		return a.Before(b)
	})
	usedPayments := map[string]bool{}
	for _, c := range cases {
		if t, ok := byID[c.OriginalTradeID]; ok {
			usedPayments[chargeKey(t)] = true
		}
	}
	payments := PaymentTrades(trades)
	paymentTimes := paymentTradeTimes(cases, trades)
	// Use the original authorization time even when its clearing arrived later.
	earliest := map[string]time.Time{}
	for _, t := range trades {
		if t.Trade.Type != "1" && t.Trade.Type != "4" {
			continue
		}
		k := chargeKey(t)
		at := paymentTimes[t.ID]
		if !at.IsZero() && (earliest[k].IsZero() || at.Before(earliest[k])) {
			earliest[k] = at
		}
	}
	sort.Slice(payments, func(i, j int) bool {
		a, b := earliest[chargeKey(payments[i])], earliest[chargeKey(payments[j])]
		if a.Equal(b) {
			return payments[i].ID < payments[j].ID
		}
		return a.Before(b)
	})
	for _, t := range payments {
		key := chargeKey(t)
		at := earliest[key]
		merchant := strings.ToLower(t.Trade.Merchant)
		if usedPayments[key] || at.IsZero() || (merchant != "" && !strings.Contains(merchant, "openai") && !strings.Contains(merchant, "chatgpt")) {
			continue
		}
		for _, i := range order {
			c := &cases[i]
			if c.CardType != "ejh" || c.PaymentStatus != "success" || cardID(*c) != t.CardID || c.OriginalTradeID != "" || c.CreatedAt.IsZero() {
				continue
			}
			if at.Before(c.CreatedAt.Add(-2*time.Minute)) || at.After(opened(*c).Add(30*time.Minute)) {
				continue
			}
			c.OriginalTradeID = t.ID
			c.ChargeEvidence = "auto: 同卡付款时间与开通订单匹配；授权与清算去重"
			c.ChargeLinkedAt = &now
			usedPayments[key] = true
			break
		}
	}
	usedAccounts, usedReturns := map[string]bool{}, map[string]bool{}
	caseIndex := map[string]int{}
	for i, c := range cases {
		caseIndex[c.ID] = i
	}
	for _, t := range trades {
		if t.OrderID != "" {
			if i, ok := caseIndex[t.OrderID]; ok {
				usedAccounts[strings.ToLower(cases[i].Email)] = true
			}
			usedReturns[chargeKey(t)] = true
		}
	}
	returns := []TradeRecord{}
	for _, t := range trades {
		if t.Trade.IsReturn() {
			returns = append(returns, t)
		}
	}
	returnTimes := returnTradeTimesWithPayments(trades, paymentTimes)
	sort.Slice(returns, func(i, j int) bool {
		a, b := returnTimes[returns[i].ID], returnTimes[returns[j].ID]
		if a.Equal(b) {
			return returns[i].ID < returns[j].ID
		}
		return a.Before(b)
	})
	tradeIndex := map[string]int{}
	for i, t := range trades {
		tradeIndex[t.ID] = i
	}
	for _, rt := range returns {
		at := returnTimes[rt.ID]
		if rt.OrderID != "" || !ReturnReceived(rt.Trade) || at.IsZero() || usedReturns[chargeKey(rt)] {
			continue
		}
		match := -1
		source := "auto_fifo"
		// Product policy: within one card and currency, match the oldest return
		// to the oldest activated account awaiting funds. Authorization codes
		// deduplicate events; they do not reorder this queue.
		for _, i := range order {
			c := cases[i]
			p, valid := byID[c.OriginalTradeID]
			if valid && ChargeUsable(p.Trade) && p.Trade.CardCurrency == rt.Trade.CardCurrency && c.CardType == "ejh" && cardID(c) == rt.CardID && c.PaymentStatus == "success" && c.OriginalTradeID != "" && !usedAccounts[strings.ToLower(c.Email)] && !at.Before(opened(c)) {
				match = i
				break
			}
		}
		if match < 0 {
			continue
		}
		c := cases[match]
		t := &trades[tradeIndex[rt.ID]]
		t.OrderID = c.ID
		t.OriginalTradeID = c.OriginalTradeID
		t.Amount = t.Trade.CardAmount
		t.Currency = t.Trade.CardCurrency
		t.Confirmed = true
		t.ConfirmedAt = &now
		t.Source = source
		t.Evidence = "同卡回款按开通时间先后自动匹配"
		usedAccounts[strings.ToLower(c.Email)] = true
		usedReturns[chargeKey(rt)] = true
	}
	for i := range cases {
		if cases[i].OriginalTradeID == oldCases[i].OriginalTradeID && cases[i].ChargeEvidence == oldCases[i].ChargeEvidence {
			cases[i].ChargeLinkedAt = oldCases[i].ChargeLinkedAt
		}
	}
	for i := range trades {
		a, b := &trades[i], oldTrades[i]
		if a.OrderID == b.OrderID && a.OriginalTradeID == b.OriginalTradeID && a.Confirmed == b.Confirmed && a.Amount == b.Amount && a.Currency == b.Currency && a.Source == b.Source {
			a.ConfirmedAt = b.ConfirmedAt
		}
	}
}
