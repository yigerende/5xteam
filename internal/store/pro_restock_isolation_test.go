package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestRestockIsolationKeepsMoneyAndExcludesOriginal(t *testing.T) {
	s, task := preparationFixture(t)
	task.Status, task.Cost, task.CardProvider = "attention", 2000, "ejh"
	task.Message = "issuer timeout"
	if err := s.SaveProRestockTask(task); err != nil {
		t.Fatal(err)
	}
	op := ejh.Operation{ID: "uncertain", Method: "open", Status: "unknown", RestockTaskID: task.ID, AccountEmail: task.Email, Amount: "20", CreatedAt: time.Now()}
	if err := s.EJHBeginOperation(op); err != nil {
		t.Fatal(err)
	}
	o := transferOrder(task.Email, "uncertain-payment", "submission_unknown")
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	task, _ = s.ProRestockTask(task.ID)
	var done atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := task
			ok, err := s.IsolateProRestockTask(&copy)
			if err != nil {
				t.Error(err)
			}
			if ok {
				done.Add(1)
			}
		}()
	}
	wg.Wait()
	if done.Load() != 1 {
		t.Fatal("isolation not atomic", done.Load())
	}
	current, _ := s.ProRestockTask(task.ID)
	if current.Active() || !current.AutoIsolated || current.Cost != 2000 || current.StoppedAt == nil {
		t.Fatal(current)
	}
	if err := s.SaveProRestockTask(task); err != nil {
		t.Fatal(err)
	}
	current, _ = s.ProRestockTask(task.ID)
	if current.Active() {
		t.Fatal("late worker resurrected task")
	}
	order, _, _ := s.GPTPayOrder(o.Order.ID)
	if order.Status != "submission_unknown" {
		t.Fatal("financial status changed")
	}
	var saved ejh.Operation
	s.EJHGet("operation", op.ID, &saved)
	if saved.Status != "unknown" {
		t.Fatal("issuance changed")
	}
	if s.CheckEJHOpenOwner(task.Email, "") == nil {
		t.Fatal("same account may issue twice")
	}
	if s.CheckEJHOpenOwner("next@example.com", "next-task") != nil {
		t.Fatal("unrelated account blocked")
	}
	candidates, _ := s.ProScheduleCandidates(100)
	for _, email := range candidates {
		if email == task.Email {
			t.Fatal("isolated account reclaimed")
		}
	}
	snap := restock.Snapshot{Binding: task.Binding, GroupID: task.GroupID, Valid: true, LoadValid: true, At: time.Now().Add(time.Second)}
	if restock.WaitForBatch(snap, []restock.Task{current}) != nil {
		t.Fatal("isolated account still blocks batch")
	}
	l, err := s.ProRestockLimits(time.Now())
	if err != nil || l.DayCost != 2000 {
		t.Fatal("lost uncertain budget", l, err)
	}
}

func TestRestockIsolationPreservesScheduledRecovery(t *testing.T) {
	for _, kind := range []string{"retry", "push", "stale", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			s, task := preparationFixture(t)
			task.Status = "attention"
			s.SaveProRestockTask(task)
			task, _ = s.ProRestockTask(task.ID)
			s.UpdateProAccount(task.Email, func(p *model.MailAccountProfile) {
				switch kind {
				case "retry":
					now := time.Now()
					p.ProAuto.RetryAt = &now
				case "push":
					p.PushStatus = "completed"
				case "replacement":
					p.ProAuto.Replacement = &model.ProRechargeReplacement{Pending: true}
				}
			})
			if kind == "stale" {
				copy := task
				copy.Message = "changed"
				s.SaveProRestockTask(copy)
			}
			ok, err := s.IsolateProRestockTask(&task)
			if err != nil || ok {
				t.Fatal("active/replaced state isolated", ok, err)
			}
		})
	}
}

func TestEJHOpenIsolationOwnerSurvivesRestart(t *testing.T) {
	s, task := preparationFixture(t)
	op := ejh.Operation{ID: "legacy", Method: "open", Status: "unknown", RestockTaskID: task.ID, CreatedAt: time.Now()}
	if err := s.EJHBeginOperation(op); err != nil {
		t.Fatal(err)
	}
	other, err := Open(s.path[:len(s.path)-len("state.db")])
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.CheckEJHOpenOwner(task.Email, "") == nil {
		t.Fatal("legacy owner lost")
	}
	if err := other.EJHBeginOperation(ejh.Operation{ID: "other", Method: "open", Status: "submitting", AccountEmail: "other@example.com", CreatedAt: time.Now()}); err != nil {
		t.Fatal("other account blocked", err)
	}
	if err := other.EJHBeginOperation(ejh.Operation{ID: "duplicate", Method: "open", Status: "submitting", AccountEmail: "OTHER@example.com"}); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestEJHOpenIndexUpgradeAndLargeHistory(t *testing.T) {
	s := transferStore(t)
	op := ejh.Operation{ID: "old-open", Method: "open", Status: "unknown", AccountEmail: "old@example.com", CreatedAt: time.Now().Add(-72 * time.Hour)}
	if err := s.EJHPut("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
	// Rebuild the auxiliary index from existing encrypted records, as on upgrade.
	if _, err := s.db.Exec(`DELETE FROM ejh_open_index; DELETE FROM ejh_open_index_version`); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeEJHOpenIndex(); err != nil {
		t.Fatal(err)
	}
	if s.CheckEJHOpenOwner(op.AccountEmail, "") == nil {
		t.Fatal("upgrade lost unknown operation")
	}
	var sealed string
	if err := s.db.QueryRow(`SELECT sealed FROM ejh_records WHERE kind='operation' AND id=?`, op.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50000; i++ {
		id := fmt.Sprintf("archived-%d", i)
		if _, err = tx.Exec(`INSERT INTO ejh_records VALUES('operation',?,?,?)`, id, sealed, formatTime(op.CreatedAt)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO ejh_open_index VALUES(?,'completed','','',?)`, id, formatTime(op.CreatedAt)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	recent := op
	recent.ID, recent.AccountEmail, recent.Status, recent.CreatedAt = "recent", "next@example.com", "completed", time.Now()
	if err = s.EJHPut("operation", recent.ID, recent); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for i := 0; i < 12; i++ {
		ops, err := s.EJHRecentOpenOperations(time.Now().Add(-time.Hour))
		if err != nil || len(ops) != 1 || ops[0].ID != "recent" {
			t.Fatal(ops, err)
		}
		if err = s.CheckEJHOpenOwner("unused@example.com", ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("50,000 archived operations: 12 rounds of recent budget + owner query took %s", time.Since(started))
	if time.Since(started) > 2*time.Second {
		t.Fatal("recent queries regressed into historical scans")
	}
}
