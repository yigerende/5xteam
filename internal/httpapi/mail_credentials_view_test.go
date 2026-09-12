package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestMailCredentialsViewIncludesTOTPWithoutRequiringAT(t *testing.T) {
	for _, tc := range []struct{ name, at, rt, totp string }{
		{"all_credentials", "fixture-at", "fixture-rt", "fixture-totp-secret"},
		{"totp_without_at", "", "", "fixture-totp-secret"},
		{"rt_without_at", "", "fixture-rt", "fixture-totp-secret"},
		{"no_totp", "fixture-at", "fixture-rt", ""},
		{"password_only", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			profile, err := st.SaveMailAccount(model.MailAccountProfile{Email: "fixture@example.com"}, model.MailAccountCredentials{
				Email: "fixture@example.com", GptPassword: "fixture-gpt-password", TotpSecret: tc.totp,
				AccessToken: tc.at, RefreshToken: tc.rt, MailPassword: "mail-private", PickupURL: "https://example.com/mail-private",
			})
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st}
			for _, format := range []string{"", "raw"} {
				r := httptest.NewRequest("GET", "/credentials?format="+format, nil)
				r.SetPathValue("email", profile.Email)
				w := httptest.NewRecorder()
				s.exportMailAccountCredentials(w, r)
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("unexpected response: HTTP %d", w.Code)
				}
				var out struct{ Data map[string]any }
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				for key, expected := range map[string]string{"email": profile.Email, "gpt_password": "fixture-gpt-password", "totp_secret": tc.totp, "access_token": tc.at, "refresh_token": tc.rt} {
					if out.Data[key] != expected {
						t.Fatalf("incorrect credential field %s", key)
					}
				}
				if strings.Contains(w.Body.String(), "mail-private") {
					t.Fatal("view exposed unrelated mail credentials")
				}
			}
			if tc.at != "" && tc.rt != "" {
				for _, format := range []string{"cpa", "sub2"} {
					r := httptest.NewRequest("GET", "/credentials?format="+format, nil)
					r.SetPathValue("email", profile.Email)
					w := httptest.NewRecorder()
					s.exportMailAccountCredentials(w, r)
					if w.Code != 200 || strings.Contains(w.Body.String(), "totp_secret") || strings.Contains(w.Body.String(), "fixture-totp-secret") {
						t.Fatalf("%s export changed or includes TOTP", format)
					}
				}
			}
			after, _, err := st.MailAccountCredential(profile.Email)
			if err != nil || !after.UpdatedAt.Equal(profile.UpdatedAt) {
				t.Fatal("view modified the stored account")
			}
			r := httptest.NewRequest("GET", "/credentials", nil)
			r.SetPathValue("email", "missing@example.com")
			w := httptest.NewRecorder()
			s.exportMailAccountCredentials(w, r)
			if w.Code != 400 || strings.Contains(w.Body.String(), "fixture-") {
				t.Fatal("missing account did not return a safe error")
			}
		})
	}
}
