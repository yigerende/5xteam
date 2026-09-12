package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestOutsideInvalidATMailSelectionReturnsOnlyIdentifiers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	const email = "invalid@example.com"
	profile, err := st.SaveMailAccount(model.MailAccountProfile{Email: email, ATCheckedAt: &now}, model.MailAccountCredentials{Email: email, AccessToken: "fixture-secret-at", GptPassword: "fixture-secret-password"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	r := httptest.NewRequest("GET", "/api/mail/accounts/invalid-at-outside", nil)
	w := httptest.NewRecorder()
	s.listOutsideInvalidATMailEmails(w, r)
	var payload struct {
		OK   bool `json:"ok"`
		Data struct {
			Emails []string `json:"emails"`
			Total  int      `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !payload.OK || payload.Data.Total != 1 || len(payload.Data.Emails) != 1 || payload.Data.Emails[0] != email {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
	for _, unwanted := range []string{"fixture-secret", "access_token", "gpt_password", "at_valid"} {
		if strings.Contains(w.Body.String(), unwanted) {
			t.Fatalf("selection response contains unnecessary account data: %s", unwanted)
		}
	}
	after, credentials, err := st.MailAccountCredential(email)
	if err != nil || !after.UpdatedAt.Equal(profile.UpdatedAt) || after.ATValid || credentials.AccessToken != "fixture-secret-at" {
		t.Fatal("selection must not modify credentials or AT state", err)
	}
}

func TestMailConditionSelectionScopesAndValidation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, email := range []string{"first@example.com", "second@example.com"} {
		if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, RefreshToken: "fixture-rt"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st}
	for _, tc := range []struct {
		name, body  string
		code, count int
	}{
		{"all", `{"scope":"all","require_rt":true}`, 200, 2},
		{"page", `{"scope":"page","require_rt":true,"page_emails":["FIRST@example.com"]}`, 200, 1},
		{"empty_page", `{"scope":"page","page_emails":[]}`, 200, 0},
		{"missing_page", `{"scope":"page"}`, 200, 0},
		{"combined", `{"scope":"all","require_rt":true,"require_password":true}`, 200, 0},
		{"not_logged_in_all", `{"scope":"all","at_status":"not_logged_in"}`, 200, 2},
		{"not_logged_in_page", `{"scope":"page","at_status":"not_logged_in","page_emails":["FIRST@example.com"]}`, 200, 1},
		{"not_logged_in_combined", `{"scope":"all","at_status":"not_logged_in","require_password":true}`, 200, 0},
		{"scope", `{"scope":"typo"}`, 400, 0},
		{"condition", `{"scope":"all","at_status":"typo"}`, 400, 0},
		{"bad_email", `{"scope":"page","page_emails":["bad"]}`, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/mail/accounts/select", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			s.selectMailAccounts(w, r)
			if w.Code != tc.code {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			if tc.code == 200 {
				var payload struct {
					Data struct {
						Emails []string `json:"emails"`
						Total  int      `json:"total"`
					} `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Data.Emails) != tc.count || payload.Data.Total != tc.count {
					t.Fatal(w.Body.String())
				}
			}
			if strings.Contains(w.Body.String(), "fixture-rt") {
				t.Fatal("selection leaked a token")
			}
		})
	}
}
