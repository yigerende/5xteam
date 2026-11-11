package store

import (
	"reflect"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestCardCaseReconciliationEndsOnlyMatchingRestockWait(t *testing.T) {
	for _, provider := range []string{"ejh", "nexovcc"} {
		for _, entry := range []string{"resolve", "delete"} {
			for _, status := range []string{"unknown", "attention"} {
				t.Run(provider+"/"+entry+"/"+status, func(t *testing.T) {
					s := transferStore(t)
					var card string
					if provider == "nexovcc" {
						card = nexStoreCard(t, s).ID
					} else {
						card = ejhFixtureCard(t, s).ID
					}
					o := transferOrder("deleted-wait@example.com", "unpaid-wait", "manual_review")
					o.Order.CardID = card
					if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
						t.Fatal(err)
					}
					now := time.Now()
					task := restock.Task{ID: "waiting-task", Email: o.Order.Email, OrderID: o.Order.ID, CardID: card, Status: status, Cost: 25, CreatedAt: now, UpdatedAt: now, SubmittedAt: &now}
					if _, err := s.db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, task.ID, task.Email, task.Status, formatTime(now), task.Cost, mustJSON(task)); err != nil {
						t.Fatal(err)
					}
					if entry == "resolve" {
						in := EJHOccupancyResolutionInput{CardID: card, Email: o.Order.Email, ExpectedStatus: "manual_review", Outcome: "not_activated", Confirm: true, Evidence: "Operator verified no payment or activation"}
						var err error
						if provider == "ejh" {
							err = s.ResolveEJHOccupancy(o.Order.ID, in)
						} else {
							err = s.ResolveNexovccOccupancy(o.Order.ID, in)
						}
						if err != nil {
							t.Fatal(err)
						}
					} else if provider == "ejh" {
						if _, err := s.DeleteEJHUnopenedCases(o.Order.ID); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := s.DeleteNexovccUnopenedCases(); err != nil {
							t.Fatal(err)
						}
					}
					got, err := s.ProRestockTask(task.ID)
					if err != nil || got.Active() || got.Status != "failed" || got.StoppedAt == nil || got.Cost != 25 || got.OrderID != task.OrderID || got.ReadyAt != nil {
						t.Fatal(got, err)
					}
					c, _, err := s.GPTPayCard(card)
					if err != nil || c.ActiveAccounts != 0 || c.PendingAccounts != 0 {
						t.Fatal("card still held", c, err)
					}
					order, _, err := s.GPTPayOrder(o.Order.ID)
					if err != nil || order.Active() || order.Status != "failed" || order.OccupancyResolution == nil || !reflect.DeepEqual(order.Remote, o.Order.Remote) {
						t.Fatal(order, err)
					}
				})
			}
		}
	}
}

