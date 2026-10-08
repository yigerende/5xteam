package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func standardAccount(t *testing.T, f *recoveryFixture, name, method string, enabled bool) model.FreeAccountProfile {
	t.Helper()
	p, _, err := f.s.store.SaveImportedFreeAccount(model.FreeAccountProfile{Email: name + "@example.com", UserID: name}, "source-at")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.store.SaveMailAccount(model.MailAccountProfile{Email: p.Email}, model.MailAccountCredentials{Email: p.Email, AccessToken: "child-at-" + name})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
		p.AdminAccountID, p.TeamAccountID, p.SeatType = f.admin.ID, f.admin.TeamAccountID, "prolite"
		p.RemovalSeatPolicy = &model.RemovalSeatPolicy{Enabled: enabled, AdminIDs: []string{f.admin.ID}}
		p.RemoveMethod = method
		p.InviteStatus, p.AcceptStatus, p.RemoveStatus = "completed", "completed", "pending"
	})
	if err != nil {
		t.Fatal(err)
	}
	f.state[name] = "prolite"
	return p
}

func TestStandardRemovalMethodsAndSelectedMother(t *testing.T) {
	for _, mode := range []string{"automatic-kick", "automatic-leave", "manual", "dead", "401-exhausted", "quality", "disabled", "unselected", "legacy", "already-ordinary", "already-outside"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			p := standardAccount(t, f, "one", "child_leave", true)
			p, err := f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
				switch mode {
				case "automatic-kick":
					p.RemoveMethod = "mother_kick"
				case "dead":
					p.Dead = true
				case "401-exhausted":
					p.ReloginExhausted = true
				case "quality":
					p.Quality.Excluded = true
				case "disabled":
					p.RemovalSeatPolicy.Enabled = false
				case "unselected":
					p.RemovalSeatPolicy.AdminIDs = []string{"other"}
				case "legacy":
					p.RemovalSeatPolicy = nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "already-ordinary" {
				f.state["one"] = "default"
			}
			if mode == "already-outside" {
				f.state["one"] = "outside"
			}
			f.allowDirectRemove = mode == "disabled" || mode == "unselected" || mode == "legacy"
			got, err := f.s.performFreeAccountRemoval(t.Context(), p.ID, mode == "manual")
			if err != nil || got.RemoveStatus != "completed" {
				t.Fatalf("remove: %#v %v", got.StandardRemoval, err)
			}
			wantSwitch := !f.allowDirectRemove && mode != "already-ordinary" && mode != "already-outside"
			var switches, removes int
			for _, call := range f.mutations {
				if strings.Contains(call, "/seat/update") {
					switches++
				}
				if strings.Contains(call, ":DELETE:") {
					removes++
					mother := mode == "manual" || mode == "dead" || mode == "401-exhausted" || mode == "quality" || mode == "automatic-kick"
					if strings.HasPrefix(call, "mother:") != mother {
						t.Fatalf("wrong actor: %s", call)
					}
				}
			}
			if (switches == 1) != wantSwitch || switches > 1 {
				t.Fatalf("switch count %d", switches)
			}
			if mode != "already-outside" && removes != 1 {
				t.Fatalf("remove count %d", removes)
			}
			if got.StandardRemoval != nil && (got.StandardRemoval.Lane || got.StandardRemoval.Stage != "completed") {
				t.Fatal("lane not released")
			}
		})
	}
}

func TestStandardRemovalWaitingManualForceAndRestart(t *testing.T) {
	f := newRecoveryFixture(t)
	p := standardAccount(t, f, "one", "child_leave", true)
	f.noSpace = true
	out := callManualRemoval(f.s, p.ID)
	if out.Code != 202 || len(f.mutations) != 0 {
		t.Fatalf("no vacancy: %d %s", out.Code, out.Body.String())
	}
	got, _, _ := f.s.store.FreeAccountCredential(p.ID)
	if got.StandardRemoval.Method != "mother_kick" || got.StandardRemoval.Stage != "waiting_seat" {
		t.Fatal("manual override not persisted")
	}
	if err := f.s.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.s.store = st
	f.noSpace = false
	_, err = st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.StandardRemoval.NextAt = time.Now().Add(-time.Second).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	f.s.resumeStandardRemovals(t.Context())
	got, _, _ = st.FreeAccountCredential(p.ID)
	if got.RemoveStatus != "completed" || len(f.mutations) != 2 || !strings.HasPrefix(f.mutations[1], "mother:DELETE") {
		t.Fatalf("restart: %#v %v", got.StandardRemoval, f.mutations)
	}
}

