package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestReuseRotationFillsAfterPreparationFailureWithoutReassigningInflight(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings := model.AutoRotationSettings{ThresholdPercent: 50, IntervalSeconds: 300, Concurrency: 10, MaxPerRun: 10, AllowMultiMotherReuse: true, JoinMethod: "child_request"}
	if _, err := st.SaveAutoRotationSettings(settings); err != nil {
		t.Fatal(err)
	}
	oldMother := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "old mother", Email: "old@example.com", TeamAccountID: "old-team"}, "fixture-token", "")
	currentMother := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "new mother", Email: "new@example.com"}, "fixture-token", "")
	if err := st.SaveAdminCapacitySnapshot(oldMother.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAdminCapacitySnapshot(currentMother.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 3}}); err != nil {
		t.Fatal(err)
	}
	waiting, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "waiting@example.com", UserID: "waiting"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	used, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "used@example.com", UserID: "used"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(used.ID, func(p *model.FreeAccountProfile) {
		p.AdminAccountID, p.TeamAccountID, p.SeatType = oldMother.ID, oldMother.TeamAccountID, "prolite"
		p.InviteStatus, p.AcceptStatus = "completed", "completed"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(used.ID, func(p *model.FreeAccountProfile) {
		p.RemoveStatus, p.DownstreamCleaned = "completed", true
	}); err != nil {
		t.Fatal(err)
	}
	inflight, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "inflight@example.com", UserID: "inflight"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(inflight.ID, func(p *model.FreeAccountProfile) {
		p.AdminAccountID, p.TeamAccountID, p.SeatType = oldMother.ID, oldMother.TeamAccountID, "prolite"
		p.InviteStatus = "completed"
	}); err != nil {
		t.Fatal(err)
	}
	saveImportLimitMail(t, st, "mail-1@example.com")
	saveImportLimitMail(t, st, "mail-2@example.com")
	saveImportLimitMail(t, st, "mail-3@example.com")
	run := model.AutoRotationRun{ID: "reuse-fallback", Status: "running", SeatTotal: 3, SeatRemaining: 3, StartedAt: time.Now()}
	if err := st.SaveAutoRotationRun(run); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 1000)}
	s.executeAutoRotation(context.Background(), run, settings)
	tasks := st.AutoRotationTasks(run.ID)
	if len(tasks) != 3 {
		t.Fatalf("tasks=%d, want 3", len(tasks))
	}
	if tasks[0].AccountID == used.ID || tasks[1].AccountID == used.ID || tasks[2].AccountID == used.ID {
		t.Fatal("failed reuse preparation occupied a task slot")
	}
	if tasks[0].AccountID == inflight.ID || tasks[1].AccountID == inflight.ID || tasks[2].AccountID == inflight.ID {
		t.Fatal("inflight invitation was reassigned")
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.Email] = true
		if task.AdminAccountID != currentMother.ID {
			t.Fatalf("unexpected mother for %s: %s", task.Email, task.AdminAccountID)
		}
	}
	if !seen[waiting.Email] || !seen["mail-1@example.com"] || !seen["mail-2@example.com"] || seen["mail-3@example.com"] {
		t.Fatalf("candidate order/budget incorrect: %+v", seen)
	}
	failed, inflightSkipped, prepared, started := false, false, 0, false
	for _, event := range drainReuseEvents(s.auditQueue) {
		if event.Type == "candidate_failed" && event.AccountID == used.ID && !strings.Contains(event.Message, "复用准备失败") {
			t.Fatalf("missing preparation reason: %+v", event)
		}
		if event.Type == "candidate_failed" && event.AccountID == used.ID {
			failed = true
		}
		if event.Type == "candidate_skipped" && event.AccountID == inflight.ID {
			inflightSkipped = true
		}
		if event.Type == "candidate_prepared" {
			prepared++
		}
		if event.Type == "audit" && event.Stage == "task_started" {
			started = true
		}
		if event.Type == "preparation_complete" && prepared != 3 {
			t.Fatalf("tasks started before all candidates were prepared: %d", prepared)
		}
	}
	if !failed || !inflightSkipped || prepared != 3 || !started {
		t.Fatalf("audit incomplete: failed=%v inflight=%v prepared=%d started=%v", failed, inflightSkipped, prepared, started)
	}
}

