package store

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProManualStagesPersistAndDoNotScheduleAutomation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const email = "manual@example.com"
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{}); err != nil {
		t.Fatal(err)
	}
	s.UpdateMailAccountManagementScope(email, "pro")
	for _, step := range []string{"login", "recharge", "oauth", "push", "quota", "merge"} {
		if err = s.StartProManualStage(email, step, step+"-old"); err != nil {
			t.Fatal(err)
		}
		p, _, _ := s.MailAccountCredential(email)
		if p.ProStages().Steps[step] != "running" {
			t.Fatal("running step invisible", step)
		}
		if err = s.StartProManualStage(email, step, step+"-new"); err != nil {
			t.Fatal(err)
		}
		s.FinishProManualStage(email, step, step+"-old", "failed", "late response")
		p, _, _ = s.MailAccountCredential(email)
		if p.ProManualStages[step].Status != "running" {
			t.Fatal("late result replaced active run", step)
		}
		s.FinishProManualStage(email, step, step+"-new", "completed", "")
	}
	p, _, _ := s.MailAccountCredential(email)
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "updated"}); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.MailAccountCredential(email)
	for _, step := range []string{"login", "recharge", "oauth", "push", "quota", "merge"} {
		if p.ProStages().Steps[step] != "completed" {
			t.Fatal("credential save erased progress", step)
		}
	}
	if p.ProAuto.ID != "" || p.ProAuto.Steps["oauth"] != "" {
		t.Fatal("manual steps polluted automation")
	}
	due, err := s.DueProAutomations(time.Now().Add(time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatal("manual steps scheduled automatic work", err)
	}
	s.StartProManualStage(email, "login", "restart")
	s.StartProManualStage(email, "recharge", "payment")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RecoverProManualStages(); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.MailAccountCredential(email)
	if p.ProStages().Steps["login"] != "interrupted" || p.ProStages().Errors["login"] == "" {
		t.Fatal("restart leaves infinite spinner")
	}
	if p.ProStages().Steps["recharge"] != "unknown" || p.ProStages().Steps["oauth"] != "completed" {
		t.Fatal("restart lost payment or completed step")
	}
	s.FinishProManualStage(email, "login", "restart", "completed", "")
	p, _, _ = s.MailAccountCredential(email)
	if p.ProStages().Steps["login"] != "interrupted" {
		t.Fatal("terminated job revived")
	}
}

func TestProManualDisplayPreservesAutomaticState(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	now := time.Now()
	p := model.MailAccountProfile{
		ProAuto:         model.ProAutoState{ID: "auto", StartedAt: now, Status: "failed", Stage: "oauth", Error: "original auto failure", Steps: map[string]string{"login": "completed", "oauth": "failed"}},
		ProManualStages: map[string]model.ProStageProgress{"login": {StartedAt: old, Status: "failed"}, "oauth": {StartedAt: now.Add(time.Second), Status: "completed"}},
	}
	display := p.ProStages()
	if display.Steps["login"] != "completed" || display.Steps["oauth"] != "completed" || display.Errors["oauth"] != "" {
		t.Fatal("display does not prefer latest execution", display)
	}
	if p.ProAuto.Steps["oauth"] != "failed" || p.ProAuto.Error == "" {
		t.Fatal("display modified automation checkpoints")
	}
}
