package store

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
	"sync"
	"testing"
	"time"
)

// Exercise the supplier ingestion and automatic reconciliation path, not the
// occupancy index directly. This works even if the original account was deleted.
func ejhReceiveForOrder(t *testing.T, s *Store, cardID, orderID string) {
	t.Helper()
	o, _, err := s.GPTPayOrder(orderID)
	if err != nil {
		t.Fatal(err)
	}
	pay := ejh.Trade{OrderNo: "fixture-card", TradeNo: orderID + "-pay", Type: "1", Status: "1", AuthCode: orderID, CardAmount: "5794.64", CardCurrency: "PHP", TradeTime: o.CreatedAt.Format(time.RFC3339Nano)}
	ret := pay
	ret.Type, ret.Status, ret.TradeNo = "2", "1", orderID+"-return"
	ret.CardAmount = "5780.87"
	ret.TradeTime = o.UpdatedAt.Add(time.Hour).Format(time.RFC3339Nano)
	if err = s.EJHSaveTrades(cardID, []ejh.Trade{ret, pay}); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	cases, err := s.EJHCases()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.ID == orderID {
			if c.State != "returned" || c.Amounts["PHP"] != "5780.87" {
				t.Fatalf("automatic reversal not received: %+v", c)
			}
			return
		}
	}
	t.Fatal("order case missing")
}