func TestReuseRotationUsesNewMotherBeforeMailFallback(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"test"}`))
			return
		}
		http.Error(w, "fixture only", http.StatusNotFound)
	}))
	defer upstream.Close()
	global := st.Settings()
	global.BaseURL = upstream.URL
	if err := st.SaveSettings(global); err != nil {
		t.Fatal(err)
	}
	settings := model.AutoRotationSettings{ThresholdPercent: 50, IntervalSeconds: 300, Concurrency: 4, MaxPerRun: 2, AllowMultiMotherReuse: true, JoinMethod: "child_request"}
	if _, err := st.SaveAutoRotationSettings(settings); err != nil {
		t.Fatal(err)
	}
	oldMother := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "previous", Email: "previous@example.com", TeamAccountID: "old-team"}, "fixture", "")
	newMother := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "next", Email: "next@example.com", TeamAccountID: "new-team"}, "fixture", "")
	if err := st.SaveAdminCapacitySnapshot(oldMother.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAdminCapacitySnapshot(newMother.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 1}}); err != nil {
		t.Fatal(err)
	}
	usedToken := saveImportLimitMail(t, st, "used@example.com")
	used, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "used@example.com", UserID: "used@example.com"}, usedToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(used.ID, func(p *model.FreeAccountProfile) {
		p.AdminAccountID, p.TeamAccountID, p.SeatType = oldMother.ID, oldMother.TeamAccountID, "prolite"
		p.InviteStatus, p.AcceptStatus = "completed", "completed"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(used.ID, func(p *model.FreeAccountProfile) {
		p.RemoveStatus, p.DownstreamCleaned = "completed", true
	}); err != nil {
		t.Fatal(err)
	}
	saveImportLimitMail(t, st, "fresh@example.com")
	run := model.AutoRotationRun{ID: "reuse-next-mother", Status: "running", StartedAt: time.Now()}
	if err := st.SaveAutoRotationRun(run); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 1000)}
	s.executeAutoRotation(context.Background(), run, settings)
	tasks := st.AutoRotationTasks(run.ID)
	if len(tasks) != 2 {
		t.Fatalf("tasks=%d, want 2", len(tasks))
	}
	byEmail := map[string]model.AutoRotationTask{}
	for _, task := range tasks {
		byEmail[task.Email] = task
	}
	if byEmail[used.Email].Source != "reuse" || byEmail[used.Email].AdminAccountID != newMother.ID || byEmail["fresh@example.com"].Source != "mail" || byEmail["fresh@example.com"].AdminAccountID != oldMother.ID {
		t.Fatalf("wrong reuse/mail assignment: %+v", byEmail)
	}
}

func TestArchivedReuseIdentityMatchesOpenAIUserID(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "old@example.com", UserID: "same-user"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(old.ID, func(p *model.FreeAccountProfile) {
		p.TeamAccountID, p.InviteStatus, p.AcceptStatus = "old-team", "completed", "completed"
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFreeAccount(old.ID); err != nil {
		t.Fatal(err)
	}
	archived, found, err := st.ArchivedTeamAccount("new@example.com", "same-user")
	if err != nil || !found || archived.TeamAccountID != "old-team" {
		t.Fatalf("renamed identity not found: %+v found=%v err=%v", archived, found, err)
	}
}

func drainReuseEvents(queue chan model.AutoRotationEvent) []model.AutoRotationEvent {
	result := make([]model.AutoRotationEvent, 0, len(queue))
	for len(queue) > 0 {
		result = append(result, <-queue)
	}
	return result
}