func TestStandardRemoval401WaitingIsPendingNotFailureOrComplete(t *testing.T) {
	for _, mode := range []string{"no-relogin", "relogin-exhausted"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			p := standardAccount(t, f, "one", "child_leave", true)
			f.noSpace = true
			var got model.FreeAccountProfile
			if mode == "no-relogin" {
				var err error
				got, err = f.s.removeFreeAccountWithoutRelogin(t.Context(), p.ID, "sub2")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				limit := f.s.reloginFailureLimit("sub2")
				_, err := f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.ReloginFailureCount = limit - 1 })
				if err != nil {
					t.Fatal(err)
				}
				outcome, err := f.s.record401ReloginFailure(t.Context(), p.ID, "sub2", errors.New("oauth failed"))
				if err != nil || outcome.Removed || !outcome.RemovalPending {
					t.Fatalf("outcome: %+v %v", outcome, err)
				}
				got = outcome.Profile
			}
			if got.RemoveStatus != "running" || got.StandardRemoval == nil || got.StandardRemoval.Stage != "waiting_seat" || got.StandardRemoval.Method != "mother_kick" {
				t.Fatalf("wrong pending state: %+v", got.StandardRemoval)
			}
			if len(f.mutations) != 0 {
				t.Fatal("removed before ordinary seat was available")
			}
		})
	}
}

func TestStandardRemovalReconcilesAmbiguousAndPartialFailures(t *testing.T) {
	for _, mode := range []string{"switch-ambiguous", "leave-ambiguous", "switch-failed", "leave-failed", "restart-after-switch", "release-delayed"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryFixture(t)
			p := standardAccount(t, f, "one", "mother_kick", true)
			f.ambiguousSwitch = mode == "switch-ambiguous"
			f.ambiguousRemove = mode == "leave-ambiguous"
			f.failSwitch = mode == "switch-failed"
			f.failLeave = mode == "leave-failed"
			if mode == "restart-after-switch" {
				f.state["one"] = "default"
				_, err := f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
					p.StandardRemoval = &model.StandardRemoval{Stage: "switching", Lane: true, Method: "mother_kick", SwitchSent: true, FlowID: recoveryID(), MutationID: recoveryID()}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "release-delayed" {
				f.onMutation = func(r *http.Request) {
					if r.Method == "DELETE" {
						f.noSpace = true
					}
				}
			}
			got, err := f.s.performFreeAccountRemove(t.Context(), p.ID)
			if mode == "switch-failed" {
				if err == nil || got.RemoveStatus == "completed" || len(f.mutations) != 1 {
					t.Fatal("failed switch allowed kick")
				}
				return
			}
			if mode == "leave-failed" || mode == "release-delayed" {
				if err == nil || !got.StandardRemoval.Lane {
					t.Fatalf("lost pending reservation: %v", err)
				}
				f.failLeave = false
				f.noSpace = false
				f.onMutation = nil
				if err := f.s.store.Close(); err != nil {
					t.Fatal(err)
				}
				f.s.store, err = store.Open(f.dir)
				if err != nil {
					t.Fatal(err)
				}
				got, err = f.s.performFreeAccountRemove(t.Context(), p.ID)
			}
			if err != nil || got.RemoveStatus != "completed" {
				t.Fatalf("reconcile: %v %#v", err, got.StandardRemoval)
			}
			var switches, removes int
			for _, call := range f.mutations {
				if strings.Contains(call, "/seat/update") {
					switches++
				}
				if strings.Contains(call, ":DELETE:") {
					removes++
				}
			}
			wantSwitch := 1
			if mode == "restart-after-switch" {
				wantSwitch = 0
			}
			wantRemove := 1
			if mode == "leave-failed" {
				wantRemove = 2
			}
			if switches != wantSwitch || removes != wantRemove {
				t.Fatalf("duplicated mutation %v", f.mutations)
			}
		})
	}
}

