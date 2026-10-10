package store

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProManualRecoveryRepairsExistingRecordsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"recovered", "oauth-only", "stopped", "paused"} {
		email := mode + "@example.com"
		if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.UpdateMailAccountManagementScope(email, "pro"); err != nil {
			t.Fatal(err)
		}
		if err = s.SaveMailAccountOAuth(email, "stored-at", "stored-rt"); err != nil {
			t.Fatal(err)
		}
		_, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			now := time.Now()
			p.ProAuto = model.ProAutoState{ID: mode, OrderID: "original-order", Status: "failed", Stage: "oauth", Error: "old failure", StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), QuotaUsedThreshold: 90, QuotaIntervalSeconds: 60, Steps: map[string]string{"login": "completed", "recharge": "completed", "oauth": "failed"}}
			p.ProManualStages = map[string]model.ProStageProgress{"oauth": {Status: "completed", StartedAt: now.Add(-30 * time.Second), UpdatedAt: now}, "push": {Status: "completed", StartedAt: now, UpdatedAt: now}}
			p.PushStatus, p.PushProvider, p.Sub2AccountID = "completed", "sub2", 42
			if mode == "oauth-only" {
				delete(p.ProManualStages, "push")
			}
			if mode == "stopped" {
				p.ProAuto.Status = "stopped"
			}
			if mode == "paused" {
				p.ProMigration = &model.ProMigrationState{Paused: true}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	for email, want := range map[string]string{"recovered@example.com": "waiting_quota", "oauth-only@example.com": "awaiting_push", "stopped@example.com": "stopped", "paused@example.com": "failed"} {
		p, _, err := s.MailAccountCredential(email)
		if err != nil || p.ProAuto.Status != want || p.ProAuto.OrderID != "original-order" {
			t.Fatalf("%s: %+v %v", email, p.ProAuto, err)
		}
	}
	due, err := s.DueProAutomations(time.Now())
	if err != nil || len(due) != 1 || due[0].Email != "recovered@example.com" {
		t.Fatal("not scheduled after repair", due, err)
	}
	next := *due[0].ProAuto.NextCheckAt
	if err = s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.MailAccountCredential("recovered@example.com")
	if !p.ProAuto.NextCheckAt.Equal(next) {
		t.Fatal("restart changed already repaired schedule")
	}
}
