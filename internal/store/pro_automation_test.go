package store

import (
	"chatgpt-space-merge/internal/model"
	"testing"
	"time"
)

func TestSIWCConsentFinalRecoversOnlyBeforeAction(t *testing.T) {
	s := transferStore(t)
	for _, stage := range []string{"oauth", "merge"} {
		email := stage + "@example.com"
		transferSource(t, s, email)
		_, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProAuto = model.ProAutoState{ID: "flow", OrderID: "paid", Status: "running", Stage: stage,
				Steps:              map[string]string{"oauth": "failed", "push": "skipped", "quota": "skipped"},
				SIWCConsentFailure: &model.ProSIWCConsentFailure{OrderID: "paid", Exhausted: true, Retries: 2}}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.MailAccountCredential("oauth@example.com")
	if p.ProAuto.Status != "waiting_quota" || p.ProAuto.NextCheckAt == nil || p.ProAuto.Steps["quota"] != "skipped" {
		t.Fatal("safe final-action gap not recovered")
	}
	p, _, _ = s.MailAccountCredential("merge@example.com")
	if p.ProAuto.Status != "interrupted" {
		t.Fatal("replayed an already-started action")
	}
}

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

func TestProAutomationUserStopSurvivesLateWritesAndRestart(t *testing.T) {
	s := transferStore(t)
	const email = "stopped-restock@example.com"
	transferSource(t, s, email)
	_, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "stopped", StopRequested: true, Status: "stopped", Steps: map[string]string{"oauth": "completed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	// A quota response arriving after the stop must not schedule more work.
	p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		next := time.Now().Add(-time.Minute)
		p.ProAuto.Status, p.ProAuto.NextCheckAt = "waiting_quota", &next
		p.TransferStatus = "completed"
	})
	if err != nil || p.ProAuto.Status != "stopped" || p.ProAuto.NextCheckAt != nil {
		t.Fatal("late result erased stop", err)
	}
	if err = s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	p, _, err = s.MailAccountCredential(email)
	if err != nil || p.ProAuto.Status != "stopped" || !p.ProAuto.StopRequested {
		t.Fatal("restart erased stop", err)
	}
	due, err := s.DueProAutomations(time.Now())
	if err != nil || len(due) != 0 {
		t.Fatal("stopped workflow scheduled", err)
	}
}
