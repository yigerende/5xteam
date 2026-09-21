package store

import (
	"encoding/json"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProRecoverManuallyCompletedAutomation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, quota := range []string{"waiting", "completed"} {
		email := quota + "@example.com"
		_, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{})
		if err != nil {
			t.Fatal(err)
		}
		s.UpdateMailAccountManagementScope(email, "pro")
		p, _, _ := s.MailAccountCredential(email)
		next := time.Now().Add(-time.Minute)
		p.ProAuto = model.ProAutoState{ID: email, Status: "waiting_quota", Stage: "quota", NextCheckAt: &next,
			Steps: map[string]string{"quota": quota}, Error: "额度尚未达到阈值，等待下次检测"}
		p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "completed", "completed", "completed", "completed"
		p.SpaceMergedOnce = true
		// Reproduce existing database rows without applying write-time reconciliation.
		raw, _ := json.Marshal(p)
		if _, err = s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", string(raw), email); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	for _, quota := range []string{"waiting", "completed"} {
		p, _, _ := s.MailAccountCredential(quota + "@example.com")
		want := quota
		if quota == "waiting" {
			want = "skipped"
		}
		if p.ProAuto.Status != "completed" || p.ProAuto.Steps["quota"] != want || p.ProAuto.Error != "" || p.ProAuto.NextCheckAt != nil {
			t.Fatalf("stale quota wait not repaired: %+v", p.ProAuto)
		}
	}
	due, err := s.DueProAutomations(time.Now())
	if err != nil || len(due) != 0 {
		t.Fatal("completed rows still due", err)
	}
}
