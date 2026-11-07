package store

import (
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestMailImportPreservesDurableWorkflows(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for _, scope := range []string{"pro", "mail", "mail_actions"} {
		for _, flow := range []string{"merge_v2", "merchant", "deactivate"} {
			for _, status := range []string{"running", "completed"} {
				for _, entry := range []string{"import", "import_pro", "save_id"} {
					t.Run(scope+"/"+flow+"/"+status+"/"+entry, func(t *testing.T) {
						email := fmt.Sprintf("%s-%s-%s-%s@example.com", scope, flow, status, entry)
						p := model.MailAccountProfile{Email: email, ManagementScope: scope, ProAuto: model.ProAutoState{ID: email, Status: status, Stage: flow, Steps: map[string]string{flow: status}, OrderID: "original-order"}}
						if scope == "mail_actions" {
							p.ManagementScope = "mail"
							p.MailProActions = &model.MailProActions{EnabledAt: now, FinalMode: flow}
						}
						var completed *time.Time
						if status == "completed" {
							completed = &now
						}
						switch flow {
						case "merge_v2":
							p.ProMergeV2 = &model.ProMergeV2{Status: status, Stage: "groups", Steps: map[string]string{"invite": "completed", "login": "completed", "submit": "completed", "remove": "completed", "groups": status}, AdminID: "mother", TeamID: "team", UserID: "child", StartedAt: now, UpdatedAt: now, SubmittedAt: &now, AcceptedAt: &now, CompletedAt: completed, Recovery: &model.ProMergeV2Recovery{Phase: "tail", Cycles: 1, Attempts: 2}}
							p.ProMergeV2Restart = &model.ProMergeV2Restart{ID: "restart", Status: "pending", RequestedAt: now}
						case "merchant":
							p.ProMerchant = &model.ProMerchant{RequestID: "upstream-request", Status: status, SubmittedAt: &now, AcceptedAt: &now, CompletedAt: completed, Attempts: 2}
						case "deactivate":
							p.ProDeactivation = &model.ProDeactivation{Status: status, Stage: "check", Steps: map[string]string{"submit": "completed"}, SubmittedAt: &now, AcceptedAt: &now, ConfirmedAt: completed, AccountID: "personal"}
						}
						before, e := s.SaveMailAccount(p, model.MailAccountCredentials{Email: email, GptPassword: "old"})
						if e != nil {
							t.Fatal(e)
						}
						// Both a minimal re-import and a stale credential-save snapshot
						// lack the workflow now present in the database.
						input := model.MailAccountProfile{Email: email}
						creds := model.MailAccountCredentials{Email: email, GptPassword: "updated"}
						var after model.MailAccountProfile
						if entry == "save_id" {
							input.ID = before.ID
							after, e = s.SaveMailAccount(input, creds)
						} else {
							var created bool
							after, created, e = s.ImportMailAccount(input, creds, entry == "import_pro")
							if created {
								t.Fatal("existing account recreated")
							}
						}
						if e != nil {
							t.Fatal(e)
						}
						stored, savedCreds, e := s.MailAccountCredential(email)
						if e != nil || savedCreds.GptPassword != "updated" {
							t.Fatal("credential update failed", e)
						}
						for _, got := range []model.MailAccountProfile{after, stored} {
							if mustJSON(got.ProMergeV2) != mustJSON(before.ProMergeV2) || mustJSON(got.ProMergeV2Restart) != mustJSON(before.ProMergeV2Restart) || mustJSON(got.ProMerchant) != mustJSON(before.ProMerchant) || mustJSON(got.ProDeactivation) != mustJSON(before.ProDeactivation) || mustJSON(got.ProAuto) != mustJSON(before.ProAuto) || mustJSON(got.MailProActions) != mustJSON(before.MailProActions) {
								t.Fatal("durable workflow changed during credential update")
							}
							if got.ID != before.ID || !got.CreatedAt.Equal(before.CreatedAt) {
								t.Fatal("account identity changed")
							}
						}
					})
				}
			}
		}
	}
}

func TestMailSaveDoesNotRestoreStaleWorkflow(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.SaveMailAccountToPro(model.MailAccountProfile{Email: "stale@example.com"}, model.MailAccountCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	p.ProMergeV2 = &model.ProMergeV2{Status: "completed"}
	p.ProMergeV2Restart = &model.ProMergeV2Restart{ID: "stale", Status: "pending"}
	p.ProMerchant = &model.ProMerchant{RequestID: "stale"}
	p.ProDeactivation = &model.ProDeactivation{Status: "completed"}
	got, err := s.SaveMailAccount(p, model.MailAccountCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProMergeV2 != nil || got.ProMergeV2Restart != nil || got.ProMerchant != nil || got.ProDeactivation != nil {
		t.Fatal("stale input resurrected a workflow absent from the current record")
	}
}