func TestEJHAutoReconcilePersistenceAndCorrections(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	card := ejhFixtureCard(t, s)
	email := "auto@example.com"
	transferSource(t, s, email)
	order := transferOrder(email, "auto-order", "success")
	order.Order.CardID = card.ID
	at := time.Now().Add(-3 * time.Hour)
	order.Order.CreatedAt = at.Add(-time.Minute)
	order.Order.UpdatedAt = at
	if err = s.CreateGPTPayOrder(order.Order, order.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHMarkRequested(email, "remote", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	pay := ejh.Trade{OrderNo: "fixture-card", TradeNo: "auto-pay", Type: "1", Status: "1", AuthCode: "AUTH", CardAmount: "5794.64", CardCurrency: "PHP", TradeTime: at.Add(-30 * time.Second).Format(time.RFC3339)}
	ret := pay
	ret.Type = "5"
	ret.TradeNo = "auto-return"
	ret.Status = "2"
	ret.CardAmount = "5700.01"
	ret.TradeTime = at.Add(2 * time.Hour).Format(time.RFC3339)
	if err = s.EJHSaveTrades(card.ID, []ejh.Trade{ret, pay}); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	cases, err := s.EJHCases()
	if err != nil || len(cases) != 1 || cases[0].State != "returned" || cases[0].Charge == nil || cases[0].Amounts["PHP"] != "5700.01" {
		t.Fatalf("wrong auto result: %+v %v", cases, err)
	}
	before, _, err := s.GPTPayCard(card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.OpenedAccounts != 1 || before.ActiveAccounts != 0 {
		t.Fatalf("return did not release occupancy or changed lifetime usage: %+v", before)
	}
	audits, err := EJHRecords[map[string]any](s, "match_audit")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.EJHReconcile(); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	after, _ := EJHRecords[map[string]any](s, "match_audit")
	if len(after) != len(audits) {
		t.Fatal("duplicate audit on repeat")
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases, err = s.EJHCases()
	if err != nil || cases[0].State != "returned" {
		t.Fatal("restart lost auto association", err)
	}
	ret.CardAmount = "5699.99"
	if err = s.EJHSaveTrade(card.ID, ret); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	cases, _ = s.EJHCases()
	if cases[0].Amounts["PHP"] != "5699.99" {
		t.Fatal("correction ignored")
	}
	ret.Status = "1"
	if err = s.EJHSaveTrade(card.ID, ret); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	cases, _ = s.EJHCases()
	if cases[0].State != "waiting" || len(cases[0].Returns) != 0 {
		t.Fatal("invalidated return remained linked")
	}
	cardAfter, _, cardErr := s.GPTPayCard(card.ID)
	if cardErr != nil || cardAfter.ActiveAccounts != 1 || cardAfter.Available {
		t.Fatalf("invalidated return did not restore occupancy: %+v %v", cardAfter, cardErr)
	}
}

func TestEJHTradeCollectionAtomic(t *testing.T) {
	s := transferStore(t)
	card := ejhFixtureCard(t, s)
	good := ejh.Trade{OrderNo: "fixture-card", TradeNo: "good", Type: "1"}
	bad := good
	bad.TradeNo = ""
	if err := s.EJHSaveTrades(card.ID, []ejh.Trade{good, bad}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	rows, err := EJHRecords[ejh.TradeRecord](s, "trade")
	if err != nil || len(rows) != 0 {
		t.Fatal("partial collection saved", err)
	}
}

func TestEJHDelayedAuthorizationIngestionRestoresPaymentAndReturn(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	card := ejhFixtureCard(t, s)
	email := "delayed-issuer@example.com"
	transferSource(t, s, email)
	o := transferOrder(email, "delayed-issuer", "success")
	o.Order.CardID = card.ID
	at, _ := time.Parse(time.RFC3339, "2026-11-17T18:34:13+08:00")
	o.Order.CreatedAt, o.Order.UpdatedAt = at.Add(-time.Minute), at
	if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHMarkRequested(email, "remote", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	pay := ejh.Trade{OrderNo: "fixture-card", TradeNo: "delayed-pay", Type: "1", Status: "1", AuthCode: "A", CardAmount: "5794.64", CardCurrency: "PHP", TradeTime: "2026-11-17 10:34:07", CreatedAt: "2026-11-17 21:54:06", Merchant: "OPENAI"}
	ret := pay
	ret.Type, ret.TradeNo, ret.CardAmount, ret.TradeTime = "2", "delayed-return", "5779.57", "2026-11-17 12:28:22"
	// A return may be ingested first. It cannot release the card without its
	// original payment, but the later complete batch must repair both links.
	if err = s.EJHSaveTrades(card.ID, []ejh.Trade{ret}); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	cases, err := s.EJHCases()
	if err != nil || len(cases) != 1 || cases[0].State != "waiting" || cases[0].Charge != nil {
		t.Fatalf("premature return: %+v %v", cases, err)
	}
	if err = s.EJHSaveTrades(card.ID, []ejh.Trade{ret, pay}); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases, err = s.EJHCases()
	if err != nil || len(cases) != 1 || cases[0].State != "returned" || cases[0].Charge == nil || cases[0].Charge.CardAmount != "5794.64" || cases[0].Amounts["PHP"] != "5779.57" {
		t.Fatalf("delayed evidence not persisted: %+v %v", cases, err)
	}
	got, _, err := s.GPTPayCard(card.ID)
	if err != nil || got.ActiveAccounts != 0 || got.OpenedAccounts != 1 {
		t.Fatalf("returned occupancy incorrect: %+v %v", got, err)
	}
}

func TestEJHReturnUpgradeBackfillAndConcurrentReuse(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	card := ejhFixtureCard(t, s)
	email := "upgrade@example.com"
	transferSource(t, s, email)
	o := transferOrder(email, "upgrade-order", "success")
	o.Order.CardID = card.ID
	if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" }); err != nil {
		t.Fatal(err)
	}
	// Emulate upgrading a database with a legacy removal release.
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveProCard("next@example.com", card.ID, false); err == nil {
		t.Fatal("legacy removal released unpaid return")
	}
	ejhReceiveForOrder(t, s, card.ID, o.Order.ID)
	// The new admission index can be rebuilt solely from persisted evidence.
	if _, err = s.db.Exec("DELETE FROM ejh_received_returns"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GPTPayCard(card.ID)
	if err != nil || !got.Available || got.ActiveAccounts != 0 || got.OpenedAccounts != 1 {
		t.Fatal(got, err)
	}
	var wg sync.WaitGroup
	accepted := make(chan string, 2)
	for _, email := range []string{"next-a@example.com", "next-b@example.com"} {
		transferSource(t, s, email)
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			if s.ReserveProCard(email, card.ID, false) == nil {
				accepted <- email
			}
		}(email)
	}
	wg.Wait()
	close(accepted)
	if len(accepted) != 1 {
		t.Fatal("returned card allocated more than once", len(accepted))
	}
}

func TestEJHReturnTransferOldAndNewEvidence(t *testing.T) {
	src := transferStore(t)
	card := ejhFixtureCard(t, src)
	email := "return-transfer@example.com"
	transferSource(t, src, email)
	o := transferOrder(email, "return-transfer-order", "success")
	o.Order.CardID = card.ID
	if err := src.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := src.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" }); err != nil {
		t.Fatal(err)
	}
	ejhReceiveForOrder(t, src, card.ID, o.Order.ID)
	file, err := src.ExportProAccounts([]string{email}, model.ProDownstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{true, false} {
		dst := transferStore(t)
		entry := transferWire(t, file).Accounts[0]
		if legacy {
			entry.Orders[0].ReturnConfirmed = false
		}
		if _, err = dst.ImportProAccount(entry, file.ExportedAt, false, model.ProDownstreamRef{}); err != nil {
			t.Fatal(err)
		}
		imported := ejhFixtureCard(t, dst)
		got, _, err := dst.GPTPayCard(imported.ID)
		if err != nil || got.Available == legacy {
			t.Fatalf("legacy=%v card=%+v err=%v", legacy, got, err)
		}
		if legacy {
			continue
		}
		if err = dst.EJHBackfillCases(); err != nil {
			t.Fatal(err)
		}
		ejhReceiveForOrder(t, dst, imported.ID, o.Order.ID)
		rows, err := EJHRecords[ejh.TradeRecord](dst, "trade")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if !row.Trade.IsReturn() {
				continue
			}
			row.Trade.Status = "0"
			if err = dst.EJHSaveTrade(imported.ID, row.Trade); err != nil {
				t.Fatal(err)
			}
		}
		if err = dst.EJHReconcile(); err != nil {
			t.Fatal(err)
		}
		got, _, err = dst.GPTPayCard(imported.ID)
		if err != nil || got.Available || got.ActiveAccounts != 1 {
			t.Fatal("imported release outlived invalidated evidence", got, err)
		}
	}
}
