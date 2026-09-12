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

func TestMailAccountSelectionAllConditionCombinations(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	type fixture struct {
		email, status string
		mask          int
	}
	fixtures := []fixture{}
	for _, status := range []string{"not_logged_in", "unknown", "valid", "invalid"} {
		for mask := 0; mask < 8; mask++ {
			email := fmt.Sprintf("%s-%d@example.com", status, mask)
			profile := model.MailAccountProfile{Email: email, ATValid: status == "valid"}
			if status == "valid" || status == "invalid" {
				profile.ATCheckedAt = &now
			}
			credentials := model.MailAccountCredentials{Email: email}
			if status != "not_logged_in" {
				credentials.AccessToken = "fixture-at"
			}
			if mask&1 != 0 {
				credentials.RefreshToken = "fixture-rt"
			}
			if mask&2 != 0 {
				credentials.GptPassword = "fixture-password"
			}
			if mask&4 != 0 {
				credentials.TotpSecret = "fixture-totp"
			}
			if _, err := s.SaveMailAccount(profile, credentials); err != nil {
				t.Fatal(err)
			}
			fixtures = append(fixtures, fixture{email, status, mask})
		}
	}
	for _, status := range []string{"", "valid", "invalid", "not_logged_in"} {
		for mask := 0; mask < 8; mask++ {
			t.Run(fmt.Sprintf("%s-%d", status, mask), func(t *testing.T) {
				filter := MailAccountSelection{ATStatus: status, RequireRT: mask&1 != 0, RequirePassword: mask&2 != 0, RequireTOTP: mask&4 != 0}
				want := map[string]bool{}
				for _, f := range fixtures {
					if (status == "" || f.status == status) && f.mask&mask == mask {
						want[f.email] = true
					}
				}
				got, err := s.SelectOutsideMailAccounts(filter)
				if err != nil || len(got) != len(want) {
					t.Fatalf("got=%v want=%v err=%v", got, want, err)
				}
				for _, email := range got {
					if !want[email] {
						t.Fatal("unexpected selection", email)
					}
				}
				filter.Emails = []string{" VALID-7@EXAMPLE.COM ", "invalid-3@example.com", "not_logged_in-7@example.com"}
				got, err = s.SelectOutsideMailAccounts(filter)
				expectedPage := 0
				for _, email := range []string{"valid-7@example.com", "invalid-3@example.com", "not_logged_in-7@example.com"} {
					if want[email] {
						expectedPage++
					}
				}
				if err != nil || len(got) != expectedPage {
					t.Fatalf("page=%v expected=%d err=%v", got, expectedPage, err)
				}
				for _, email := range got {
					if email != "valid-7@example.com" && email != "invalid-3@example.com" && email != "not_logged_in-7@example.com" {
						t.Fatal("outside page", email)
					}
				}
			})
		}
	}
	if emails, err := s.SelectOutsideMailAccounts(MailAccountSelection{Emails: []string{}}); err != nil || len(emails) != 0 {
		t.Fatal("empty page selected records", emails, err)
	}
	if _, err := s.SelectOutsideMailAccounts(MailAccountSelection{ATStatus: "bad"}); err == nil {
		t.Fatal("invalid condition was accepted")
	}
}

func TestMailNotLoggedInSelectionScopeAndStatus(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, name := range []string{"outside", "pro", "inside", "removed", "dead", "registration-dead", "pipeline-dead", "checked-valid", "checked-invalid", "legacy-at", "legacy-rt"} {
		email := name + "@example.com"
		profile := model.MailAccountProfile{Email: email}
		switch name {
		case "pro":
			profile.ManagementScope = "pro"
		case "dead":
			profile.ChatGPTStatus = "dead"
		case "registration-dead":
			profile.RegistrationStatus = "dead"
		case "checked-valid", "checked-invalid":
			profile.ATCheckedAt, profile.ATValid = &now, name == "checked-valid"
		}
		if _, err := s.SaveMailAccount(profile, model.MailAccountCredentials{Email: email}); err != nil {
			t.Fatal(err)
		}
		if name == "legacy-at" || name == "legacy-rt" {
			authType := "AT"
			if name == "legacy-rt" {
				authType = "RT"
			}
			if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.auth_type',?) WHERE email=?`, authType, email); err != nil {
				t.Fatal(err)
			}
		}
		if name == "inside" || name == "removed" || name == "pipeline-dead" {
			account, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "fixture-at")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateFreeAccount(account.ID, func(p *model.FreeAccountProfile) {
				if name == "pipeline-dead" {
					p.Dead = true
				} else {
					p.AcceptStatus = "completed"
				}
				if name == "removed" {
					p.RemoveStatus = "completed"
				}
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	emails, err := s.SelectOutsideMailAccounts(MailAccountSelection{ATStatus: "not_logged_in"})
	if err != nil || len(emails) != 1 || emails[0] != "outside@example.com" {
		t.Fatalf("unexpected not-logged-in selection: %v, %v", emails, err)
	}
}
