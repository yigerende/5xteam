package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNexovccResolveDeletedOccupancy(t *testing.T) {
	for _, status := range []string{"success", "failed", "manual_review", "submission_unknown"} {
		for _, outcome := range []string{"success_removed", "not_activated"} {
			t.Run(status+"/"+outcome, func(t *testing.T) {
				s := transferStore(t)
				card := nexStoreCard(t, s)
				o := transferOrder("deleted@example.com", "old-order", status)
				o.Order.CardID = card.ID
				if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
					t.Fatal(err)
				}
				in := EJHOccupancyResolutionInput{CardID: card.ID, Email: o.Order.Email, ExpectedStatus: status, Outcome: outcome, Evidence: "Verified supplier result and removal", Confirm: true}
				if err := s.ResolveEJHOccupancy(o.Order.ID, in); err == nil {
					t.Fatal("wrong issuer accepted")
				}
				err := s.ResolveNexovccOccupancy(o.Order.ID, in)
				conflict := status == "success" && outcome == "not_activated" || status == "failed" && outcome == "success_removed"
				if conflict {
					if err == nil {
						t.Fatal("terminal conflict accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				cases, err := s.NexovccCases()
				if err != nil || len(cases) != 1 {
					t.Fatal(cases, err)
				}
				local, secret, err := s.GPTPayCard(card.ID)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "success_removed" {
					if cases[0].State != "waiting" || cases[0].RequestedAt == "" || local.ActiveAccounts != 1 || local.Available {
						t.Fatal("successful order prematurely released", cases, local)
					}
					if err = s.ReserveProCard("next@example.com", card.ID, false); err == nil {
						t.Fatal("pending return reused")
					}
					if err = s.SetNexovccCase(cases[0].ID, "returned", "Confirmed actual return", cases[0].Revision); err != nil {
						t.Fatal(err)
					}
				} else if cases[0].State != "unopened" || cases[0].RequestedAt != "" || !local.Available {
					t.Fatal(cases, local)
				}
				max := 3
				local.MaxAccounts = &max
				if _, err = s.SaveGPTPayCard(local, secret); err != nil {
					t.Fatal(err)
				}
				if err = s.ReserveProCard("next@example.com", card.ID, false); err != nil {
					t.Fatal("returned card not reusable", err)
				}
				if err = s.ResolveNexovccOccupancy(o.Order.ID, in); err != nil {
					t.Fatal("not idempotent", err)
				}
				local, _, _ = s.GPTPayCard(card.ID)
				if local.ActiveAccounts != 1 || local.ActiveEmail != "next@example.com" {
					t.Fatal("repeat released new reservation", local)
				}
				order, snapshot, err := s.GPTPayOrder(o.Order.ID)
				if err != nil || order.OccupancyResolution == nil || snapshot.Input.Number == "" || !reflect.DeepEqual(order.Remote, o.Order.Remote) {
					t.Fatal("lost original evidence", order, err)
				}
				var raw string
				if err = s.db.QueryRow(`SELECT profile FROM nexovcc_audit WHERE json_extract(profile,'$.action')='deleted_account_occupancy'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var audit map[string]json.RawMessage
				if err = json.Unmarshal([]byte(raw), &audit); err != nil || len(audit["before"]) == 0 {
					t.Fatal("lost audit", err)
				}
			})
		}
	}
}

func TestNexovccManualStateRestoresOccupancy(t *testing.T) {
	s := transferStore(t)
	card := nexStoreCard(t, s)
	o := transferOrder("deleted@example.com", "failed-order", "failed")
	o.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"opened", "waiting", "returned", "unopened", "auto"} {
		if err := s.SetNexovccCase(o.Order.ID, state, "Verified actual state", i); err != nil {
			t.Fatal(err)
		}
		local, _, err := s.GPTPayCard(card.ID)
		occupied := state == "opened" || state == "waiting"
		if err != nil || local.Available == occupied || (local.ActiveAccounts > 0) != occupied {
			t.Fatal(state, local, err)
		}
	}
}

func TestNexovccResolveDeletedOccupancyGuards(t *testing.T) {
	for _, scenario := range []string{"no_confirm", "no_evidence", "wrong_email", "stale", "restored", "processing", "remote_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			s := transferStore(t)
			card := nexStoreCard(t, s)
			status := "manual_review"
			if scenario == "processing" {
				status = "processing"
			}
			o := transferOrder("deleted@example.com", "old-order", status)
			o.Order.CardID = card.ID
			if scenario == "remote_conflict" {
				o.Order.Remote.Status = "failed"
			}
			if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
				t.Fatal(err)
			}
			in := EJHOccupancyResolutionInput{CardID: card.ID, Email: o.Order.Email, ExpectedStatus: status, Outcome: "success_removed", Evidence: "Verified supplier and removal", Confirm: true}
			switch scenario {
			case "no_confirm":
				in.Confirm = false
			case "no_evidence":
				in.Evidence = ""
			case "wrong_email":
				in.Email = "other@example.com"
			case "stale":
				in.ExpectedStatus = "success"
			case "restored":
				transferSource(t, s, o.Order.Email)
			}
			if err := s.ResolveNexovccOccupancy(o.Order.ID, in); err == nil {
				t.Fatal("guard bypassed")
			}
			got, _, _ := s.GPTPayOrder(o.Order.ID)
			if got.Status != status || got.OccupancyResolution != nil {
				t.Fatal("order changed", got)
			}
			if err := s.SetNexovccCase(o.Order.ID, "returned", "Cannot skip payment uncertainty", 0); err == nil {
				t.Fatal("unknown released")
			}
		})
	}
}
