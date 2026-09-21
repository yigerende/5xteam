package store

import (
	"chatgpt-space-merge/internal/model"
	"testing"
	"time"
)

func TestProAutomationRecoveryAndDueSelection(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		email, status, oauth string
		next                 *time.Time
	}{{"session@example.com", "running", "", nil}, {"tail@example.com", "running", "completed", nil}, {"due@example.com", "waiting_quota", "completed", &past}, {"later@example.com", "waiting_quota", "completed", &future}} {
		if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: tc.email}, model.MailAccountCredentials{}); err != nil {
			t.Fatal(err)
		}
		s.UpdateMailAccountManagementScope(tc.email, "pro")
		_, err = s.UpdateProAccount(tc.email, func(p *model.MailAccountProfile) {
			p.ProAuto = model.ProAutoState{ID: tc.email, Status: tc.status, OrderID: "original-order", Stage: "oauth", Steps: map[string]string{"oauth": tc.oauth}, NextCheckAt: tc.next}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	p, c, _ := s.MailAccountCredential("session@example.com")
	if p.ProAuto.Status != "interrupted" || p.ProAuto.OrderID != "original-order" {
		t.Fatal("lost order/restarted session")
	}
	// Reimporting/updating an AT cannot erase automation progress.
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: p.Email}, c); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.MailAccountCredential(p.Email)
	if p.ProAuto.ID == "" {
		t.Fatal("automation lost on credential save")
	}
	due, err := s.DueProAutomations(now)
	if err != nil || len(due) != 1 || due[0].Email != "due@example.com" {
		t.Fatal("wrong due accounts", err, due)
	}
	p, _, _ = s.MailAccountCredential("tail@example.com")
	if p.ProAuto.Steps["oauth"] != "completed" {
		t.Fatal("lost post-purchase token milestone")
	}
}
