package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

// Build the same persisted links as ClaimProRechargeReplacement. Old payments
// intentionally remain active reviews, while only the final delivery is ready.
func restockDeliveryFixture(t *testing.T, s *Store, count int) []restock.Task {
	t.Helper()
	now := time.Now().Add(-time.Minute)
	tasks := make([]restock.Task, count)
	for i := range tasks {
		id := "original"
		if i > 0 {
			id = fmt.Sprintf("recharge:flow-%d", i-1)
		}
		tasks[i] = restock.Task{ID: id, Email: fmt.Sprintf("delivery-%d@example.com", i), DecisionID: "delivery-batch", Status: "unknown", Stage: "recharge", GroupID: 1, Binding: "pool", OrderID: fmt.Sprintf("payment-%d", i), CardID: fmt.Sprintf("card-%d", i), Cost: 25, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now, SubmittedAt: &now}
	}
	for i, task := range tasks {
		r := &model.ProRechargeReplacement{RootEmail: tasks[0].Email}
		if i > 0 {
			r.PreviousEmail = tasks[i-1].Email
		}
		if i+1 < len(tasks) {
			r.NextEmail = tasks[i+1].Email
		}
		payment := "manual_review"
		if i == len(tasks)-1 {
			ready := now.Add(30 * time.Second)
			task.Status, task.Stage, task.AccountID, task.ReadyAt = "ready", "quota", 3965, &ready
			payment = "success"
		}
		tasks[i] = task
		p := model.MailAccountProfile{Email: task.Email, ManagementScope: "pro", ProRestockTaskID: task.ID, ProAuto: model.ProAutoState{ID: fmt.Sprintf("flow-%d", i), OrderID: task.OrderID, CardID: task.CardID, Replacement: r}}
		if _, err := s.SaveMailAccount(p, model.MailAccountCredentials{Email: task.Email}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, task.ID, task.Email, task.Status, formatTime(task.CreatedAt), task.Cost, mustJSON(task)); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateGPTPayOrder(gptpay.Order{ID: task.OrderID, Email: task.Email, CardID: task.CardID, Status: payment, SubmissionStarted: true, CreatedAt: now}, gptpay.Snapshot{}); err != nil {
			t.Fatal(err)
		}
	}
	return tasks
}

func reconcileDeliveries(t *testing.T, s *Store) []restock.Task {
	t.Helper()
	tasks, err := s.ProRestockTasks()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileProRestockDeliveries(tasks); err != nil {
		t.Fatal(err)
	}
	return tasks
}

