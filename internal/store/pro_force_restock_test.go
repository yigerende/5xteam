package store

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"fmt"
	"sync"
	"testing"
	"time"
)

func forceRestockFixture(t *testing.T, s *Store) (restock.Config, restock.Snapshot) {
	t.Helper()
	c := restock.DefaultConfig()
	c.Enabled = true
	c.Mode = "auto"
	c.GroupID = 1
	c.ForceMergeEnabled = true
	c.TimeEnabled = false
	c.ConcurrencyEnabled = false
	c, err := s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		email := fmt.Sprintf("fresh-%d@example.com", i)
		if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
			t.Fatal(err)
		}
	}
	enabled := true
	now := time.Now()
	deadline := now.Add(5 * time.Minute)
	snap := restock.Snapshot{At: now, Valid: true, LoadValid: true, GroupID: 1, Binding: "pool", NewPlan: "pro", Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 20, EJH: true, PushTimeoutEnabled: &enabled, ForceMergeDeadline: &deadline, ForceMergeKey: "original-cycle"}}}
	return c, snap
}

func TestForceRestockAtomicRestartReadyAndRelease(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	c, snap := forceRestockFixture(t, s)
	d := restock.Evaluate(c, snap, nil, restock.Limits{}, 0, 20, 5, snap.At)
	d.ID = "replacement"
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dd := d
			dd.ID = fmt.Sprint("concurrent-", i)
			tasks, e := s.ReserveProRestock(dd, "", 0)
			if e == nil {
				mu.Lock()
				count += len(tasks)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("duplicate source claims: %d", count)
	}
	tasks, err := s.ProRestockTasks()
	if err != nil || len(tasks) != 1 || tasks[0].ForceMergeSource != "original-cycle" {
		t.Fatal(tasks, err)
	}
	task := tasks[0]
	task.Status = "unknown"
	if err = s.SaveProRestockTask(task); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err = s.ProRestockTasks()
	if err != nil {
		t.Fatal(err)
	}
	if stats := restock.ForceMergeSummary(c, snap, tasks, time.Now()); len(stats.Sources) != 0 || stats.Pending != 1 {
		t.Fatal("restart lost unknown coverage", stats)
	}
	task = tasks[0]
	task.Status = "ready"
	task.AccountID = 2
	ready := time.Now()
	task.ReadyAt = &ready
	task.CreatedAt = ready.Add(-72 * time.Hour)
	if err = s.SaveProRestockTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE pro_restock_tasks SET created_at=? WHERE id=?", formatTime(task.CreatedAt), task.ID); err != nil {
		t.Fatal(err)
	}
	snap.Accounts = append(snap.Accounts, restock.Account{ID: 2, Ready: true, Capacity: 20})
	tasks, err = s.ProRestockTasks()
	if err != nil || len(tasks) != 1 {
		t.Fatal("ready coverage older than 48h lost", tasks, err)
	}
	if stats := restock.ForceMergeSummary(c, snap, tasks, time.Now()); len(stats.Sources) != 0 || stats.Ready != 1 {
		t.Fatal(stats)
	}
	task.Status = "failed"
	if err = s.SaveProRestockTask(task); err != nil {
		t.Fatal(err)
	}
	tasks, _ = s.ProRestockTasks()
	tasks, err = s.ReconcileForceMerge(c, snap, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if stats := restock.ForceMergeSummary(c, snap, tasks, time.Now()); len(stats.Sources) != 1 {
		t.Fatal("confirmed failure did not release", stats)
	}
}