func TestCardCaseRestockWaitNeverOverridesRunningOrDifferentOrder(t *testing.T) {
	for _, scenario := range []string{"running", "submitting", "reserved", "waiting_card", "other_order", "unknown_without_link", "remote_running", "paid", "account_running"} {
		t.Run(scenario, func(t *testing.T) {
			s := transferStore(t)
			_, order, in := occupancyResolutionFixture(t, s, "manual_review")
			in.Outcome = "not_activated"
			now := time.Now()
			task := restock.Task{ID: "wait", Email: order.Email, OrderID: order.ID, Status: "unknown", CreatedAt: now}
			switch scenario {
			case "running", "submitting", "reserved", "waiting_card":
				task.Status = scenario
			case "other_order":
				task.OrderID = "newer-order"
			case "unknown_without_link":
				task.OrderID = ""
			case "remote_running":
				order.Remote.AutomaticRunning = true
			case "paid":
				order.Remote.PaymentState = "succeeded"
			case "account_running":
				transferSource(t, s, order.Email)
				if _, err := s.UpdateProAccount(order.Email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "running" }); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, task.ID, task.Email, task.Status, formatTime(now), task.Cost, mustJSON(task)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE gptpay_orders SET profile=? WHERE id=?`, mustJSON(order), order.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.ResolveEJHOccupancy(order.ID, in); err == nil {
				t.Fatal("live or unlinked task incorrectly released")
			}
			got, _ := s.ProRestockTask(task.ID)
			if !got.Active() {
				t.Fatal("task changed after rejected resolution")
			}
		})
	}
}

func TestCardCaseDeletionReleasesOnlyVerifiedOrder(t *testing.T) {
	for _, provider := range []string{"ejh", "nexovcc"} {
		for _, status := range []string{"manual_review", "submission_unknown", "failed"} {
			t.Run(provider+"/"+status, func(t *testing.T) {
				dir := t.TempDir()
				s, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { s.Close() }()
				var cardID string
				if provider == "ejh" {
					cardID = ejhFixtureCard(t, s).ID
				} else {
					cardID = nexStoreCard(t, s).ID
				}
				email := "verified@example.com"
				transferSource(t, s, email)
				o := transferOrder(email, "verified-order", status)
				o.Order.CardID = cardID
				o.Order.Provider = gptpay.XUPay
				o.Order.Remote.ID = "original"
				o.Order.Remote.Status = "manual_review"
				o.Order.Remote.PaymentState = "action_required"
				o.Order.Remote.CancellationStatus = "waiting"
				if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
					t.Fatal(err)
				}
				remove := func() int {
					t.Helper()
					if provider == "ejh" {
						ids, e := s.DeleteEJHUnopenedCases(o.Order.ID)
						if e != nil {
							t.Fatal(e)
						}
						return len(ids)
					}
					out, e := s.DeleteNexovccUnopenedCases()
					if e != nil {
						t.Fatal(e)
					}
					return out.Deleted
				}
				if remove() != 1 || remove() != 0 {
					t.Fatal("delete is not idempotent")
				}
				got, snap, err := s.GPTPayOrder(o.Order.ID)
				if err != nil || got.Status != "failed" || got.OccupancyResolution == nil || got.Active() || got.NeedsStatusPoll() || got.RetryBlocked() || got.PaymentReviewRequired() || !reflect.DeepEqual(got.Remote, o.Order.Remote) || snap.APIKey != o.Snapshot.APIKey {
					t.Fatal("reconciliation lost evidence or retained hold", got, err)
				}
				c, _, err := s.GPTPayCard(cardID)
				if err != nil || c.ActiveAccounts != 0 || c.PendingAccounts != 0 || len(c.Occupants) != 0 {
					t.Fatal("occupancy retained", c, err)
				}
				if _, _, err = s.MailAccountCredential(email); err != nil {
					t.Fatal("account removed", err)
				}
				if err = s.UpdateGPTPayOrder(o.Order); err == nil {
					t.Fatal("late update resurrected resolved order")
				}
				s.Close()
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				c, _, err = s.GPTPayCard(cardID)
				if err != nil || c.ActiveAccounts != 0 || c.PendingAccounts != 0 {
					t.Fatal("restart restored hold", c, err)
				}
				if due, e := s.GPTPayOrdersDue(time.Now().Add(time.Hour), 20); e != nil || len(due) != 0 {
					t.Fatal("resolved order keeps polling", due, e)
				}
			})
		}
	}
}

func TestCardCaseDeletionGuardsActiveAndPaidOrders(t *testing.T) {
	for _, scenario := range []string{"processing", "submitting", "auto_running", "check_pending", "account_running", "paid", "success_time"} {
		t.Run(scenario, func(t *testing.T) {
			s := transferStore(t)
			card := ejhFixtureCard(t, s)
			email := "guard@example.com"
			transferSource(t, s, email)
			o := transferOrder(email, "guard-order", "manual_review")
			o.Order.CardID = card.ID
			switch scenario {
			case "processing", "submitting":
				o.Order.Status = scenario
			case "auto_running":
				o.Order.Remote.AutomaticRunning = true
			case "check_pending":
				o.Order.CheckPending = true
			case "paid":
				o.Order.Remote.PaymentState = "succeeded"
			case "success_time":
				now := time.Now()
				o.Order.SucceededAt = &now
			case "account_running":
				_, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "running"; p.ProAuto.OrderID = "new-order" })
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
				t.Fatal(err)
			}
			if scenario == "success_time" {
				if _, err := s.db.Exec(`UPDATE gptpay_orders SET profile=json_set(profile,'$.succeeded_at',?) WHERE id=?`, formatTime(time.Now()), o.Order.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.DeleteEJHUnopenedCases(o.Order.ID); err == nil {
				t.Fatal("deleted live or paid order")
			}
			rows, err := s.EJHCases()
			if err != nil || rows[0].RemovedAt != nil {
				t.Fatal("partially hidden", rows, err)
			}
			got, _, _ := s.GPTPayOrder(o.Order.ID)
			if got.OccupancyResolution != nil || got.Status != o.Order.Status {
				t.Fatal("guard mutated order")
			}
		})
	}
}

func TestCardCaseDeletionRepairsHistoricalMarkersOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	card := ejhFixtureCard(t, s)
	cfg, key, _ := s.EJHSettings()
	cfg.SingleActiveAccount = false
	cfg.DefaultMaxAccounts = 100
	if _, err = s.SaveEJHSettings(cfg, key); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"removed-review", "deleted-account-only", "running", "paid"} {
		o := transferOrder(id+"@example.com", id, "manual_review")
		o.Order.CardID = card.ID
		if id == "running" {
			o.Order.Status = "processing"
		}
		if id == "paid" {
			o.Order.Remote.PaymentState = "succeeded"
		}
		if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
			t.Fatal(err)
		}
		if id != "deleted-account-only" {
			if _, err = s.db.Exec(`UPDATE ejh_cases SET profile=json_set(profile,'$.removed_at',?) WHERE id=?`, formatTime(time.Now()), id); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"removed-review", "deleted-account-only", "running", "paid"} {
		o, _, e := s.GPTPayOrder(id)
		if e != nil {
			t.Fatal(e)
		}
		if (o.OccupancyResolution != nil) != (id == "removed-review") {
			t.Fatal("historical marker handling", id, o)
		}
	}
}

func TestCardCaseDeletionPreservesNewOccupancy(t *testing.T) {
	s := transferStore(t)
	card, old, in := occupancyResolutionFixture(t, s, "manual_review")
	transferSource(t, s, old.Email)
	in.Outcome = "not_activated"
	if err := s.ResolveEJHOccupancy(old.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteEJHUnopenedCases(old.ID); err != nil {
		t.Fatal(err)
	}
	next := transferOrder("next@example.com", "new-success", "success")
	next.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(next.Order, next.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteEJHUnopenedCases(old.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileDeletedUnopenedCases(); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GPTPayCard(card.ID)
	if err != nil || got.ActiveAccounts != 1 || got.ActiveEmail != next.Order.Email {
		t.Fatal("new successful occupancy released", got, err)
	}
}
