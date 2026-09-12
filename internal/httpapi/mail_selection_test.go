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
