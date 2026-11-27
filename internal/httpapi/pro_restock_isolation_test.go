package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/restock"
)

type isolationTransport func(*http.Request) (*http.Response, error)

func (f isolationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRestockEJHUnknownAutomaticallyContinuesWithAnotherAccount(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
		return errors.New("fixture stops before payment")
	}}
	pro, _, _, _ := s.store.ProSettings()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c.CooldownSeconds, c.EmergencyCooldownSeconds = 0, 0
	c, err := s.store.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	pay, _, _ := s.store.GPTPaySettings()
	snap := restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(pro), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}
	tasks, err := s.store.ReserveProRestock(restock.Decision{ID: "first", At: time.Now(), Version: c.Version, Suggested: 1, Snapshot: snap}, "", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	base := s.ejh.client.HTTP.Transport
	var attempts atomic.Int32
	s.ejh.client.HTTP.Transport = isolationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v3/authenticate" && attempts.Add(1) == 1 {
			return nil, errors.New("fixture response lost after issuer accepted request")
		}
		return base.RoundTrip(r)
	})
	s.dispatchProRestock(context.Background(), c, pro, snap)
	first, err := s.store.ProRestockTask(tasks[0].ID)
	if err != nil || first.Active() || !first.AutoIsolated || first.StoppedAt == nil {
		t.Fatal("unknown opening still blocks batch", first, err)
	}
	if attempts.Load() != 1 || paid.Load() != 0 {
		t.Fatal("unexpected retry/payment")
	}
	// A fresh five-second monitor sample can admit another account. It must not
	// require ten minutes or an operator to resolve the original issuance.
	allocationAccount(t, s, "next-isolation@example.com")
	snap.At = time.Now().Add(5 * time.Second)
	if restock.WaitForBatch(snap, []restock.Task{first}) != nil {
		t.Fatal("old task blocks fresh sample")
	}
	next, err := s.store.ReserveProRestock(restock.Decision{ID: "second", At: snap.At, Version: c.Version, Suggested: 1, Snapshot: snap}, "", 0)
	if err != nil || len(next) != 1 || next[0].Email == email {
		t.Fatal("next account not reserved", next, err)
	}
	snap.At = time.Now()
	s.dispatchProRestock(context.Background(), c, pro, snap)
	waitProAutoFinished(t, s, next[0].Email)
	if issued.Load() != 1 || attempts.Load() != 2 || paid.Load() != 0 {
		t.Fatal("unrelated issuance blocked or duplicated", issued.Load(), attempts.Load(), paid.Load())
	}
	if s.store.CheckEJHOpenOwner(email, "") == nil {
		t.Fatal("unknown original was unlocked")
	}
	if r := payCall(t, s.startProAutomation, "email", email, map[string]any{"plan_code": "pro20"}); r.Code != 409 || attempts.Load() != 2 {
		t.Fatal("manual retry resubmitted unknown opening", r.Code)
	}
}

func TestRestockIsolationDoesNotStopRunningAccount(t *testing.T) {
	s, _, c, task, snap, _ := restockImportFixture(t)
	task.Status, task.CardProvider = "unknown", "ejh"
	s.store.SaveProRestockTask(task)
	task, _ = s.store.ProRestockTask(task.ID)
	s.proAutoMu.Lock()
	if s.proAutoJobs == nil {
		s.proAutoJobs = map[string]context.CancelFunc{}
	}
	s.proAutoJobs[task.Email] = func() {}
	s.proAutoMu.Unlock()
	if err := s.isolateRestockFailure(&task); err != nil || !task.Active() {
		t.Fatal("live worker isolated", err)
	}
	s.proAutoMu.Lock()
	delete(s.proAutoJobs, task.Email)
	s.proAutoMu.Unlock()
	if err := s.isolateRestockFailure(&task); err != nil || task.Active() {
		t.Fatal("idle unknown task retained", err)
	}
	p, _, _, _ := s.store.ProSettings()
	tasks, _, err := s.restockTasks(snap, p, c)
	if err != nil || len(tasks) != 1 || tasks[0].Active() {
		t.Fatal("monitor resurrected isolated task", err)
	}
}
