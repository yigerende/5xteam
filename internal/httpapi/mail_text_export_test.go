package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestMailAccountTextFormatsAndTokenOptions(t *testing.T) {
	for _, tc := range []struct{ name, gpt, totp, mail, link, base string }{
		{"password_totp", "gpt", "totp", "mail", "https://mail.example/code", "a@example.com----gpt----totp"},
		{"password_without_totp", "gpt", "", "mail", "https://mail.example/code", "a@example.com----mail----https://mail.example/code"},
		{"totp_without_password", "", "totp", "mail", "https://mail.example/code", "a@example.com----mail----https://mail.example/code"},
		{"mail_credentials", "", "", "mail", "https://mail.example/code", "a@example.com----mail----https://mail.example/code"},
		{"password_link_fallback", "gpt", "", "", "https://mail.example/code", "a@example.com----https://mail.example/code"},
		{"link_only", "", "", "", "https://mail.example/code", "a@example.com----https://mail.example/code"},
		{"missing_link", "gpt", "", "mail", "", "a@example.com----mail----"},
		{"no_credentials", "", "", "", "", "a@example.com----"},
	} {
		for mask := 0; mask < 4; mask++ {
			t.Run(fmt.Sprintf("%s-%d", tc.name, mask), func(t *testing.T) {
				c := model.MailAccountCredentials{Email: "a@example.com", GptPassword: tc.gpt, TotpSecret: tc.totp, MailPassword: tc.mail, PickupURL: tc.link, AccessToken: "AT", RefreshToken: "RT"}
				want := tc.base
				if mask&1 != 0 {
					want += "----AT"
				}
				if mask&2 != 0 {
					want += "----RT"
				}
				got, err := mailAccountTextLine(c, mask&1 != 0, mask&2 != 0)
				if err != nil || got != want {
					t.Fatalf("got=%q want=%q err=%v", got, want, err)
				}
			})
		}
	}
	got, err := mailAccountTextLine(model.MailAccountCredentials{Email: "a@example.com", PickupURL: "https://mail.example/code", RefreshToken: "RT"}, true, true)
	if err != nil || got != "a@example.com----https://mail.example/code----RT" {
		t.Fatalf("missing AT must be skipped: %q %v", got, err)
	}
	for _, value := range []string{"bad\nsecret", "bad\rsecret", "bad----secret"} {
		_, err := mailAccountTextLine(model.MailAccountCredentials{Email: "a@example.com", MailPassword: value}, false, false)
		if err == nil || strings.Contains(err.Error(), value) {
			t.Fatal("malformed credential was not rejected safely")
		}
	}
}

func TestMailAccountTextSkipsAbsentTokens(t *testing.T) {
	for _, withTOTP := range []bool{false, true} {
		for atIndex, at := range []string{"", " \t ", "AT"} {
			for rtIndex, rt := range []string{"", " \t ", "RT"} {
				for mask := 0; mask < 4; mask++ {
					t.Run(fmt.Sprintf("totp-%t-at-%d-rt-%d-options-%d", withTOTP, atIndex, rtIndex, mask), func(t *testing.T) {
						credentials := model.MailAccountCredentials{Email: "a@example.com", MailPassword: "mail", PickupURL: "https://mail.example/code", AccessToken: at, RefreshToken: rt}
						want := "a@example.com----mail----https://mail.example/code"
						if withTOTP {
							credentials.GptPassword, credentials.TotpSecret = "gpt", "totp"
							want = "a@example.com----gpt----totp"
						}
						if mask&1 != 0 && atIndex == 2 {
							want += "----AT"
						}
						if mask&2 != 0 && rtIndex == 2 {
							want += "----RT"
						}
						got, err := mailAccountTextLine(credentials, mask&1 != 0, mask&2 != 0)
						if err != nil || got != want {
							t.Fatalf("got=%q want=%q err=%v", got, want, err)
						}
					})
				}
			}
		}
	}
}

func TestMailTextExportDoesNotRequireTokensOrModifyCredentials(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	before, err := st.SaveMailAccount(model.MailAccountProfile{Email: "a@example.com"}, model.MailAccountCredentials{Email: "a@example.com", GptPassword: "unused-gpt-password", MailPassword: "mail-password", PickupURL: "https://mail.example/code"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	r := httptest.NewRequest("POST", "/api/mail/accounts/credentials/export-batch", strings.NewReader(`{"emails":["a@example.com"],"format":"text","include_at":true,"include_rt":true}`))
	w := httptest.NewRecorder()
	s.exportMailAccountCredentialsBatch(w, r)
	if w.Code != 200 || w.Body.String() != "a@example.com----mail-password----https://mail.example/code\r\n" {
		t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Export-Missing-AT") != "1" || w.Header().Get("X-Export-Missing-RT") != "1" || w.Header().Get("X-Export-Count") != "1" {
		t.Fatal(w.Header())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(w.Header().Get("Content-Disposition"), ".txt") {
		t.Fatal(w.Header())
	}
	after, credentials, err := st.MailAccountCredential("a@example.com")
	if err != nil || !before.UpdatedAt.Equal(after.UpdatedAt) || credentials.AccessToken != "" || credentials.RefreshToken != "" {
		t.Fatal("export modified credentials")
	}
	for _, badEmail := range []string{"missing@example.com", "pro@example.com", "malformed@example.com"} {
		if badEmail != "missing@example.com" {
			profile := model.MailAccountProfile{Email: badEmail}
			secret := model.MailAccountCredentials{Email: badEmail}
			if badEmail == "pro@example.com" {
				profile.ManagementScope = "pro"
			} else {
				secret.MailPassword = "must-not-leak\nsecond-line"
			}
			if _, err := st.SaveMailAccount(profile, secret); err != nil {
				t.Fatal(err)
			}
		}
		payload, _ := json.Marshal(batchMailCredentialExportInput{Emails: []string{"a@example.com", badEmail}, Format: "text"})
		w = httptest.NewRecorder()
		s.exportMailAccountCredentialsBatch(w, httptest.NewRequest("POST", "/export", bytes.NewReader(payload)))
		if w.Code != 409 || strings.Contains(w.Body.String(), "must-not-leak") || strings.Contains(w.Body.String(), "mail-password") {
			t.Fatalf("partial/unsafe export: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestAllBatchExportFormatsSupportMoreThan500Accounts(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	emails := make([]string, 501)
	for i := range emails {
		emails[i] = fmt.Sprintf("account-%03d@example.com", i)
		if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: emails[i]}, model.MailAccountCredentials{Email: emails[i], AccessToken: turbIntegrationTestToken(emails[i]), RefreshToken: "fixture-rt", PickupURL: "https://mail.example/code"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st}
	for _, format := range []string{"text", "cpa", "sub2"} {
		t.Run(format, func(t *testing.T) {
			payload, _ := json.Marshal(batchMailCredentialExportInput{Emails: emails, Format: format})
			w := httptest.NewRecorder()
			s.exportMailAccountCredentialsBatch(w, httptest.NewRequest("POST", "/export", bytes.NewReader(payload)))
			if w.Code != 200 {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			switch format {
			case "text":
				if strings.Count(w.Body.String(), "\r\n") != 501 {
					t.Fatal("text export truncated")
				}
			case "cpa":
				archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
				if err != nil || len(archive.File) != 501 {
					t.Fatal("CPA export truncated", err)
				}
			case "sub2":
				var out struct {
					Accounts []json.RawMessage `json:"accounts"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Accounts) != 501 {
					t.Fatal("Sub2 export truncated", err)
				}
			}
		})
	}
}
