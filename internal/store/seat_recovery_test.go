package store

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func recoveryStoreAccount(t *testing.T, s *Store, name string) model.FreeAccountProfile {
	t.Helper()
	email := name + "@example.com"
	if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	p, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: name}, "at")
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.RemoveStatus = "completed"; p.DownstreamCleaned = true })
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func recoveryStoreTask(p model.FreeAccountProfile) model.SeatRecoveryTask {
	return model.SeatRecoveryTask{ID: "recovery-" + p.ID, TeamID: "team", Email: p.Email, FreeID: p.ID, FreeCycle: p.CycleID, Stage: "login", Status: "queued", Settings: model.DefaultSeatRecoverySettings(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

func TestSeatRecoveryReservationOnlyBlocksItsOwnChild(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := recoveryStoreAccount(t, s, "one")
	b := recoveryStoreAccount(t, s, "two")
	task := recoveryStoreTask(a)
	if err = s.CreateSeatRecoveryTask(task); err != nil {
		t.Fatal(err)
	}
	a, _, err = s.FreeAccountCredential(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ClaimAutoRotationAccount(a.ID, "team-task-a"); ok {
		t.Fatal("Team stole recovering account")
	}
	if _, err = s.BeginFreeAccountCycle(a.ID, a.CycleID, "at"); err == nil {
		t.Fatal("manual reuse stole recovery")
	}
	if _, _, err = s.SaveImportedFreeAccount(a, "at"); err == nil {
		t.Fatal("manual import stole recovery")
	}
	after, _, _ := s.FreeAccountCredential(a.ID)
	if !reflect.DeepEqual(a, after) {
		t.Fatal("recovery guard changed Team account")
	}
	if ok, err := s.ClaimAutoRotationAccount(b.ID, "team-task-b"); err != nil || !ok {
		t.Fatalf("unrelated Team account blocked: %v", err)
	}
	if _, err = s.UpdateSeatRecoveryTask(task.ID, func(p *model.SeatRecoveryTask) { p.Finished = true }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginFreeAccountCycle(a.ID, a.CycleID, "new-at"); err != nil {
		t.Fatalf("finished task did not release manual reuse: %v", err)
	}
}

func TestSeatRecoveryAtomicRaceWithRotationClaim(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 12; i++ {
		p := recoveryStoreAccount(t, s, string(rune('a'+i)))
		task := recoveryStoreTask(p)
		var wg sync.WaitGroup
		var claim bool
		var recoveryErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; claim, _ = s.ClaimAutoRotationAccount(p.ID, "claim-"+p.ID) }()
		go func() { defer wg.Done(); <-start; recoveryErr = s.CreateSeatRecoveryTask(task) }()
		close(start)
		wg.Wait()
		if claim == (recoveryErr == nil) {
			t.Fatalf("exactly one owner required: claim=%v recoveryErr=%v", claim, recoveryErr)
		}
	}
}

func TestSeatRecoveryLaneUniqueAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := recoveryStoreTask(recoveryStoreAccount(t, s, "one"))
	b := recoveryStoreTask(recoveryStoreAccount(t, s, "two"))
	c := recoveryStoreTask(recoveryStoreAccount(t, s, "three"))
	c.TeamID = "other-team"
	for _, p := range []model.SeatRecoveryTask{a, b, c} {
		if err = s.CreateSeatRecoveryTask(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.UpdateSeatRecoveryTask(a.ID, func(p *model.SeatRecoveryTask) { p.Lane = true }); err != nil {
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
	if _, err = s.UpdateSeatRecoveryTask(b.ID, func(p *model.SeatRecoveryTask) { p.Lane = true }); err == nil {
		t.Fatal("same mother lane was not durable")
	}
	if _, err = s.UpdateSeatRecoveryTask(c.ID, func(p *model.SeatRecoveryTask) { p.Lane = true }); err != nil {
		t.Fatal("different mother blocked")
	}
}

func TestSeatRecoveryOutsideAndUsedEligibility(t *testing.T) {
	for _, tc := range []struct {
		name               string
		eligible, admitted bool
	}{
		{"unused-mail", true, true},
		{"waiting-first", true, true},
		{"waiting-reuse", true, true},
		{"used-completed", true, true},
		{"used-remote", true, true},
		{"inside", false, false},
		{"dead-pipeline", false, false},
		{"dead-gpt", false, false},
		{"dead-registration", false, false},
		{"pro", false, false},
		{"missing", false, false},
		{"invite-in-flight", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			email := tc.name + "@example.com"
			mail := model.MailAccountProfile{Email: email}
			if tc.name == "dead-gpt" {
				mail.ChatGPTStatus = "dead"
			}
			if tc.name == "dead-registration" {
				mail.RegistrationStatus = "dead"
			}
			if tc.name == "pro" {
				mail.ManagementScope = "pro"
			}
			if tc.name != "missing" {
				if _, err = s.SaveMailAccount(mail, model.MailAccountCredentials{Email: email, AccessToken: "at"}); err != nil {
					t.Fatal(err)
				}
			}
			free := model.FreeAccountProfile{Email: email}
			switch tc.name {
			case "waiting-first", "waiting-reuse", "used-completed", "used-remote", "inside", "dead-pipeline", "invite-in-flight":
				free, _, err = s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: tc.name}, "at")
				if err != nil {
					t.Fatal(err)
				}
				free, err = s.UpdateFreeAccount(free.ID, func(p *model.FreeAccountProfile) {
					switch tc.name {
					case "waiting-reuse":
						p.ReusePending = true
					case "used-completed":
						p.AcceptStatus = "completed"
						p.RemoveStatus = "completed"
					case "used-remote":
						now := time.Now()
						p.AcceptStatus = "completed"
						p.RemoteRemovedAt = &now
					case "inside":
						p.AcceptStatus = "completed"
					case "dead-pipeline":
						p.Dead = true
					case "invite-in-flight":
						p.InviteStatus = "completed"
						p.TeamAccountID = "other-team"
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if free.ID != "" {
				free, _, err = s.FreeAccountCredential(free.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			eligible, err := s.SeatRecoveryMailEligible(email)
			if err != nil || eligible != tc.eligible {
				t.Fatalf("eligible=%v err=%v", eligible, err)
			}
			task := recoveryStoreTask(free)
			if err = s.CreateSeatRecoveryTask(task); (err == nil) != tc.admitted {
				t.Fatalf("admission: %v", err)
			}
			if free.ID != "" {
				after, _, err := s.FreeAccountCredential(free.ID)
				if err != nil || !reflect.DeepEqual(free, after) {
					t.Fatal("admission changed Team state")
				}
			}
		})
	}
}
