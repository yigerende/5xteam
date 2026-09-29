package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func recoveryControl(t *testing.T, f *recoveryFixture, id, action string) model.SeatRecoveryTask {
	t.Helper()
	p, code, err := f.s.applySeatRecoveryControl(id, action)
	if err != nil {
		t.Fatalf("%s: %d %v", action, code, err)
	}
	return p
}

func TestSeatRecoveryManualRemovalSkipsDwellSwitchAndPreservesTeam(t *testing.T) {
	for _, method := range []string{"mother_kick", "child_leave"} {
		for _, ambiguous := range []bool{false, true} {
			t.Run(fmt.Sprint(method, ambiguous), func(t *testing.T) {
				f := newRecoveryFixture(t)
				p := f.task(t, "one", "child_request", "mother_kick")
				f.runUntil(t, p.ID, "dwell")
				before, _, _ := f.s.store.FreeAccountCredential(p.FreeID)
				f.allowDirectRemove, f.holdAgain, f.ambiguousRemove = true, true, ambiguous
				_, err := f.s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) {
					due := time.Now().Add(time.Hour)
					v.DueAt = &due
					v.NextAt = due
					v.Paused = true
				})
				if err != nil {
					t.Fatal(err)
				}
				recoveryControl(t, f, p.ID, method)
				if _, _, err = f.s.applySeatRecoveryControl(p.ID, method); err == nil {
					t.Fatal("duplicate manual removal queued")
				}
				p = f.runUntil(t, p.ID, "manual_done")
				if !p.Finished || p.Lane || !strings.Contains(p.Message, "仍有 5x 临停") {
					t.Fatalf("incorrect completion: %+v", p)
				}
				wantSource := "mother:DELETE:"
				if method == "child_leave" {
					wantSource = "child:DELETE:"
				}
				if len(f.mutations) != 3 || !strings.HasPrefix(f.mutations[2], wantSource) {
					t.Fatalf("wrong actor or extra mutation: %v", f.mutations)
				}
				after, _, _ := f.s.store.FreeAccountCredential(p.FreeID)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("manual recovery removal modified Team")
				}
			})
		}
	}
}

func TestSeatRecoveryManualQueuedDuringWorkerSurvivesRestart(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "mother_kick")
	p = f.runUntil(t, p.ID, "join")
	f.s.recovery.active = map[string]bool{p.ID: true}
	recoveryControl(t, f, p.ID, "mother_kick")
	// An old in-flight worker saving its outcome must retain the queued command.
	p.Stage, p.Status, p.Message = "confirm", "queued", "old worker result"
	if err := f.s.recoverySave(&p); err != nil {
		t.Fatal(err)
	}
	if p.PendingAction != "mother_kick" {
		t.Fatal("worker overwrote pending action")
	}
	if _, _, err := f.s.applySeatRecoveryControl(p.ID, "delete"); err == nil {
		t.Fatal("deleted active worker")
	}
	f.s.recovery.active = map[string]bool{}
	if err := f.s.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.s = &Server{store: st}
	f.setLogin()
	recoveryControl(t, f, p.ID, "pause")
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	if f.get(t, p.ID).PendingAction == "" {
		t.Fatal("paused manual command executed")
	}
	recoveryControl(t, f, p.ID, "resume")
	f.runUntil(t, p.ID, "manual_done")
	if len(f.mutations) != 0 {
		t.Fatal("absent member was invited or removed")
	}
}

