package store

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/nexovcc"
	"errors"
	"testing"
	"time"
)

func TestCardPaymentPolicyRequiresCompletedAccount(t *testing.T) {
	for _, provider := range []string{"ejh", "nexovcc"} {
		t.Run(provider, func(t *testing.T) {
			s := transferStore(t)
			cardID, serial := "", ""
			if provider == "ejh" {
				c := ejhFixtureCard(t, s)
				cardID = c.ID
			} else {
				c := nexStoreCard(t, s)
				cardID, serial = c.ID, c.Serial
			}
			email := "payment-policy@example.com"
			transferSource(t, s, email)
			o := transferOrder(email, "policy-order", "success")
			o.Order.CardID = cardID
			if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
				t.Fatal(err)
			}
			p, err := s.SaveCardPaymentPolicy(CardPaymentPolicy{Provider: provider, CardID: cardID, Mode: "payment_only", Note: "test"}, "")
			if err != nil {
				t.Fatal(err)
			}
			occupied := func() bool {
				t.Helper()
				var n int
				query := ejhOrderOccupiedSQL
				if provider == "nexovcc" {
					query = nexovccOccupiedSQL
				}
				if err := s.db.QueryRow(`SELECT count(*) FROM pro_card_usage u WHERE u.order_id=? AND `+query, o.Order.ID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n > 0
			}
			if !occupied() {
				t.Fatal("classification released running account")
			}
			if _, err := s.db.Exec(`INSERT INTO pro_card_order_releases(order_id,released_at) VALUES(?,?)`, o.Order.ID, time.Now().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if occupied() {
				t.Fatal("completed payment-only order still waits for refund")
			}
			p.Mode = "normal"
			p, err = s.SaveCardPaymentPolicy(p, "")
			if err != nil {
				t.Fatal(err)
			}
			if !occupied() {
				t.Fatal("restoring normal failed to restore refund guard")
			}
			p.Mode = "payment_only"
			if _, err = s.SaveCardPaymentPolicy(p, ""); err != nil {
				t.Fatal(err)
			}
			if provider == "nexovcc" {
				if err = s.BeginNexovccOperation(nexovcc.Operation{ID: "nex-policy-close", Action: "withdraw_cancel", CardID: cardID, Serial: serial, Status: "submitted", CapacityRecycle: true}); err != nil {
					t.Fatal("completed payment-only card cannot recycle", err)
				}
			}
		})
	}
}

func TestCardPaymentPolicyPersistenceAndTransfer(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if err = s.EJHPut("card", "c", ejh.CardRecord{ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveEJHRechargeLedger("1000"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveEJHExternalTransfer("200", "external"); err != nil {
		t.Fatal(err)
	}
	input := CardPaymentPolicy{Provider: "ejh", CardID: "c", Mode: "payment_only", AmountUSD: "140", Note: "test"}
	got, err := s.SaveCardPaymentPolicy(input, "140")
	if err != nil || got.Revision != 1 {
		t.Fatal(got, err)
	}
	transfers, err := EJHRecords[EJHExternalTransferEvent](s, "finance_external_transfer")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range transfers {
		if event.Kind == "payment_only_reclassification" {
			found = event.CardID == "c" && event.Delta == "-140.00" && event.Before == "200.00" && event.After == "60.00"
		}
	}
	if !found {
		t.Fatal("reclassification evidence missing", transfers)
	}
	ledger, _ := s.EJHRechargeLedger()
	if ledger.ExternalTransferAmount != "60.00" || ledger.Amount != "1000.00" {
		t.Fatal(ledger)
	}
	if _, err = s.SaveCardPaymentPolicy(input, "140"); !errors.Is(err, ErrCardPaymentPolicyConflict) {
		t.Fatal("replayed transfer", err)
	}
	ledger, _ = s.EJHRechargeLedger()
	if ledger.ExternalTransferAmount != "60.00" {
		t.Fatal("double deducted")
	}
	before, _ := s.CardPaymentPolicies("ejh")
	got.Mode = "normal"
	restored, err := s.SaveCardPaymentPolicy(got, "")
	if err != nil || restored.Revision != 2 {
		t.Fatal(restored, err)
	}
	if before[0].Mode != "payment_only" {
		t.Fatal(before)
	}
	for _, bad := range []string{"-1", "1.001", "NaN", "1e3"} {
		badInput := restored
		badInput.AmountUSD = bad
		if _, err = s.SaveCardPaymentPolicy(badInput, ""); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	c := nexStoreCard(t, s)
	p, err := s.SaveCardPaymentPolicy(CardPaymentPolicy{Provider: "nexovcc", CardID: c.ID, Mode: "payment_only", Note: "test"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveNexovccCard(nexovcc.Card{Serial: c.Serial, Status: "4", Balance: "99"}, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.CardPaymentPolicies("nexovcc")
	if err != nil || len(rows) != 1 || rows[0].Revision != p.Revision || rows[0].Mode != "payment_only" {
		t.Fatal(rows, err)
	}
	audit, err := EJHRecords[map[string]any](s, "card_payment_policy_audit")
	if err != nil || len(audit) != 3 {
		t.Fatal(len(audit), err)
	}
}

func TestCardPaymentPolicyFinanceSnapshot(t *testing.T) {
	s := transferStore(t)
	card := ejhFixtureCard(t, s)
	_, err := s.SaveCardPaymentPolicy(CardPaymentPolicy{Provider: "ejh", CardID: card.ID, Mode: "payment_only", AmountUSD: "140", Note: "fixture"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var remote ejh.CardRecord
	if err = s.EJHGet("card", card.ID, &remote); err != nil {
		t.Fatal(err)
	}
	remote.Remote.Status = "2"
	if err = s.EJHPut("card", card.ID, remote); err != nil {
		t.Fatal(err)
	}
	if err = s.CaptureEJHFinance(time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshots, err := EJHRecords[EJHFinancialSnapshot](s, "finance_snapshot")
	if err != nil || len(snapshots) != 2 {
		t.Fatal(len(snapshots), err)
	}
	for _, v := range snapshots {
		if len(v.PaymentPolicies) != 1 || v.PaymentPolicies[0].AmountUSD != "140.00" || len(v.Cards) != 1 || v.Cards[0].Remote.Status != "2" {
			t.Fatal("lost classified historical expenditure", v)
		}
	}
}
