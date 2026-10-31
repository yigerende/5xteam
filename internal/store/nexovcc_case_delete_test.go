package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/nexovcc"
)

func TestNexovccDeleteUnopenedCasesPreservesHistoryAndProtectedCases(t *testing.T) {
	s := transferStore(t)
	card := nexStoreCard(t, s)
	transferSource(t, s, "cleanup-00@example.com")
	cfg := nexovcc.Defaults()
	cfg.Enabled, cfg.SingleActiveAccount, cfg.DefaultMaxAccounts = true, false, 100
	if _, err := s.SaveNexovccSettings(cfg, nexovcc.Credentials{}, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 19; i++ {
		id, status := fmt.Sprintf("cleanup-%02d", i), "failed"
		if i == 12 || i == 13 {
			status = "success"
		}
		if i == 17 {
			status = "processing"
		}
		if i == 18 {
			status = "submission_unknown"
		}
		o := transferOrder(id+"@example.com", id, status)
		o.Order.CardID = card.ID
		if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy/imported data may have a failed status alongside an earlier success.
	if _, err := s.db.Exec(`UPDATE gptpay_orders SET profile=json_set(profile,'$.succeeded_at',?) WHERE id='cleanup-14'`, formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNexovccCase("cleanup-13", "unopened", "手工调整为未开通", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNexovccCase("cleanup-16", "opened", "人工确认已经开通", 0); err != nil {
		t.Fatal(err)
	}
	match := mustJSON(NexovccMatch{Group: nexovcc.TradeGroup{ID: "existing-payment", Payment: "10.00", Valid: true}})
	if _, err := s.db.Exec(`INSERT INTO nexovcc_case_matches(order_id,group_id,returned,profile) VALUES(?,?,?,?)`, "cleanup-15", "existing-payment", 0, match); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, table := range []string{"gptpay_orders", "pro_card_usage", "nexovcc_cards", "nexovcc_case_matches", "nexovcc_case_states", "mail_accounts"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[table] = n
	}
	// Concurrent clicks still remove each eligible record only once, across all pages.
	var wg sync.WaitGroup
	results := make(chan NexovccCaseRemoval, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.DeleteNexovccUnopenedCases()
			if err != nil {
				t.Error(err)
			}
			results <- out
		}()
	}
	wg.Wait()
	close(results)
	total := 0
	for out := range results {
		total += out.Deleted
		if out.Deleted != len(out.DeletedIDs) || out.Skipped != 4 {
			t.Fatal(out)
		}
	}
	if total != 13 {
		t.Fatal("wrong deletion count", total)
	}
	for table, want := range before {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatal("original data changed", table, n, want, err)
		}
	}
	checkHidden := func(want int) {
		t.Helper()
		rows, err := s.NexovccCases()
		if err != nil || len(rows) != 19 {
			t.Fatal("internal history lost", rows, err)
		}
		n := 0
		for _, c := range rows {
			if c.RemovedAt != "" {
				n++
			}
		}
		if n != want {
			t.Fatal("wrong hidden count", n, want)
		}
	}
	checkHidden(13)
	order, snapshot, err := s.GPTPayOrder("cleanup-00")
	if err != nil || snapshot.APIKey != "supplier-key" {
		t.Fatal("original order lost", err)
	}
	if err = s.UpdateGPTPayOrder(order); err != nil {
		t.Fatal(err)
	}
	checkHidden(13) // Unchanged failed-order refresh must not undo cleanup.
	order.Status = "success"
	if err = s.UpdateGPTPayOrder(order); err == nil {
		t.Fatal("stale refresh resurrected an explicitly resolved order")
	}
	checkHidden(13)
	if err = s.SetNexovccCase("cleanup-01", "unopened", "重新核实该笔记录", 0); err != nil {
		t.Fatal(err)
	}
	checkHidden(12)
	if _, err = s.db.Exec(`INSERT INTO nexovcc_case_matches(order_id,group_id,returned,profile) VALUES(?,?,?,?)`, "cleanup-02", "late-payment", 0, match); err != nil {
		t.Fatal(err)
	}
	checkHidden(11)
	var audits int
	if err = s.db.QueryRow(`SELECT count(*) FROM nexovcc_audit WHERE json_extract(profile,'$.action')='delete_unopened_case'`).Scan(&audits); err != nil || audits != 13 {
		t.Fatal("missing audit or duplicate cleanup", audits, err)
	}
}
