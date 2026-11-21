package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func productionRestockConfig(t testing.TB) restock.Config {
	t.Helper()
	raw, err := os.ReadFile("../restock/testdata/production_20261121.json")
	if err != nil {
		t.Fatal(err)
	}
	var c restock.Config
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c.Version = restock.DefaultConfig().Version // Isolated database CAS version, same business settings.
	return c
}

func TestProRestockProduction64ConcurrentReservations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c, err := s.SaveProRestockConfig(productionRestockConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		email := fmt.Sprintf("parallel-%d@example.invalid", i)
		if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	snap := restock.Snapshot{At: now, CollectionStartedAt: now, Binding: "parallel", GroupID: c.GroupID, Valid: true, LoadValid: true, Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 60, Used: 60}, {ID: 2, Ready: true, Capacity: 60, Used: 60, Waiting: 20}}}
	snap = restock.ObserveAdmission(c, snap, restock.Snapshot{}, nil, 60, 5, now)
	d := restock.Evaluate(c, snap, nil, restock.Limits{}, 0, 60, 5, now)
	if d.Suggested != 2 {
		t.Fatal("production high load must reserve two", d)
	}
	var wg sync.WaitGroup
	var won, quantity atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			candidate := d
			candidate.ID = fmt.Sprintf("race-%d", i)
			db := s
			if i%2 != 0 {
				db = other
			}
			tasks, e := db.ReserveProRestock(candidate, "", 0)
			if e == nil {
				won.Add(1)
				quantity.Add(int32(len(tasks)))
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if won.Load() != 1 || quantity.Load() != 2 {
		t.Fatalf("winning batches=%d tasks=%d", won.Load(), quantity.Load())
	}
	tasks, err := s.ProRestockActiveTasks()
	if err != nil || len(tasks) != 2 || tasks[0].Email == tasks[1].Email {
		t.Fatal("duplicate/missing account claim", tasks, err)
	}
	// A completed member doesn't unlock the unfinished member's batch.
	tasks[0].Status = "ready"
	done := time.Now()
	tasks[0].ReadyAt = &done
	tasks[0].UpdatedAt = done
	if err = s.SaveProRestockTask(tasks[0]); err != nil {
		t.Fatal(err)
	}
	d.ID = "partial-completion"
	if added, e := s.ReserveProRestock(d, "", 0); e == nil || len(added) != 0 {
		t.Fatal("partial batch admitted new work")
	}
	// End the other member in failure, retaining the refresh barrier.
	tasks[1].Status = "failed"
	tasks[1].UpdatedAt = time.Now()
	if err = s.SaveProRestockTask(tasks[1]); err != nil {
		t.Fatal(err)
	}
	d.ID = "old-snapshot"
	if added, e := s.ReserveProRestock(d, "", 0); e == nil || len(added) != 0 {
		t.Fatal("stale decision bypassed finish barrier")
	}
}

func TestProRestockRelevantHistoryDoesNotReadArchivedPayloads(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.SaveProRestockConfig(productionRestockConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		old := now.Add(-72 * time.Hour)
		task := restock.Task{ID: fmt.Sprintf("history-%d", i), Email: fmt.Sprintf("old-%d@example.invalid", i), Status: "ready", CreatedAt: old, UpdatedAt: old, ReadyAt: &old, AccountID: int64(i + 100), Binding: "parallel", GroupID: 6}
		if _, err = stmt.Exec(task.ID, task.Email, task.Status, formatTime(old), 0, mustJSON(task)); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	active := restock.Task{ID: "live", Email: "live@example.invalid", Status: "running", CreatedAt: now, UpdatedAt: now, AccountID: 1, Binding: "parallel", GroupID: 6}
	if _, err = s.db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, active.ID, active.Email, active.Status, formatTime(active.CreatedAt), 0, mustJSON(active)); err != nil {
		t.Fatal(err)
	}
	snap := restock.Snapshot{At: now, Binding: "parallel", GroupID: 6, Valid: true, LoadValid: true, Accounts: []restock.Account{{ID: 100, Ready: true}}}
	tx, err = s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := time.Now()
	tasks, err := replacementTasks(tx, snap)
	elapsed := time.Since(start)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("read irrelevant history: count=%d error=%v", len(tasks), err)
	}
	t.Logf("10,000 archived tasks: relevant query returned %d in %s, production config v%d", len(tasks), elapsed, c.Version)
	rows, err := tx.Query(`EXPLAIN QUERY PLAN SELECT id FROM pro_restock_tasks WHERE status='ready' AND json_extract(payload,'$.account_id') IN (SELECT value FROM json_each(?)) AND json_extract(payload,'$.binding')=? AND json_extract(payload,'$.group_id')=?`, "[100]", snap.Binding, snap.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "pro_restock_ready_scope_account") || !strings.Contains(plan, "<expr>=? AND <expr>=? AND <expr>=?") {
		t.Fatalf("ready lookup may scan archived target: %s", plan)
	}
}

func TestProRestockFinishBarrierSubMillisecondAndTimezones(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := productionRestockConfig(t)
	c.CooldownSeconds = 0
	c, err = s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	email := "fresh@example.invalid"
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond).Add(400 * time.Microsecond)
	finished := now.Add(100 * time.Microsecond).In(time.FixedZone("CST", 28800))
	task := restock.Task{ID: "finished", Email: "finished@example.invalid", Status: "ready", CreatedAt: now.Add(-time.Hour), UpdatedAt: finished, ReadyAt: &finished, Binding: "scope", GroupID: 6}
	if _, err = s.db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, task.ID, task.Email, task.Status, formatTime(task.CreatedAt), 0, mustJSON(task)); err != nil {
		t.Fatal(err)
	}
	d := restock.Decision{ID: "boundary", Version: c.Version, At: time.Now(), Suggested: 1, Snapshot: restock.Snapshot{At: now, CollectionStartedAt: now, Binding: "scope", GroupID: 6, Valid: true, LoadValid: true}}
	if _, err = s.ReserveProRestock(d, "", 0); err == nil {
		t.Fatal("SQLite millisecond rounding bypassed exact barrier")
	}
	d.Snapshot.At = finished.Add(time.Nanosecond)
	d.Snapshot.CollectionStartedAt = d.Snapshot.At
	if added, e := s.ReserveProRestock(d, "", 0); e != nil || len(added) != 1 {
		t.Fatal("exact fresh boundary blocked", added, e)
	}
}
