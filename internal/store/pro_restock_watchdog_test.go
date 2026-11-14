package store

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func watchdogFixture(t *testing.T) (*Store, restock.Task, time.Time) {
	t.Helper()
	s := transferStore(t)
	email := "watchdog@example.com"
	transferSource(t, s, email)
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err := s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tasks, err := s.ReserveProRestock(restock.Decision{ID: "watchdog", At: now, Version: c.Version, Suggested: 1}, "target", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatal(err)
	}
	task := tasks[0]
	task.Status = "running"
	task.SubmittedAt = &now
	task.OrderID = "original-order"
	task.CardID = "original-card"
	s.SaveProRestockTask(task)
	_, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "flow", Status: "running", Stage: "login", OrderID: task.OrderID, CardID: task.CardID, StartedAt: now, Steps: map[string]string{"login": "running"}, StageTimes: map[string]time.Time{"login:running": now}}
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.encrypt(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO gptpay_orders(id,email,profile,encrypted_snapshot,active,created_at) VALUES(?,?,'{"status":"submission_unknown"}',?,1,?)`, task.OrderID, email, sealed, formatTime(now))
	if err != nil {
		t.Fatal(err)
	}
	return s, task, now
}

func TestRestockWatchdogHeartbeatRestartAndLateWriter(t *testing.T) {
	s, task, now := watchdogFixture(t)
	before, _ := s.ProRestockLimits(now)
	var originalSnapshot string
	s.db.QueryRow(`SELECT encrypted_snapshot FROM gptpay_orders WHERE id=?`, task.OrderID).Scan(&originalSnapshot)
	for _, minute := range []int{0, 4, 9} {
		s.UpdateProAccount(task.Email, func(p *model.MailAccountProfile) {
			p.ProAuto.Error = "retry"
			p.ProAuto.UpdatedAt = now.Add(time.Duration(minute) * time.Minute)
		})
		task.UpdatedAt = now.Add(time.Duration(minute) * time.Minute)
		s.SaveProRestockTask(task)
		out, err := s.SweepProRestockWatchdog(task.UpdatedAt)
		if err != nil || len(out) != 0 {
			t.Fatal(out, err)
		}
	}
	// Persisted progress survives creating a new store process.
	other, err := Open(s.path[:len(s.path)-len("state.db")])
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	out, err := other.SweepProRestockWatchdog(now.Add(10 * time.Minute))
	if err != nil || len(out) != 1 || out[0].CancelEmail != task.Email {
		t.Fatal(out, err)
	}
	p, _, _ := s.MailAccountCredential(task.Email)
	if !p.ProAuto.StopRequested || p.ProAuto.StopVersion != 1 {
		t.Fatal("worker not fenced")
	}
	s.SaveProRestockTask(task)
	got, _ := s.ProRestockTask(task.ID)
	if got.Active() || got.StoppedAt == nil || got.OrderID != task.OrderID || got.CardID != task.CardID {
		t.Fatal("late result revived intent", got)
	}
	var profile, snapshot string
	var active int
	s.db.QueryRow(`SELECT profile,encrypted_snapshot,active FROM gptpay_orders WHERE id=?`, task.OrderID).Scan(&profile, &snapshot, &active)
	if profile != `{"status":"submission_unknown"}` || snapshot != originalSnapshot || active != 1 {
		t.Fatal("money state changed")
	}
	after, _ := s.ProRestockLimits(now.Add(10 * time.Minute))
	if before.HourCost != after.HourCost || before.HourCount != after.HourCount {
		t.Fatal("budget disappeared")
	}
}

func TestRestockWatchdogRealProgressAndConcurrentStop(t *testing.T) {
	s, task, now := watchdogFixture(t)
	s.SweepProRestockWatchdog(now)
	s.UpdateProAccount(task.Email, func(p *model.MailAccountProfile) {
		p.ProAuto.Steps["login"] = "completed"
		p.ProAuto.StageTimes["login:completed"] = now.Add(8 * time.Minute)
		p.ProAuto.Stage = "recharge"
	})
	out, err := s.SweepProRestockWatchdog(now.Add(10 * time.Minute))
	if err != nil || len(out) != 0 {
		t.Fatal("valid progress killed", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := s.SweepProRestockWatchdog(now.Add(21 * time.Minute))
			if e != nil {
				t.Error(e)
			}
			mu.Lock()
			n += len(out)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if n != 1 {
		t.Fatal("non-atomic expiration", n)
	}
}

func TestRestockWatchdogFinalStepImmediatelyReleasesWithoutDelivery(t *testing.T) {
	for _, stage := range []string{"merchant", "merge", "merge_v2", "deactivate"} {
		t.Run(stage, func(t *testing.T) {
			s, task, now := watchdogFixture(t)
			s.UpdateProAccount(task.Email, func(p *model.MailAccountProfile) {
				p.ProAuto.Stage = stage
				p.ProAuto.Status = "completed"
				p.ProAuto.Steps[stage] = "completed"
				p.ProAuto.Error = "group adjustment failed"
			})
			var before string
			s.db.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, task.Email).Scan(&before)
			out, err := s.SweepProRestockWatchdog(now.Add(time.Second))
			if err != nil || len(out) != 1 || out[0].CancelEmail != "" || out[0].Task.StoppedFailure || out[0].Task.ReadyAt != nil {
				t.Fatal(out, err)
			}
			var after string
			s.db.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, task.Email).Scan(&after)
			if before != after {
				t.Fatal("completed account/cleanup altered")
			}
			if got, _ := s.ProRestockTask(task.ID); got.Active() {
				t.Fatal("still inflight")
			}
		})
	}
}

func TestRestockWatchdogDoesNotStopNewManualFlow(t *testing.T) {
	s, task, now := watchdogFixture(t)
	s.SweepProRestockWatchdog(now)
	s.UpdateProAccount(task.Email, func(p *model.MailAccountProfile) { p.ProRestockTaskID = "new-task"; p.ProAuto.ID = "new-manual" })
	out, err := s.SweepProRestockWatchdog(now.Add(11 * time.Minute))
	if err != nil || len(out) != 1 || out[0].CancelEmail != "" {
		t.Fatal(out, err)
	}
	p, _, _ := s.MailAccountCredential(task.Email)
	if p.ProAuto.StopRequested {
		t.Fatal("old task stopped new flow")
	}
}

func TestRestockWatchdogObservationCannotRevive(t *testing.T) {
	s, task, now := watchdogFixture(t)
	tasks, _ := s.ProRestockTasks()
	observed := tasks[0]
	s.SweepProRestockWatchdog(now.Add(11 * time.Minute))
	observed.UpdatedAt = now.Add(12 * time.Minute)
	s.SaveProRestockObservation(&observed)
	if observed.Active() {
		t.Fatal("stale monitor revived timeout")
	}
	var payload string
	s.db.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, task.ID).Scan(&payload)
	var got restock.Task
	json.Unmarshal([]byte(payload), &got)
	if got.Active() {
		t.Fatal("durable stop lost")
	}
}
