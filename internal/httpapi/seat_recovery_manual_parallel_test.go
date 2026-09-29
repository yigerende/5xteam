package httpapi

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestSeatRecoveryManualDifferentMothersParallel(t *testing.T) {
	f := newRecoveryFixture(t)
	a := f.task(t, "one", "mother_invite", "mother_kick")
	b := f.task(t, "two", "mother_invite", "mother_kick")
	f.runUntil(t, a.ID, "dwell")
	f.runUntil(t, b.ID, "dwell")
	other, err := f.s.store.SaveAdminAccount(model.AdminAccountProfile{Label: "other", Email: "other@example.com", TeamAccountID: "team-two", ProxyID: f.admin.ProxyID}, "mother-at")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.store.UpdateSeatRecoveryTask(b.ID, func(p *model.SeatRecoveryTask) { p.AdminID = other.ID; p.TeamID = other.TeamAccountID })
	if err != nil {
		t.Fatal(err)
	}
	f.allowDirectRemove = true
	recoveryControl(t, f, a.ID, "mother_kick")
	recoveryControl(t, f, b.ID, "child_leave")
	entered := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.onMutation = func(r *http.Request) { entered <- r.Header.Get("chatgpt-account-id"); <-release }
	done := make(chan struct{}, 2)
	go func() { f.s.runSeatRecoveryStep(t.Context(), a.ID); done <- struct{}{} }()
	go func() { f.s.runSeatRecoveryStep(t.Context(), b.ID); done <- struct{}{} }()
	teams := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case team := <-entered:
			teams[team] = true
		case <-time.After(5 * time.Second):
			t.Fatal("different mothers blocked each other")
		}
	}
	if len(teams) != 2 {
		t.Fatal("lost mother identity")
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not finish")
		}
	}
}

func TestSeatRecoveryManualExitMissingTokenCanSwitchToKick(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "child_leave")
	f.runUntil(t, p.ID, "dwell")
	f.allowDirectRemove = true
	if err := f.s.store.SaveSeatRecoveryLogin(p.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	recoveryControl(t, f, p.ID, "child_leave")
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	if f.get(t, p.ID).Status != "failed" || len(f.mutations) != 2 {
		t.Fatal("missing AT exit mutated membership")
	}
	recoveryControl(t, f, p.ID, "mother_kick")
	p = f.runUntil(t, p.ID, "manual_done")
	if p.Settings.RemoveMethod != "child_leave" {
		t.Fatal("manual override changed saved configuration")
	}
}
