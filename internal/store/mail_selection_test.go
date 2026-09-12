package store

import (
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestMailOutsideInvalidATSelectionAcrossAllPages(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkedAt := time.Now()
	want := map[string]bool{}
	for i := 0; i < 501; i++ {
		email := fmt.Sprintf("invalid-%03d@example.com", i)
		if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: email, ATCheckedAt: &checkedAt}, model.MailAccountCredentials{Email: email, AccessToken: "fixture-at"}); err != nil {
			t.Fatal(err)
		}
		want[email] = true
	}

	for _, tc := range []struct {
		name, scope, chatGPTStatus, registrationStatus  string
		checked, valid, inside, removed, dead, selected bool
	}{
		{name: "unchecked"},
		{name: "valid", checked: true, valid: true},
		{name: "pro", scope: "pro", checked: true},
		{name: "inside", checked: true, inside: true},
		{name: "removed", checked: true, inside: true, removed: true},
		{name: "mail-dead", checked: true, chatGPTStatus: "dead"},
		{name: "registration-dead", checked: true, registrationStatus: "dead"},
		{name: "pipeline-dead", checked: true, dead: true},
		{name: "pending-invite", checked: true, selected: true},
	} {
		email := tc.name + "@example.com"
		profile := model.MailAccountProfile{Email: email, ManagementScope: tc.scope, ATValid: tc.valid, ChatGPTStatus: tc.chatGPTStatus, RegistrationStatus: tc.registrationStatus}
		if tc.checked {
			profile.ATCheckedAt = &checkedAt
		}
		if _, err := s.SaveMailAccount(profile, model.MailAccountCredentials{Email: email, AccessToken: "fixture-at"}); err != nil {
			t.Fatal(err)
		}
		account, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "fixture-at")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateFreeAccount(account.ID, func(p *model.FreeAccountProfile) {
			p.InviteStatus = "completed"
			p.Dead = tc.dead
			if tc.inside {
				p.AcceptStatus = "completed"
			}
			if tc.removed {
				p.RemoveStatus = "completed"
			}
		}); err != nil {
			t.Fatal(err)
		}
		// An older outside record must not override the latest inside record.
		if tc.inside {
			if _, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: "older-" + email, ImportedAt: checkedAt.Add(-time.Hour)}, "fixture-at"); err != nil {
				t.Fatal(err)
			}
			// SaveImportedFreeAccount generates timestamps, so explicitly age this fixture.
			if _, err := s.db.Exec(`UPDATE free_accounts SET profile=json_set(profile,'$.imported_at',?) WHERE user_id=?`, checkedAt.Add(-time.Hour).Format(time.RFC3339Nano), "older-"+email); err != nil {
				t.Fatal(err)
			}
		}
		if tc.selected {
			want[email] = true
		}
	}
	emails, err := s.MailOutsideInvalidATEmails()
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != len(want) {
		t.Fatalf("selected=%d, want %d (must not be capped at 500)", len(emails), len(want))
	}
	for _, email := range emails {
		if !want[email] {
			t.Fatalf("unexpected or duplicate selection: %s", email)
		}
		delete(want, email)
	}
}

func TestMailOutsideInvalidATSelectionEmpty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	emails, err := s.MailOutsideInvalidATEmails()
	if err != nil || emails == nil || len(emails) != 0 {
		t.Fatalf("emails=%v err=%v", emails, err)
	}
}
