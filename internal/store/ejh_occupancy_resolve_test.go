package store

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func occupancyResolutionFixture(t *testing.T, s *Store, status string) (gptpay.Card, gptpay.Order, EJHOccupancyResolutionInput) {
	t.Helper()
	card := ejhFixtureCard(t, s)
	o := transferOrder("deleted@example.com", "legacy-occupancy", status)
	o.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	return card, o.Order, EJHOccupancyResolutionInput{CardID: card.ID, Email: o.Order.Email, ExpectedStatus: status, Outcome: "success_removed", Evidence: "Verified supplier order and completed space removal", Confirm: true}
}

func TestEJHResolveDeletedOccupancy(t *testing.T) {
	for _, status := range []string{"success", "submission_unknown", "manual_review", "failed"} {
		for _, outcome := range []string{"success_removed", "not_activated"} {
			t.Run(status+"/"+outcome, func(t *testing.T) {
				s := transferStore(t)
				card, old, in := occupancyResolutionFixture(t, s, status)
				in.Outcome = outcome
				err := s.ResolveEJHOccupancy(old.ID, in)
				conflict := status == "success" && outcome == "not_activated" || status == "failed" && outcome == "success_removed"
				if conflict {
					if err == nil {
						t.Fatal("accepted conflicting terminal result")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, _, err := s.GPTPayCard(card.ID)
				wantOpened := 0
				if outcome == "success_removed" {
					wantOpened = 1
				}
				if err != nil || got.Available != (outcome == "not_activated") || got.ActiveAccounts != wantOpened || got.PendingAccounts != 0 || got.OpenedAccounts != wantOpened {
					t.Fatal(got, err)
				}
				order, snapshot, err := s.GPTPayOrder(old.ID)
				if err != nil || order.Active() || order.OccupancyResolution == nil || !reflect.DeepEqual(order.Remote, old.Remote) || snapshot.Input.Number == "" {
					t.Fatal(order, err)
				}
				cases, err := s.EJHCases()
				if err != nil || len(cases) != 1 {
					t.Fatal(cases, err)
				}
				if outcome == "success_removed" {
					if cases[0].State != "waiting" || cases[0].RequestedAt == nil {
						t.Fatal("lost pending return", cases)
					}
					if err = s.EJHBeginOperation(ejh.Operation{ID: "cancel", Method: "cancel", CardID: card.ID, OrderNo: "fixture-card", Status: "submitting"}); err == nil {
						t.Fatal("allowed cancellation before return")
					}
				} else if cases[0].OpenedAt != nil || cases[0].RequestedAt != nil {
					t.Fatal("invented payment or return", cases)
				}
				audits, err := EJHRecords[map[string]any](s, "match_audit")
				if err != nil || len(audits) != 1 || audits[0]["action"] != "deleted_account_occupancy" {
					t.Fatal(audits, err)
				}
				// Historical polls cannot overwrite manual results or remove their evidence.
				if err = s.UpdateGPTPayOrder(old); err == nil {
					t.Fatal("accepted stale write")
				}
				if err = s.UpdateGPTPayOrder(order); err != nil {
					t.Fatal("fresh refund poll blocked", err)
				}
				if outcome == "success_removed" {
					ejhReceiveForOrder(t, s, card.ID, old.ID)
				}
				next := transferOrder("new@example.com", "new-payment", "success")
				next.Order.CardID = card.ID
				if err = s.CreateGPTPayOrder(next.Order, next.Snapshot); err != nil {
					t.Fatal("released card cannot be reused", err)
				}
				if err = s.ResolveEJHOccupancy(old.ID, in); err != nil {
					t.Fatal("retry not idempotent", err)
				}
				got, _, err = s.GPTPayCard(card.ID)
				if err != nil || got.ActiveAccounts != 1 || got.ActiveEmail != next.Order.Email {
					t.Fatal("repeat released a newer order", got, err)
				}
			})
		}
	}
}

func TestEJHResolveDeletedOccupancyGuards(t *testing.T) {
	for _, scenario := range []string{"no_confirm", "no_evidence", "unknown", "wrong_email", "wrong_card", "stale", "restored", "active_restock", "processing", "remote_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			s := transferStore(t)
			status := "manual_review"
			if scenario == "processing" {
				status = "processing"
			}
			card, order, in := occupancyResolutionFixture(t, s, status)
			switch scenario {
			case "no_confirm":
				in.Confirm = false
			case "no_evidence":
				in.Evidence = " "
			case "unknown":
				in.Outcome = "unknown"
			case "wrong_email":
				in.Email = "other@example.com"
			case "wrong_card":
				other, err := s.ImportEJHCard(ejh.Card{OrderNo: "other", Number: "5555555555554444", Validity: "1299", CVV: "123", Status: "1"}, true)
				if err != nil {
					t.Fatal(err)
				}
				in.CardID = other.ID
			case "stale":
				in.ExpectedStatus = "success"
			case "restored":
				transferSource(t, s, order.Email)
			case "active_restock":
				if _, err := s.db.Exec(`INSERT INTO pro_restock_tasks VALUES('active',?,'activating','now',0,'{}')`, order.Email); err != nil {
					t.Fatal(err)
				}
			case "remote_conflict":
				order.Remote.Status = "failed"
				if err := s.UpdateGPTPayOrder(order); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.ResolveEJHOccupancy(order.ID, in); err == nil {
				t.Fatal("accepted invalid resolution")
			}
			got, _, err := s.GPTPayCard(card.ID)
			if err != nil || got.ActiveAccounts != 1 || got.Available {
				t.Fatal("released invalid occupancy", got, err)
			}
			audits, err := EJHRecords[map[string]any](s, "match_audit")
			if err != nil || len(audits) != 0 {
				t.Fatal("partial write", audits, err)
			}
		})
	}
}

func TestEJHResolveDeletedOccupancyAcrossStoresAndRestart(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	card, old, in := occupancyResolutionFixture(t, a, "submission_unknown")
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); failures <- []*Store{a, b}[i%2].ResolveEJHOccupancy(old.ID, in) }(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = b.UpdateGPTPayOrder(old); err == nil {
		t.Fatal("other store overwrote resolution")
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, _, err := c.GPTPayCard(card.ID)
	if err != nil || got.ActiveAccounts != 1 || got.OpenedAccounts != 1 || got.Available {
		t.Fatal(got, err)
	}
	audits, err := EJHRecords[json.RawMessage](c, "match_audit")
	if err != nil || len(audits) != 1 {
		t.Fatal(audits, err)
	}
}
