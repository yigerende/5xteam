package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func assertRecoveryDeleted(t *testing.T, f *recoveryFixture, p model.SeatRecoveryTask) {
	t.Helper()
	if _, err := f.s.store.SeatRecoveryTask(p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted task exists: %v", err)
	}
	logs, err := f.s.store.SeatRecoveryLogs(p.ID, 0)
	if err != nil || len(logs) != 0 {
		t.Fatalf("orphan logs after deletion: %d %v", len(logs), err)
	}
	if err = f.s.store.SeatRecoveryAvailable(p.Email); err != nil {
		t.Fatalf("deleted task still reserves email: %v", err)
	}
}

func waitRecoverySignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func TestSeatRecoveryForceDeleteAllStatesWithoutMother(t *testing.T) {
	for _, stage := range []string{"login", "join", "confirm", "dwell", "switch", "leave", "verify", "cooldown", "manual_remove", "manual_cooldown", "completed"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryFixture(t)
			p := f.task(t, "one", "child_request", "child_leave")
			mailBefore, credsBefore, _ := f.s.store.MailAccountCredential(p.Email)
			teamBefore, _, _ := f.s.store.FreeAccountCredential(p.FreeID)
			p, err := f.s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) {
				now := time.Now()
				v.AdminID = "missing-mother"
				v.Stage, v.Status, v.Message = stage, "failed", "母号配置不存在"
				v.Paused, v.Lane = true, true
				v.JoinSent, v.ConfirmSent, v.SwitchSent, v.LeaveSent = true, true, true, true
				v.JoinedAt = &now
				v.ManualRemoveSent, v.PendingAction = true, "child_leave"
				v.Finished = stage == "completed"
			})
			if err != nil {
				t.Fatal(err)
			}
			if !p.Finished {
				if ok, err := f.s.store.ClaimSeatRecoveryEntry(p); err != nil || !ok {
					t.Fatalf("entry claim: %v", err)
				}
			}
			f.s.recovery.active = map[string]bool{p.ID: true}
			recoveryControl(t, f, p.ID, "delete")
			assertRecoveryDeleted(t, f, p)
			// Simulate results already captured by a worker before deletion.
			if err := f.s.recoverySave(&p); err == nil {
				t.Fatal("late worker resurrected task")
			}
			f.s.recoveryLog(p, "late worker log")
			if err := f.s.store.SaveSeatRecoveryLogin(p.ID, "late-at", "late-session"); err == nil {
				t.Fatal("late credentials saved")
			}
			if err := f.s.saveRecoveryATCheck(p, model.AdminAccountTestResult{CheckedAt: time.Now(), Valid: true}); err == nil {
				t.Fatal("late AT check saved")
			}
			if ok, _ := f.s.store.ClaimSeatRecoveryEntry(p); ok {
				t.Fatal("late worker recreated entry lane")
			}
			f.s.runSeatRecoveryStep(t.Context(), p.ID)
			f.s.dispatchSeatRecovery(t.Context())
			f.s.recovery.wg.Wait()
			assertRecoveryDeleted(t, f, p)
			mailAfter, credsAfter, _ := f.s.store.MailAccountCredential(p.Email)
			teamAfter, _, _ := f.s.store.FreeAccountCredential(p.FreeID)
			if !reflect.DeepEqual(mailBefore, mailAfter) || !reflect.DeepEqual(credsBefore, credsAfter) || !reflect.DeepEqual(teamBefore, teamAfter) || len(f.mutations) != 0 {
				t.Fatal("force deletion changed mail, Team or remote membership")
			}
			// Both kinds of persistent lane must now be available to other tasks.
			other := f.task(t, "two", "mother_invite", "mother_kick")
			if _, err := f.s.store.UpdateSeatRecoveryTask(other.ID, func(v *model.SeatRecoveryTask) { v.Lane = true }); err != nil {
				t.Fatal(err)
			}
			if ok, err := f.s.store.ClaimSeatRecoveryEntry(other); err != nil || !ok {
				t.Fatalf("entry lane not released: %v", err)
			}
		})
	}
}