func TestStandardRemovalSameMotherSerialDifferentMothersParallel(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprint(different), func(t *testing.T) {
			f := newRecoveryFixture(t)
			a := standardAccount(t, f, "one", "mother_kick", true)
			b := standardAccount(t, f, "two", "mother_kick", true)
			if different {
				other, err := f.s.store.SaveAdminAccount(model.AdminAccountProfile{Email: "other@example.com", TeamAccountID: "team-two", ProxyID: f.admin.ProxyID}, "mother-at")
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.s.store.UpdateFreeAccount(b.ID, func(p *model.FreeAccountProfile) {
					p.AdminAccountID = other.ID
					p.TeamAccountID = other.TeamAccountID
					p.RemovalSeatPolicy.AdminIDs = []string{other.ID}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			var second atomic.Bool
			f.onMutation = func(r *http.Request) {
				if strings.Contains(r.URL.Path, "/users/one/seat/update") {
					once.Do(func() { close(started) })
					<-release
				}
				if strings.Contains(r.URL.Path, "/users/two/seat/update") {
					second.Store(true)
				}
			}
			done := make(chan error, 2)
			go func() { _, err := f.s.performFreeAccountRemove(context.Background(), a.ID); done <- err }()
			<-started
			go func() { _, err := f.s.performFreeAccountRemove(context.Background(), b.ID); done <- err }()
			if different {
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("different mother blocked")
				}
			} else {
				select {
				case <-done:
					t.Error("same mother bypassed serial lane")
				case <-time.After(100 * time.Millisecond):
				}
			}
			if second.Load() != different {
				t.Error("wrong per-mother concurrency")
			}
			close(release)
			count := 2
			if different {
				count = 1
			}
			for i := 0; i < count; i++ {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestStandardRemovalSharesRecoveryLaneBothDirections(t *testing.T) {
	for _, teamFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(teamFirst), func(t *testing.T) {
			f := newRecoveryFixture(t)
			task := f.task(t, "recovery", "mother_invite", "mother_kick")
			p := standardAccount(t, f, "teamchild", "mother_kick", true)
			if teamFirst {
				f.failLeave = true
				_, err := f.s.performFreeAccountRemove(t.Context(), p.ID)
				if err == nil {
					t.Fatal("expected removal failure")
				}
				_, err = f.s.store.UpdateSeatRecoveryTask(task.ID, func(p *model.SeatRecoveryTask) { p.Lane = true })
				if !errors.Is(err, store.ErrStandardSeatBusy) {
					t.Fatalf("recovery bypassed team lane: %v", err)
				}
			} else {
				_, err := f.s.store.UpdateSeatRecoveryTask(task.ID, func(p *model.SeatRecoveryTask) { p.Lane = true })
				if err != nil {
					t.Fatal(err)
				}
				got, err := f.s.performFreeAccountRemove(t.Context(), p.ID)
				if !errors.Is(err, errStandardRemovalPending) || got.StandardRemoval.Lane || len(f.mutations) != 0 {
					t.Fatalf("team bypassed recovery: %v", err)
				}
			}
		})
	}
}

func TestStandardRemovalRetryLimitAndCooldown(t *testing.T) {
	f := newRecoveryFixture(t)
	p := standardAccount(t, f, "one", "mother_kick", true)
	settings := f.s.store.AutoRotationSettings()
	settings.RetryCount = 0
	f.s.store.SaveAutoRotationSettings(settings)
	f.failLeave = true
	got, err := f.s.performFreeAccountRemove(t.Context(), p.ID)
	if err == nil || got.StandardRemoval.Stage != "failed" {
		t.Fatal("retry limit ignored")
	}
	_, err = f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.StandardRemoval.NextAt = time.Now().Add(-time.Second).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	f.s.resumeStandardRemovals(t.Context())
	if len(f.mutations) != 2 {
		t.Fatal("failed task retried automatically")
	}
	f.failLeave = false
	_, err = f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.StandardRemoval.MutationAfter = time.Now().Add(time.Minute).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.s.performFreeAccountRemove(t.Context(), p.ID)
	if !errors.Is(err, errStandardRemovalPending) || got.StandardRemoval.Stage != "cooldown" || len(f.mutations) != 2 {
		t.Fatal("persisted cooldown ignored")
	}
	_, _ = f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.StandardRemoval.MutationAfter = time.Time{} })
	got, err = f.s.performFreeAccountRemove(t.Context(), p.ID)
	if err != nil || got.RemoveStatus != "completed" || len(f.mutations) != 3 {
		t.Fatalf("manual retry: %v", err)
	}
}
