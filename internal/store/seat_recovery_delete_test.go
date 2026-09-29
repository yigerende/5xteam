package store

import (
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestSeatRecoveryForceDeleteFencesConcurrentWriters(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account := recoveryStoreAccount(t, s, "one")
	p := recoveryStoreTask(account)
	if err = s.CreateSeatRecoveryTask(p); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 10; j++ {
				_ = s.AppendSeatRecoveryLog(p.ID, "login", "late event")
				_, _ = s.ClaimSeatRecoveryEntry(p)
				_, _ = s.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) { v.Lane = true })
				_ = s.SaveSeatRecoveryLogin(p.ID, "concurrent-at", "concurrent-session")
			}
		}()
	}
	close(start)
	if err = s.DeleteSeatRecoveryTask(p.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err = s.SeatRecoveryTask(p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("task resurrected: %v", err)
	}
	for _, table := range []string{"seat_recovery_tasks", "seat_recovery_entry_lanes", "seat_recovery_logs"} {
		var count int
		if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphan %s count=%d err=%v", table, count, err)
		}
	}
	mailBefore, credsBefore, _ := s.MailAccountCredential(p.Email)
	if err = s.SaveSeatRecoveryLogin(p.ID, "after-delete", "after-delete"); err == nil {
		t.Fatal("deleted login saved credentials")
	}
	mailAfter, credsAfter, _ := s.MailAccountCredential(p.Email)
	if !reflect.DeepEqual(mailBefore, mailAfter) || !reflect.DeepEqual(credsBefore, credsAfter) {
		t.Fatal("deleted login modified mail")
	}
}
