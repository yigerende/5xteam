package store

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"fmt"
	"sync"
	"testing"
	"time"
)

func capacityCard(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.SaveGPTPayCard(gptpay.Card{ID: id, Enabled: true}, transferOrder("a@example.com", "x", "success").Snapshot.Input.CardSecret)
	if err != nil {
		t.Fatal(err)
	}
}
func TestProCardLimitConcurrentManualAndAutomatic(t *testing.T) {
	s := transferStore(t)
	capacityCard(t, s, "card")
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []string{}
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			email := fmt.Sprintf("c%d@example.com", i)
			var err error
			if i%2 == 0 {
				err = s.ReserveProCard(email, "card", false)
			} else {
				o := transferOrder(email, fmt.Sprint(i), "submitting")
				err = s.CreateGPTPayOrder(o.Order, o.Snapshot)
			}
			if err == nil {
				mu.Lock()
				accepted = append(accepted, email)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 3 {
		t.Fatalf("capacity race allowed %d accounts", len(accepted))
	}
	c, _, err := s.GPTPayCard("card")
	if err != nil || c.OpenedAccounts != 0 || c.PendingAccounts != 3 || c.Available {
		t.Fatalf("bad capacity %+v %v", c, err)
	}
	cards, n, err := s.GPTPayCards(10, 0, true)
	if err != nil || len(cards) != 0 || n != 0 {
		t.Fatal("full card remained selectable")
	}
	capacityCard(t, s, "duplicate-card")
	c, _, _ = s.GPTPayCard("duplicate-card")
	if c.Available {
		t.Fatal("duplicate card bypassed cap")
	}
	// A reservation converts to an order without counting itself twice.
	var reserved string
	s.db.QueryRow(`SELECT email FROM pro_card_reservations LIMIT 1`).Scan(&reserved)
	if reserved != "" {
		o := transferOrder(reserved, "converted", "submitting")
		if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
			t.Fatal(err)
		}
		o.Order.Status = "success"
		if err = s.UpdateGPTPayOrder(o.Order); err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ = s.GPTPayCard("card")
	if c.OpenedAccounts+c.PendingAccounts != 3 {
		t.Fatal("conversion lost/doubled capacity")
	}
	// Deleting a card does not delete its historical usage.
	if err = s.DeleteGPTPayCard("card"); err != nil {
		t.Fatal(err)
	}
	capacityCard(t, s, "card")
	c, _, _ = s.GPTPayCard("card")
	if c.Available {
		t.Fatal("delete/recreate reset usage")
	}
}
func TestProCardFailedOrdersReleaseAndImportedOrdersCount(t *testing.T) {
	s := transferStore(t)
	capacityCard(t, s, "card")
	v := gptpay.Settings{CardAccountLimit: 1}
	s.SaveGPTPaySettings(v, "")
	o := transferOrder("a@example.com", "a", "submission_unknown")
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveProCard("b@example.com", "card", false); err == nil {
		t.Fatal("unknown payment released capacity")
	}
	o.Order.Status = "failed"
	s.UpdateGPTPayOrder(o.Order)
	if err := s.ReserveProCard("b@example.com", "card", false); err != nil {
		t.Fatal(err)
	}
	s.ReleaseProCard("b@example.com")
	src := transferStore(t)
	transferSource(t, src, "import@example.com")
	paid := transferOrder("import@example.com", "paid", "success")
	src.CreateGPTPayOrder(paid.Order, paid.Snapshot)
	f, err := src.ExportProAccounts([]string{"import@example.com"}, model.ProDownstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportProAccount(f.Accounts[0], f.ExportedAt, false, model.ProDownstreamRef{}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := s.GPTPayCard("card")
	if c.OpenedAccounts != 1 || c.Available {
		t.Fatalf("imported card usage missing %+v", c)
	}
}
func TestProPopulationLimitReservationsAndCandidateOrder(t *testing.T) {
	s := transferStore(t)
	capacityCard(t, s, "card")
	cfg := model.DefaultProSettings()
	cfg.MaxUnmerged = 5
	s.SaveProSettings(cfg, "", "")
	s.SaveGPTPaySettings(gptpay.Settings{CardAccountLimit: 10}, "")
	for i := 0; i < 4; i++ {
		email := fmt.Sprintf("paid%d@example.com", i)
		transferSource(t, s, email)
		s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.CurrentPlanType = "pro" })
	}
	for i := 0; i < 3; i++ {
		email := fmt.Sprintf("fresh%d@example.com", i)
		transferSource(t, s, email)
		s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.CreatedAt = time.Now().Add(time.Duration(i) * time.Hour) })
	}
	candidates, err := s.ProScheduleCandidates(1)
	if err != nil || len(candidates) != 1 || candidates[0] != "fresh0@example.com" {
		t.Fatal("wrong bounded chronological selection", candidates, err)
	}
	var wg sync.WaitGroup
	ok := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok <- s.ReserveProCard(fmt.Sprintf("claim%d@example.com", i), "card", true) == nil
		}(i)
	}
	wg.Wait()
	close(ok)
	n := 0
	for v := range ok {
		if v {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("remaining one account reserved %d", n)
	}
	v, err := s.ProCapacity()
	if err != nil || v.OpenedUnmerged != 4 || v.Pending != 1 {
		t.Fatalf("wrong population %+v %v", v, err)
	}
	s.UpdateProAccount("paid0@example.com", func(p *model.MailAccountProfile) { p.SpaceMergedOnce = true })
	if err = s.ReserveProCard("after-merge@example.com", "card", true); err != nil {
		t.Fatal("merge did not free population", err)
	}
}