func TestRestockDeliveryPreservesFinanceAndReleasesOnlyInflight(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err = s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	initial := restockDeliveryFixture(t, s, 3)
	before, _, _ := s.GPTPayOrder(initial[0].OrderID)
	profileBefore, _, _ := s.MailAccountCredential(initial[0].Email)
	stale, _ := s.ProRestockTasks()
	tasks := reconcileDeliveries(t, s)
	for _, task := range tasks[:2] {
		if task.Active() || task.Status != "unknown" || task.FulfilledByTaskID != initial[2].ID || task.FulfilledByEmail != initial[2].Email || !task.FulfilledAt.Equal(*initial[2].ReadyAt) || task.ReadyAt != nil || task.CardID == "" || task.Cost != 25 {
			t.Fatal("lost delivery/financial boundary", task)
		}
	}
	// A monitoring response read before reconciliation cannot undo the milestone.
	stale[0].Message = "stale monitor"
	if err = s.SaveProRestockObservation(&stale[0]); err != nil || stale[0].Active() {
		t.Fatal("stale monitor restored inflight", stale[0], err)
	}
	after, _, _ := s.GPTPayOrder(initial[0].OrderID)
	profileAfter, _, _ := s.MailAccountCredential(initial[0].Email)
	if mustJSON(before) != mustJSON(after) || !after.Active() || mustJSON(profileBefore) != mustJSON(profileAfter) {
		t.Fatal("original payment/account was modified")
	}
	limits, err := s.ProRestockLimits(time.Now().Add(72 * time.Hour))
	if err != nil || limits.DayCost != 50 || limits.HourCost != 50 {
		t.Fatal("unknown original payment budgets were released", limits, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ProRestockTask(initial[0].ID)
	if err != nil || got.Active() || got.ReadyAt != nil {
		t.Fatal("restart lost delivery result", got, err)
	}
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: "next-batch@example.com", ManagementScope: "pro"}, model.MailAccountCredentials{Email: "next-batch@example.com"}); err != nil {
		t.Fatal(err)
	}
	d := restock.Decision{ID: "next", At: time.Now(), Version: c.Version, Suggested: 1, Snapshot: restock.Snapshot{Binding: "pool", GroupID: 1, Valid: true, LoadValid: true, At: initial[2].UpdatedAt}}
	if _, err = s.ReserveProRestock(d, "", 0); err == nil {
		t.Fatal("old monitoring snapshot accepted after delivery")
	}
	d.Snapshot.At, d.Snapshot.CollectionStartedAt = time.Now().Add(time.Second), time.Now().Add(time.Second)
	if added, err := s.ReserveProRestock(d, "", 0); err != nil || len(added) != 1 {
		t.Fatal("verified replacement still blocks next batch", added, err)
	}
	// Delivery archives leave the hot monitoring window without deleting history.
	for _, task := range tasks {
		old := time.Now().Add(-72 * time.Hour)
		task.CreatedAt, task.UpdatedAt = old, old
		if _, err = s.db.Exec(`UPDATE pro_restock_tasks SET created_at=?,payload=? WHERE id=?`, formatTime(old), mustJSON(task), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	hot, err := s.ProRestockTasks()
	if err != nil || len(hot) != 1 || hot[0].DecisionID != "next" {
		t.Fatal("completed ancestors scanned forever", hot, err)
	}
	history, err := s.ProRestockDecisionTasks(initial[0].DecisionID)
	if err != nil || len(history) != 3 {
		t.Fatal("decision history lost", history, err)
	}
}

func TestRestockDeliveryRequiresVerifiedLinkedSuccess(t *testing.T) {
	for _, scenario := range []string{"unverified", "no_time", "no_account", "wrong_decision", "wrong_root", "wrong_backlink", "new_flow", "different_order", "unknown_payment", "failed_payment", "stopped", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			s := transferStore(t)
			tasks := restockDeliveryFixture(t, s, 2)
			leaf := tasks[1]
			switch scenario {
			case "unverified":
				leaf.Status = "verifying"
			case "no_time":
				leaf.ReadyAt = nil
			case "no_account":
				leaf.AccountID = 0
			case "wrong_decision":
				leaf.DecisionID = "other-batch"
			case "different_order":
				leaf.OrderID = "other-order"
			case "stopped":
				now := time.Now()
				leaf.StoppedAt = &now
			case "wrong_root", "wrong_backlink", "new_flow", "cycle":
				_, err := s.UpdateProAccount(leaf.Email, func(p *model.MailAccountProfile) {
					switch scenario {
					case "wrong_root":
						p.ProAuto.Replacement.RootEmail = "different@example.com"
					case "wrong_backlink":
						p.ProAuto.Replacement.PreviousEmail = "different@example.com"
					case "new_flow":
						p.ProRestockTaskID = "another-task"
					case "cycle":
						p.ProAuto.Replacement.NextEmail = tasks[0].Email
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "cycle" {
					leaf.Status = "unknown"
				}
			case "unknown_payment", "failed_payment":
				status := "submission_unknown"
				if scenario == "failed_payment" {
					status = "failed"
				}
				if _, err := s.db.Exec(`UPDATE gptpay_orders SET profile=json_set(profile,'$.status',?) WHERE id=?`, status, leaf.OrderID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`UPDATE pro_restock_tasks SET status=?,payload=? WHERE id=?`, leaf.Status, mustJSON(leaf), leaf.ID); err != nil {
				t.Fatal(err)
			}
			got := reconcileDeliveries(t, s)
			if !got[0].Active() || got[0].ReplacementDelivered() {
				t.Fatal("unverified replacement released parent", got[0])
			}
		})
	}
}

func TestRestockDeliveryConcurrentReconcileAndStop(t *testing.T) {
	s := transferStore(t)
	tasks := restockDeliveryFixture(t, s, 2)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reconcileDeliveries(t, s)
		}()
	}
	wg.Wait()
	first, _ := s.ProRestockTask(tasks[0].ID)
	reconcileDeliveries(t, s)
	second, _ := s.ProRestockTask(tasks[0].ID)
	if first.Active() || !first.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatal("non-idempotent reconciliation")
	}
	// A stop that wins the race must be kept; stale observation uses projection.
	s2 := transferStore(t)
	initial := restockDeliveryFixture(t, s2, 2)
	stale, _ := s2.ProRestockTasks()
	if _, _, err := s2.StopProRestockTask(initial[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s2.ReconcileProRestockDeliveries(stale); err != nil {
		t.Fatal(err)
	}
	if stale[0].Status != "cancelled" || stale[0].ReplacementDelivered() {
		t.Fatal("stop overwritten", stale[0])
	}
}
