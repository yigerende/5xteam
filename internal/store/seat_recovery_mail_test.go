package store

import (
	"reflect"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestSeatRecoveryMailClassificationLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	// A mailbox with no Team history must still become used after recovery.
	email := "recover@example.com"
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	used := recoveryStoreAccount(t, s, "used")
	teamBefore, _, _ := s.FreeAccountCredential(used.ID)
	check := func(state string, count int, active bool) {
		t.Helper()
		page, e := s.MailAccountsPage("mail", "recover@", state, 10, 0)
		if e != nil || page.Total != count || len(page.Items) != count {
			t.Fatalf("%s: total=%d rows=%d err=%v", state, page.Total, len(page.Items), e)
		}
		if count > 0 && page.Items[0].SeatRecoveryActive != active {
			t.Fatalf("active field wrong: %+v", page.Items[0])
		}
		if page.SpaceCounts["outside"]+page.SpaceCounts["inside"]+page.SpaceCounts["removed"]+page.SpaceCounts["dead"]+page.SpaceCounts["recovering"] != 2 {
			t.Fatalf("overlapping counts: %v", page.SpaceCounts)
		}
	}
	check("outside", 1, false)
	p := model.SeatRecoveryTask{ID: "recovery-mail", Email: email, TeamID: "team", Stage: "login", Status: "queued", CreatedAt: time.Now()}
	if err = s.CreateSeatRecoveryTask(p); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"login", "join", "confirm", "dwell", "switch", "leave", "verify", "manual_remove"} {
		for _, status := range []string{"queued", "running", "failed"} {
			if _, err = s.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) { v.Stage, v.Status, v.Paused = stage, status, status == "failed" }); err != nil {
				t.Fatal(err)
			}
			check("recovering", 1, true)
			check("outside", 0, false)
			check("removed", 0, false)
		}
	}
	// No full list/credentials are needed for a paged search or beyond-last page.
	page, err := s.MailAccountsPage("mail", "recover@", "recovering", 10, 10)
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatal("recovery pagination failed", err)
	}
	if err = s.DeleteSeatRecoveryTask(p.ID); err != nil {
		t.Fatal(err)
	}
	check("outside", 1, false) // deleting an unverified task must not fake usage
	if err = s.CreateSeatRecoveryTask(p); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err = s.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) { v.LeftAt = &now; v.Stage = "cooldown" }); err != nil {
		t.Fatal(err)
	}
	check("recovering", 0, false)
	check("removed", 1, false) // no need to wait for next-task cooldown
	if err = s.DeleteSeatRecoveryTask(p.ID); err != nil {
		t.Fatal(err)
	}
	check("removed", 1, false)
	// Reimport and restart cannot erase the verified-departure marker.
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "new-at"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	check("removed", 1, false)
	teamAfter, _, _ := s.FreeAccountCredential(used.ID)
	if !reflect.DeepEqual(teamBefore, teamAfter) {
		t.Fatal("mail classification altered Team history")
	}
	// New recovery overrides historical usage, but a dead account stays dead.
	p.ID = "recovery-again"
	if err = s.CreateSeatRecoveryTask(p); err != nil {
		t.Fatal(err)
	}
	check("recovering", 1, true)
	if _, err = s.db.Exec("UPDATE mail_accounts SET profile=json_set(profile,'$.chatgpt_status','dead') WHERE email=?", email); err != nil {
		t.Fatal(err)
	}
	check("recovering", 0, false)
	check("dead", 1, true)
}

func TestSeatRecoveryMailBackfillsExistingDepartures(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	email := "old-recovery@example.com"
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	p := model.SeatRecoveryTask{ID: "old-task", Email: email, TeamID: "team", Stage: "login"}
	if err = s.CreateSeatRecoveryTask(p); err != nil {
		t.Fatal(err)
	}
	// Simulate an old database whose completed task predates the mail field.
	now := time.Now()
	p.LeftAt, p.Finished, p.Stage = &now, true, "completed"
	if _, err = s.db.Exec("UPDATE seat_recovery_tasks SET finished=1,payload=? WHERE id=?", mustJSON(p), p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.DeleteSeatRecoveryTask(p.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.MailAccountsPage("mail", "", "removed", 10, 0)
	if err != nil || page.Total != 1 || page.Items[0].SeatRecoveryLeftAt == nil {
		t.Fatalf("old recovery lost after deletion: %+v %v", page, err)
	}
}