func TestSeatRecoveryManualFailureRetainsOrdinaryLane(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "child_leave")
	f.runUntil(t, p.ID, "leave")
	f.failLeave = true
	recoveryControl(t, f, p.ID, "mother_kick")
	for i := 0; i < 14; i++ {
		f.s.runSeatRecoveryStep(t.Context(), p.ID)
	}
	p = f.get(t, p.ID)
	if p.Status != "failed" || !p.Lane || p.Finished {
		t.Fatalf("lost lane on failure: %+v", p)
	}
	if _, _, err := f.s.applySeatRecoveryControl(p.ID, "delete"); err == nil {
		t.Fatal("deleted occupied ordinary seat")
	}
	f.failLeave = false
	f.retry(t, p.ID)
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	f.noSpace = true
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	p = f.get(t, p.ID)
	if !p.Lane || p.Finished || p.Stage != "manual_remove" {
		t.Fatal("released before ordinary seat available")
	}
	f.noSpace = false
	f.runUntil(t, p.ID, "manual_done")
	logs, _ := f.s.store.SeatRecoveryLogs(p.ID, 0)
	if len(logs) == 0 {
		t.Fatal("missing manual logs")
	}
	recoveryControl(t, f, p.ID, "delete")
	if _, err := f.s.store.SeatRecoveryTask(p.ID); err == nil {
		t.Fatal("task not deleted")
	}
	logs, _ = f.s.store.SeatRecoveryLogs(p.ID, 0)
	if len(logs) != 0 {
		t.Fatal("task logs not deleted")
	}
	if _, _, err := f.s.store.MailAccountCredential(p.Email); err != nil {
		t.Fatal("deleted mailbox")
	}
}

func TestSeatRecoveryBatchControlsReportPartialResults(t *testing.T) {
	f := newRecoveryFixture(t)
	a := f.task(t, "one", "mother_invite", "mother_kick")
	b := f.task(t, "two", "mother_invite", "mother_kick")
	call := func(action string, ids []string) []struct {
		ID    string `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	} { body, _ := json.Marshal(recoveryBatchInput{IDs: ids, Action: action}); w := httptest.NewRecorder(); f.s.batchControlSeatRecovery(w, httptest.NewRequest("POST", "/", strings.NewReader(string(body)))); var out struct {
		Data struct {
			Results []struct {
				ID    string `json:"id"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"results"`
		} `json:"data"`
	}; if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Body.String())
	}; return out.Data.Results }
	results := call("pause", []string{a.ID, b.ID, a.ID, "missing"})
	if len(results) != 3 || !results[0].OK || !results[1].OK || results[2].OK {
		t.Fatalf("bad per-task result: %+v", results)
	}
	if !f.get(t, a.ID).Paused || !f.get(t, b.ID).Paused {
		t.Fatal("batch pause failed")
	}
	call("resume", []string{a.ID, b.ID})
	if f.get(t, a.ID).Paused || f.get(t, b.ID).Paused {
		t.Fatal("batch resume failed")
	}
	f.runUntil(t, b.ID, "dwell")
	results = call("delete", []string{a.ID, b.ID})
	if !results[0].OK || results[1].OK {
		t.Fatalf("unsafe batch delete: %+v", results)
	}
	before := len(f.mutations)
	call("child_leave", []string{b.ID})
	if len(f.mutations) != before || f.get(t, b.ID).PendingAction != "child_leave" {
		t.Fatal("batch control blocked on remote action")
	}
	w := httptest.NewRecorder()
	f.s.statusSeatRecoveryTasks(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"ids":["`+b.ID+`"]}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "child_leave") {
		t.Fatal("status progress missing")
	}
}

func TestSeatRecoveryManualRemovalSharesMotherLock(t *testing.T) {
	f := newRecoveryFixture(t)
	a := f.task(t, "one", "mother_invite", "mother_kick")
	b := f.task(t, "two", "mother_invite", "mother_kick")
	f.runUntil(t, a.ID, "dwell")
	f.runUntil(t, b.ID, "dwell")
	f.allowDirectRemove = true
	recoveryControl(t, f, a.ID, "mother_kick")
	recoveryControl(t, f, b.ID, "child_leave")
	entered, release := make(chan struct{}), make(chan struct{})
	f.onMutation = func(r *http.Request) { close(entered); <-release }
	done := make(chan struct{})
	go func() { f.s.runSeatRecoveryStep(t.Context(), a.ID); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("manual removal did not start")
	}
	f.s.runSeatRecoveryStep(t.Context(), b.ID)
	if !strings.Contains(f.get(t, b.ID).Message, "串行") {
		t.Error("second manual removal bypassed lock")
	}
	close(release)
	<-done
	f.onMutation = nil
	f.runUntil(t, a.ID, "manual_done")
	f.runUntil(t, b.ID, "manual_done")
}
