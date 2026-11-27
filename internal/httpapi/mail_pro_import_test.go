package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestMailImportDirectProOwnershipAndReturn(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &Server{store: s}
	for _, email := range []string{"existing@example.com", "unselected@example.com"} {
		if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "original-at"}); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"enter_pro":true,"group":"chosen","accounts":[{"email":" NEW@example.com ","gpt_password":"password-fixture","totp_secret":"totp-fixture","pickup_url":"https://mail.example/private-pickup","access_token":"at-fixture"},{"email":"existing@example.com","access_token":"updated-at"},{"email":"invalid"}]}`
	response := httptest.NewRecorder()
	server.importMailAccounts(response, httptest.NewRequest(http.MethodPost, "/api/mail/accounts/import", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Imported, Updated, Skipped int
			EnteredPro                 int `json:"entered_pro"`
		}
	}
	if err = json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Imported != 1 || envelope.Data.Updated != 1 || envelope.Data.Skipped != 1 || envelope.Data.EnteredPro != 2 {
		t.Fatalf("unexpected counts: %+v", envelope.Data)
	}
	if strings.Contains(response.Body.String(), "at-fixture") || strings.Contains(response.Body.String(), "password-fixture") || strings.Contains(response.Body.String(), "private-pickup") {
		t.Fatal("response leaked credentials")
	}
	mail := s.MailAccountsByManagementScope("mail")
	if len(mail) != 1 || mail[0].Email != "unselected@example.com" {
		t.Fatalf("mail scope = %+v", mail)
	}
	if got := s.MailAccountsByManagementScope("pro"); len(got) != 2 {
		t.Fatalf("pro count=%d", len(got))
	}
	p, credentials, err := s.MailAccountCredential("new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if p.ManagementScope != "pro" || p.ProManagedAt == nil || p.Group != "chosen" || p.ProAuto.ID != "" || p.ProWorkflowRunning {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if credentials.AccessToken != "at-fixture" || credentials.GptPassword != "password-fixture" || credentials.TotpSecret != "totp-fixture" || credentials.PickupURL != "https://mail.example/private-pickup" {
		t.Fatal("import did not preserve supplied credentials")
	}
	if len(s.FreeAccounts()) != 0 {
		t.Fatal("import must not create Team pipeline entries")
	}
	firstID, managedAt := p.ID, *p.ProManagedAt
	response = httptest.NewRecorder()
	server.importMailAccounts(response, httptest.NewRequest(http.MethodPost, "/api/mail/accounts/import", strings.NewReader(body)))
	p, _, err = s.MailAccountCredential("new@example.com")
	if err != nil || response.Code != http.StatusOK || p.ID != firstID || !p.ProManagedAt.Equal(managedAt) || len(s.MailAccounts()) != 3 {
		t.Fatal("retry duplicated or reset the account")
	}
	request := httptest.NewRequest(http.MethodPut, "/api/mail/accounts/new@example.com/management-scope", strings.NewReader(`{"scope":"mail"}`))
	request.SetPathValue("email", "new@example.com")
	response = httptest.NewRecorder()
	server.updateMailAccountManagementScope(response, request)
	p, after, err := s.MailAccountCredential("new@example.com")
	if err != nil || response.Code != http.StatusOK || p.ManagementScope != "mail" || p.ProManagedAt != nil || p.ID != firstID || after != credentials {
		t.Fatal("return changed identity or credentials")
	}
}

func TestMailImportWithoutProOptionKeepsMailScope(t *testing.T) {
	for _, option := range []string{"", `,"enter_pro":false`} {
		t.Run(option, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			server := &Server{store: s}
			r := httptest.NewRecorder()
			server.importMailAccounts(r, httptest.NewRequest(http.MethodPost, "/api/mail/accounts/import", strings.NewReader(`{"accounts":[{"email":"mail@example.com"}]`+option+`}`)))
			if r.Code != http.StatusOK || len(s.MailAccountsByManagementScope("mail")) != 1 || len(s.MailAccountsByManagementScope("pro")) != 0 {
				t.Fatalf("default import changed: %s", r.Body.String())
			}
		})
	}
}

func TestMailExistingScopeEndpointIsIdempotentAndKeepsCredentials(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := model.MailAccountCredentials{Email: "batch@example.com", MailPassword: "mail-pwd", GptPassword: "gpt-pwd", TotpSecret: "totp", AccessToken: "at", RefreshToken: "rt", ChatGPTSession: `{"accessToken":"at"}`}
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: c.Email}, c); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: s}
	move := func(scope string) model.MailAccountProfile {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, "/api/mail/accounts/batch@example.com/management-scope", strings.NewReader(`{"scope":"`+scope+`"}`))
		r.SetPathValue("email", c.Email)
		w := httptest.NewRecorder()
		server.updateMailAccountManagementScope(w, r)
		p, got, err := s.MailAccountCredential(c.Email)
		if err != nil || w.Code != http.StatusOK || got != c {
			t.Fatal("ownership move changed credentials")
		}
		return p
	}
	first, second := move("pro"), move("pro")
	if first.ID != second.ID || !first.ProManagedAt.Equal(*second.ProManagedAt) {
		t.Fatal("repeated move is not idempotent")
	}
	if len(s.FreeAccounts()) != 0 || second.ProAuto.ID != "" {
		t.Fatal("move started a task")
	}
	returned := move("mail")
	if returned.ManagementScope != "mail" || returned.ProManagedAt != nil {
		t.Fatal("return failed")
	}
}
