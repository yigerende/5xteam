package store

import (
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProCredentialsLoginAndOAuthSurviveReopen(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "manual"
		if automatic {
			name = "automatic"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			const email = "credential-pro@example.com"
			p, err := s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, MailPassword: "mail-secret", GptPassword: "gpt-secret", TotpSecret: "totp-secret", RefreshToken: "prior-rt"})
			if err != nil {
				t.Fatal(err)
			}
			s.UpdateMailAccountManagementScope(email, "pro")
			// Manual login and automatic initial login both persist this bundle.
			p, c, err := s.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			const initialSession = `{"accessToken":"first-at","session_token":"first-cookie","user":{"email":"credential-pro@example.com"}}`
			c.AccessToken, c.ChatGPTSession = "first-at", initialSession
			if _, err = s.SaveMailAccount(p, c); err != nil {
				t.Fatal(err)
			}
			reopen := func() {
				t.Helper()
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			reopen()
			p, c, err = s.MailAccountCredential(email)
			if err != nil || c.AccessToken != "first-at" || c.ChatGPTSession != initialSession || !p.ChatGPTSessionPresent || c.RefreshToken != "prior-rt" {
				t.Fatal("first login did not survive restart")
			}
			wantSession := initialSession
			if automatic {
				wantSession = `{"accessToken":"paid-web-at","session_token":"paid-cookie"}`
				err = s.SaveMailAccountOAuthSessionBundle(email, "paid-oauth-at", "paid-rt", "id-token", "account", "user", time.Now().Add(time.Hour), wantSession)
			} else {
				err = s.SaveMailAccountOAuth(email, "paid-oauth-at", "paid-rt")
			}
			if err != nil {
				t.Fatal(err)
			}
			reopen()
			p, c, err = s.MailAccountCredential(email)
			if err != nil || c.AccessToken != "paid-oauth-at" || c.RefreshToken != "paid-rt" || c.ChatGPTSession != wantSession || !p.AccessTokenPresent || !p.RefreshTokenPresent {
				t.Fatal("post-purchase credentials did not survive restart")
			}
			if p.Email != email || p.ManagementScope != "pro" || c.GptPassword != "gpt-secret" || c.TotpSecret != "totp-secret" || c.MailPassword != "mail-secret" {
				t.Fatal("credential save changed account or login credentials")
			}
			var sealed string
			if err = s.db.QueryRow("SELECT encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"paid-oauth-at", "paid-rt", "session_token", "gpt-secret"} {
				if strings.Contains(sealed, secret) {
					t.Fatal("credentials saved unencrypted")
				}
			}
		})
	}
}

func TestProOAuthSessionBundleWriteIsAtomic(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const email = "atomic-pro@example.com"
	const originalSession = `{"accessToken":"first-at","session_token":"first-cookie"}`
	s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "first-at", RefreshToken: "old-rt", ChatGPTSession: originalSession})
	if _, err = s.db.Exec(`CREATE TRIGGER reject_credentials BEFORE UPDATE OF encrypted_credentials ON mail_accounts BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	err = s.SaveMailAccountOAuthSessionBundle(email, "new-at", "new-rt", "", "", "", time.Time{}, `{"accessToken":"new-web-at"}`)
	if err == nil {
		t.Fatal("expected failed write")
	}
	_, c, err := s.MailAccountCredential(email)
	if err != nil || c.AccessToken != "first-at" || c.RefreshToken != "old-rt" || c.ChatGPTSession != originalSession {
		t.Fatal("failed write partially updated credentials")
	}
	if _, err = s.db.Exec("DROP TRIGGER reject_credentials"); err != nil {
		t.Fatal(err)
	}
	err = s.SaveMailAccountOAuthSessionBundle(email, "new-at", "new-rt", "", "", "", time.Time{}, "invalid-session")
	if err == nil {
		t.Fatal("invalid Session accepted")
	}
	err = s.SaveMailAccountOAuthSessionBundle(email, "new-at", "new-rt", "", "", "", time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, c, err = s.MailAccountCredential(email)
	if err != nil || c.AccessToken != "new-at" || c.RefreshToken != "new-rt" || c.ChatGPTSession != originalSession {
		t.Fatal("missing new Session erased saved Session")
	}
}
