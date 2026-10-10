package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/gptpay"
)

func TestProCardCustomLimitPersistenceAndSharedIdentity(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	capacityCard(t, s, "card")
	capacityCard(t, s, "duplicate")
	card, secret, err := s.GPTPayCard("card")
	if err != nil || card.MaxAccounts != nil || card.AccountLimit != 3 {
		t.Fatal("legacy default", card, err)
	}
	limit := 5
	card.MaxAccounts = &limit
	if card, err = s.SaveGPTPayCard(card, secret); err != nil || card.AccountLimit != 5 || card.RemainingAccounts != 5 {
		t.Fatal("custom limit not returned", card, err)
	}
	// The duplicate row has the same effective limit and cannot bypass it.
	duplicate, _, _ := s.GPTPayCard("duplicate")
	if duplicate.MaxAccounts == nil || *duplicate.MaxAccounts != 5 || duplicate.AccountLimit != 5 {
		t.Fatal("duplicate cap differs", duplicate)
	}
	other := secret
	other.Number = "4000000000000002"
	if _, err = s.SaveGPTPayCard(gptpay.Card{ID: "other", Enabled: true}, other); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGPTPaySettings(gptpay.Settings{CardAccountLimit: 7}, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	card, secret, err = s.GPTPayCard("card")
	if err != nil || card.AccountLimit != 5 {
		t.Fatal("restart/global edit lost override", card, err)
	}
	otherCard, _, _ := s.GPTPayCard("other")
	if otherCard.AccountLimit != 7 || otherCard.MaxAccounts != nil {
		t.Fatal("unrelated card should follow global", otherCard)
	}
	// Deleting/readding a duplicate must preserve the physical card's policy.
	if err = s.DeleteGPTPayCard("card"); err != nil {
		t.Fatal(err)
	}
	capacityCard(t, s, "card")
	card, secret, _ = s.GPTPayCard("card")
	if card.AccountLimit != 5 {
		t.Fatal("recreate bypassed limit")
	}
	reset := 0
	card.MaxAccounts = &reset
	if _, err = s.SaveGPTPayCard(card, secret); err != nil {
		t.Fatal(err)
	}
	duplicate, _, _ = s.GPTPayCard("duplicate")
	if duplicate.MaxAccounts != nil || duplicate.AccountLimit != 7 {
		t.Fatal("reset did not restore shared global default", duplicate)
	}
	for _, invalid := range []int{-1, 10001} {
		card.MaxAccounts = &invalid
		if _, err = s.SaveGPTPayCard(card, secret); err == nil {
			t.Fatal("invalid limit accepted", invalid)
		}
	}
}

func TestProCardCustomLimitConcurrentOrdersAndReservations(t *testing.T) {
	s := transferStore(t)
	capacityCard(t, s, "card")
	card, secret, _ := s.GPTPayCard("card")
	limit := 2
	card.MaxAccounts = &limit
	if _, err := s.SaveGPTPayCard(card, secret); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			email := fmt.Sprintf("custom-%d@example.com", i)
			var err error
			if i%2 == 0 {
				err = s.ReserveProCard(email, "card", false)
			} else {
				o := transferOrder(email, fmt.Sprint(i), "submitting")
				err = s.CreateGPTPayOrder(o.Order, o.Snapshot)
			}
			if err == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	card, _, _ = s.GPTPayCard("card")
	if accepted.Load() != 2 || card.PendingAccounts != 2 || card.RemainingAccounts != 0 || card.Available {
		t.Fatal("custom cap overbooked", accepted.Load(), card)
	}
	if items, n, err := s.GPTPayCards(10, 0, true); err != nil || n != 0 || len(items) != 0 {
		t.Fatal("full custom card selectable", err)
	}
}

func TestProCardLowerLimitPreservesAdmittedWork(t *testing.T) {
	s := transferStore(t)
	capacityCard(t, s, "card")
	card, secret, _ := s.GPTPayCard("card")
	limit := 2
	card.MaxAccounts = &limit
	if _, err := s.SaveGPTPayCard(card, secret); err != nil {
		t.Fatal(err)
	}
	paid := transferOrder("paid@example.com", "paid", "success")
	if err := s.CreateGPTPayOrder(paid.Order, paid.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveProCard("admitted@example.com", "card", false); err != nil {
		t.Fatal(err)
	}
	limit = 1
	if _, err := s.SaveGPTPayCard(card, secret); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveProCard("new@example.com", "card", false); err == nil {
		t.Fatal("new reservation accepted after lowering")
	}
	fresh := transferOrder("fresh@example.com", "fresh", "submitting")
	if err := s.CreateGPTPayOrder(fresh.Order, fresh.Snapshot); err == nil {
		t.Fatal("new manual order accepted after lowering")
	}
	inflight := transferOrder("admitted@example.com", "admitted", "submitting")
	if err := s.CreateGPTPayOrder(inflight.Order, inflight.Snapshot); err != nil {
		t.Fatal("admitted work blocked", err)
	}
	inflight.Order.Status = "success"
	if err := s.UpdateGPTPayOrder(inflight.Order); err != nil {
		t.Fatal(err)
	}
	card, secret, _ = s.GPTPayCard("card")
	if card.OpenedAccounts != 2 || card.PendingAccounts != 0 || card.AccountLimit != 1 || card.RemainingAccounts != 0 {
		t.Fatal("wrong counts after lowering", card)
	}
	limit = 4
	card.MaxAccounts = &limit
	if _, err := s.SaveGPTPayCard(card, secret); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveProCard("after-raise@example.com", "card", false); err != nil {
		t.Fatal("raising did not free capacity", err)
	}
}