func TestSeatRecoveryForceDeleteDuringLoginDiscardsLateResult(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "mother_kick")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	f.s.recovery.login = func(email string, progress func(string), diagnostic func(protocolOAuthDiagnostic)) (map[string]any, error) {
		close(entered)
		<-release
		progress("late login success")
		return map[string]any{"success": true, "access_token": "late-at", "session_token": "late-session"}, nil
	}
	f.s.dispatchSeatRecovery(t.Context())
	waitRecoverySignal(t, entered, "login did not start")
	before, credsBefore, _ := f.s.store.MailAccountCredential(p.Email)
	// Deletion must return without waiting for the blocked login.
	recoveryControl(t, f, p.ID, "delete")
	assertRecoveryDeleted(t, f, p)
	f.s.recovery.mu.Lock()
	active := f.s.recovery.active[p.ID]
	f.s.recovery.mu.Unlock()
	if !active {
		t.Fatal("worker lock released before login unwound")
	}
	// Allow a new task for the same email; the old result must not affect it.
	replacement := p
	replacement.ID = recoveryID()
	if err := f.s.store.CreateSeatRecoveryTask(replacement); err != nil {
		t.Fatal(err)
	}
	go func() { f.s.recovery.wg.Wait(); close(finished) }()
	release <- struct{}{}
	waitRecoverySignal(t, finished, "deleted login worker did not finish")
	if _, err := f.s.store.SeatRecoveryTask(p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("login recreated deleted task")
	}
	logs, _ := f.s.store.SeatRecoveryLogs(p.ID, 0)
	after, credsAfter, _ := f.s.store.MailAccountCredential(p.Email)
	if len(logs) != 0 || len(f.mutations) != 0 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(credsBefore, credsAfter) {
		t.Fatal("late login produced writes or remote mutations")
	}
	if err := f.s.store.SaveSeatRecoveryLogin(p.ID, "old-worker-at", "old-session"); err == nil {
		t.Fatal("old ID overwrote replacement credentials")
	}
	if f.get(t, replacement.ID).Stage != "login" {
		t.Fatal("replacement task was advanced")
	}
}

func TestSeatRecoveryForceDeleteCancelsInFlightRequest(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "mother_kick")
	f.runUntil(t, p.ID, "join")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	f.onMutation = func(r *http.Request) { close(entered); <-release }
	f.s.dispatchSeatRecovery(t.Context())
	waitRecoverySignal(t, entered, "invite did not start")
	recoveryControl(t, f, p.ID, "delete")
	go func() { f.s.recovery.wg.Wait(); close(finished) }()
	// The remote handler remains blocked; cancellation must still free the worker.
	waitRecoverySignal(t, finished, "force deletion did not cancel the HTTP request")
	assertRecoveryDeleted(t, f, p)
	f.s.recovery.mu.Lock()
	active := f.s.recovery.active[p.ID] || f.s.recovery.taskCancels[p.ID] != nil || f.s.recovery.workerTeams[p.ID] != ""
	f.s.recovery.mu.Unlock()
	if active {
		t.Fatal("deleted worker runtime leaked")
	}
	f.s.dispatchSeatRecovery(t.Context())
	f.s.recovery.wg.Wait()
	assertRecoveryDeleted(t, f, p)
}

func TestSeatRecoveryCancelledLoginDoesNotStartOAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &Server{} // Accessing the store or starting a process would fail.
	if _, err := s.executeOpenAILoginContext(ctx, "child@example.com", "chatgpt_at", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("login ignored cancellation: %v", err)
	}
	if _, err := s.executeOpenAILoginWithProxy(ctx, "child@example.com", "", "", 0, "chatgpt_at", oauthLoginSelection{}, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("process ignored cancellation: %v", err)
	}
}
