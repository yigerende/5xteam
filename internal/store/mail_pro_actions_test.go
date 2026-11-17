package store

import (
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestMailActionsDefaultAndLegacyScope(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new import", true: "legacy missing scope"}[legacy], func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			const email = "external+alias@example.com"
			credentials := model.MailAccountCredentials{Email: email, GptPassword: "fixture-password", TotpSecret: "fixture-totp"}
			p, err := s.SaveMailAccount(model.MailAccountProfile{Email: email}, credentials)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				if _, err = s.db.Exec("UPDATE mail_accounts SET profile=json_remove(profile,'$.management_scope') WHERE email=?", email); err != nil {
					t.Fatal(err)
				}
			} else if p.ManagementScope != "mail" {
				t.Fatalf("default import scope = %q", p.ManagementScope)
			}
			p, got, err := s.MailAccountCredential(email)
			if err != nil || p.ManagementScope != "mail" || got != credentials || p.ProActionsAllowed() {
				t.Fatalf("mailbox read: scope=%q err=%v; ordinary import must not enable actions", p.ManagementScope, err)
			}
			listed := s.MailAccountsByManagementScope("mail")
			if len(listed) != 1 || listed[0].ManagementScope != "mail" || len(s.MailAccountsByManagementScope("pro")) != 0 {
				t.Fatal("mailbox list and action lookup disagree")
			}
			p, err = s.EnableMailProActions(email, "")
			if err != nil || !p.ProActionsAllowed() || p.MailProActions.FinalMode != "" {
				t.Fatalf("standalone OAI admission: %v", err)
			}
			p, err = s.EnableMailProActions(email, "merge_v2")
			if err != nil || !p.ProFinalStepAllowed("merge_v2") || p.ProManagedAt != nil || p.ProAuto.ID != "" {
				t.Fatalf("final admission: %v", err)
			}
			var scope string
			if err = s.db.QueryRow("SELECT json_extract(profile,'$.management_scope') FROM mail_accounts WHERE email=?", email).Scan(&scope); err != nil || scope != "mail" {
				t.Fatal("admitted scope not persisted for background workers", scope, err)
			}
			if _, err = s.EnableMailProActions(email, "merchant"); err == nil {
				t.Fatal("pinned final mode changed")
			}
			p, got, err = s.MailAccountCredential(email)
			if err != nil || got != credentials {
				t.Fatal("admission changed credentials")
			}
		})
	}
}

func TestMailActionsRejectProAndRunningAccounts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const email = "pro@example.com"
	if _, err = s.SaveMailAccountToPro(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnableMailProActions(email, ""); err == nil {
		t.Fatal("Pro account admitted to mail actions")
	}
	if _, err = s.UpdateMailAccountManagementScope(email, "mail"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE mail_accounts SET profile=json_set(profile,'$.pro_workflow_running',json('true')) WHERE email=?", email); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnableMailProActions(email, ""); err == nil {
		t.Fatal("running account admitted")
	}
}