func TestForceRestockAdoptsExistingAndRetainsCommitment(t *testing.T) {
	s := transferStore(t)
	c, snap := forceRestockFixture(t, s)
	d := restock.Decision{ID: "ordinary", At: snap.At, Version: c.Version, Suggested: 1, Snapshot: snap}
	d.Snapshot.Accounts = nil
	tasks, err := s.ReserveProRestock(d, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err = s.ReconcileForceMerge(c, snap, tasks)
	if err != nil || tasks[0].ForceMergeSource == "" {
		t.Fatal("existing work not attached", tasks, err)
	}
	later := time.Now().Add(time.Hour)
	snap.Accounts[0].ForceMergeDeadline = &later
	tasks, err = s.ReconcileForceMerge(c, snap, tasks)
	if err != nil || tasks[0].ForceMergeSource == "" {
		t.Fatal("average recovery erased commitment", tasks, err)
	}
	snap.Accounts = nil
	tasks, err = s.ReconcileForceMerge(c, snap, tasks)
	if err != nil || tasks[0].ForceMergeSource == "" {
		t.Fatal("source departed before replacement; reservation must continue", tasks, err)
	}
	if err = s.CancelProRestock(tasks[0], "pause"); err != nil {
		t.Fatal(err)
	}
	tasks, _ = s.ProRestockTasks()
	tasks, err = s.ReconcileForceMerge(c, snap, tasks)
	if err != nil || tasks[0].ForceMergeSource != "" {
		t.Fatal("cancelled coverage retained", tasks, err)
	}
}

func TestForceRestockAdoptsExternalWithoutNewPurchaseBudget(t *testing.T) {
	s := transferStore(t)
	c, snap := forceRestockFixture(t, s)
	tasks := []restock.Task{{ID: "old-auto", Email: "old@example.com", Status: "unknown", Binding: snap.Binding, GroupID: 1, Plan: "pro", CreatedAt: time.Now()}}
	tasks, err := s.ReconcileForceMerge(c, snap, tasks)
	if err != nil || !tasks[0].ManagedExternally || tasks[0].ForceMergeSource == "" {
		t.Fatal(tasks, err)
	}
	limits, err := s.ProRestockLimits(time.Now())
	if err != nil || limits.HourCount != 0 || limits.DayCost != 0 {
		t.Fatal("external purchase charged twice", limits, err)
	}
	persisted, err := s.ProRestockTasks()
	if err != nil || len(persisted) != 1 || persisted[0].ForceMergeSource == "" {
		t.Fatal("external coverage not durable", persisted, err)
	}
}

func TestProOrderTimeoutCapturedBeforePushAndPreserved(t *testing.T) {
	s := transferStore(t)
	email := "policy@example.com"
	if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultProSettings()
	cfg.ForceMergeEnabled = false
	cfg.ForceMergeMinutes = 60
	if _, err := s.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	order := gptpay.Order{ID: "policy", Email: email, Status: "success", CreatedAt: time.Now()}
	if err := s.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.MailAccountCredential(email)
	if p.ProPushTimeoutEnabled == nil || *p.ProPushTimeoutEnabled || p.ProPushStartedAt == nil || !p.ProPushStartedAt.Equal(order.CreatedAt) {
		t.Fatal("manual order did not snapshot disabled policy")
	}
	cfg.ForceMergeEnabled = true
	if _, err := s.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "failed" })
	if err != nil || p.ProPushStartedAt == nil || !p.ProPushStartedAt.Equal(order.CreatedAt) {
		t.Fatal("failed push changed activation clock", err)
	}
	p, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
	if err != nil || p.ProPushStartedAt == nil || p.ProPushDeadline() == nil {
		t.Fatal("in-flight account did not adopt changed policy", err)
	}
	first := *p.ProPushStartedAt
	s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "running" })
	p, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
	if err != nil || !p.ProPushStartedAt.Equal(first) {
		t.Fatal("repush reset timer", err)
	}
	p, err = s.SaveMailAccountToPro(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email})
	if err != nil || p.ProPushTimeoutEnabled == nil || !*p.ProPushTimeoutEnabled || !p.ProPushStartedAt.Equal(first) {
		t.Fatal("credential import lost policy", err)
	}
}

func TestProPushTimeoutTransferPreservesPolicy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		src, dst := transferStore(t), transferStore(t)
		email := fmt.Sprintf("policy-%v@example.com", enabled)
		transferSource(t, src, email)
		first := time.Now().UTC().Add(-time.Minute)
		ref := model.ProDownstreamRef{Provider: "sub2", URL: "https://fixture.invalid", LoginEmail: "fixture@example.com"}
		_, err := src.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProPushTimeoutEnabled = &enabled
			p.ProPushTimeoutMinutes = 60
			p.ProPushStartedAt = &first
			p.ProPushDownstream = &ref
		})
		if err != nil {
			t.Fatal(err)
		}
		file, err := src.ExportProAccounts([]string{email}, ref)
		if err != nil {
			t.Fatal(err)
		}
		file = transferWire(t, file)
		if _, err = dst.ImportProAccount(file.Accounts[0], file.ExportedAt, false, ref); err != nil {
			t.Fatal(err)
		}
		p, _, err := dst.MailAccountCredential(email)
		if err != nil || p.ProPushTimeoutEnabled == nil || *p.ProPushTimeoutEnabled != enabled || !p.ProPushStartedAt.Equal(first) || p.ProPushTimeoutMinutes != 60 || p.ProPushDownstream == nil || !p.ProPushDownstream.Matches(ref) {
			t.Fatal("migration lost opening policy", err)
		}
	}
}
