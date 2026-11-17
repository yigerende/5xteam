package ejh

import (
	"reflect"
	"testing"
	"time"
)

func delayedPaymentFixture() ([]Case, []TradeRecord) {
	cases := []Case{autoCase("delayed", "2026-11-17T18:34:13+08:00")}
	cases[0].CreatedAt = autoTime("2026-11-17T18:33:10+08:00")
	pay := autoTrade("pay", "1", "A", "2026-11-17 10:34:07")
	pay.Trade.Status, pay.Trade.CreatedAt = "1", "2026-11-17 21:54:06"
	pay.Trade.TradeAmount, pay.Trade.BillAmount = "0", "0"
	ret := autoTrade("return", "2", "A", "2026-11-17 12:28:22")
	ret.Trade.Status, ret.Trade.CreatedAt, ret.Trade.CardAmount = "1", "2026-11-17 21:54:06", "5779.57"
	ret.Trade.TradeAmount, ret.Trade.BillAmount = "0.00", "0.00"
	return cases, []TradeRecord{ret, pay}
}

func TestAutoReconcileDelayedPaymentAndReturn(t *testing.T) {
	for _, clearing := range []bool{false, true} {
		cases, trades := delayedPaymentFixture()
		if clearing {
			trades = append(trades, autoTrade("cleared", "4", "A", "2026-11-18T04:00:00+08:00"))
		}
		raw := append([]TradeRecord(nil), trades...)
		now := autoTime("2026-11-18T05:00:00+08:00")
		AutoReconcile(cases, trades, now)
		want := "pay"
		if clearing {
			want = "cleared"
		}
		if cases[0].OriginalTradeID != want || !trades[0].Confirmed || trades[0].OrderID != "delayed" || trades[0].OriginalTradeID != want || trades[0].Amount != "5779.57" {
			t.Fatalf("delayed payment/return not reconciled: %+v %+v", cases, trades)
		}
		for i := range trades {
			if !reflect.DeepEqual(raw[i].Trade, trades[i].Trade) || !TradeAt(raw[i].Trade).Equal(TradeAt(trades[i].Trade)) {
				t.Fatal("issuer evidence or financial date changed")
			}
		}
		beforeCases, beforeTrades := append([]Case(nil), cases...), append([]TradeRecord(nil), trades...)
		AutoReconcile(cases, trades, now.Add(time.Hour))
		if !reflect.DeepEqual(cases, beforeCases) || !reflect.DeepEqual(trades, beforeTrades) {
			t.Fatal("repeat reconciliation changed attribution")
		}
		if !clearing {
			cases[0].State = "returned" // Store derives this from the confirmed return.
			card := CardRecord{ID: "card", Remote: Card{OrderNo: "issuer-card"}}
			if err := ValidateAutoCancelReturns(card, cases, trades); err != nil {
				t.Fatal("verified reversal still blocks automatic cancellation:", err)
			}
		}
	}
}

func TestDelayedPaymentTimezoneRequiresUnambiguousOrderEvidence(t *testing.T) {
	for _, scenario := range []string{"wrong-card", "failed-order", "wrong-card-type", "ambiguous-orders", "also-local-order", "explicit-timezone", "missing-ingestion", "after-ingestion", "wrong-merchant", "missing-merchant", "failed-authorization", "missing-issuer-card", "missing-created", "missing-order", "outside-payment-window"} {
		t.Run(scenario, func(t *testing.T) {
			cases, trades := delayedPaymentFixture()
			p := &trades[1].Trade
			switch scenario {
			case "wrong-card":
				cases[0].CardID = "other"
			case "failed-order":
				cases[0].PaymentStatus = "failed"
			case "wrong-card-type":
				cases[0].CardType = "other"
			case "ambiguous-orders":
				cases = append(cases, autoCase("overlap", "2026-11-17T18:34:30+08:00"))
			case "also-local-order":
				cases = append(cases, autoCase("local", "2026-11-17T10:34:13+08:00"))
			case "explicit-timezone":
				p.TradeTime = "2026-11-17T10:34:07+08:00"
			case "missing-ingestion":
				p.CreatedAt = ""
			case "after-ingestion":
				p.CreatedAt = "2026-11-17 18:30:00"
			case "wrong-merchant":
				p.Merchant = "OTHER"
			case "missing-merchant":
				p.Merchant = ""
			case "failed-authorization":
				p.Status = "0"
			case "missing-issuer-card":
				p.OrderNo = ""
			case "missing-created":
				cases[0].CreatedAt = time.Time{}
			case "missing-order":
				cases = nil
			case "outside-payment-window":
				p.TradeTime = "2026-11-17 11:40:00"
			}
			if got := paymentTradeTimes(cases, trades)["pay"]; !got.Equal(TradeAt(*p)) {
				t.Fatalf("unsupported correction: %s", got)
			}
		})
	}
}

func TestDelayedPaymentDoesNotInventReturn(t *testing.T) {
	cases, trades := delayedPaymentFixture()
	trades = trades[1:]
	AutoReconcile(cases, trades, autoTime("2026-11-17T23:00:00+08:00"))
	if cases[0].OriginalTradeID != "pay" || trades[0].Confirmed || trades[0].OrderID != "" {
		t.Fatal("payment-only evidence incorrectly confirmed a return")
	}
}
