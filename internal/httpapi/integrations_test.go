package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/store"
)

func turbIntegrationTestToken(email string) string {
	claims := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_id":    "user-session",
			"chatgpt_account_id": "account-session",
			"chatgpt_plan_type":  "free",
		},
		"https://api.openai.com/profile": map[string]any{
			"email": email,
			"name":  "Session User",
		},
	}
	payload, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestTurbIntegrationSavesCompleteSessionWithoutTeamRotation(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	const email = "session-import@example.com"
	token := turbIntegrationTestToken(email)
	body, _ := json.Marshal(map[string]any{
		"email":        email,
		"access_token": token,
		"totp_secret":  "JBSWY3DPEHPK3PXP",
		"chatgpt_session": map[string]any{
			"accessToken": token,
			"expires":     "2026-12-01T00:00:00.000Z",
			"user":        map[string]any{"email": email, "id": "user-session"},
			"account":     map[string]any{"id": "account-session", "planType": "free"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/turb/register", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server := &Server{store: dataStore}
	server.importTurbRegistration(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	profile, credentials, err := dataStore.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.ChatGPTSessionPresent || !strings.Contains(credentials.ChatGPTSession, `"accessToken"`) {
		t.Fatalf("complete session was not saved: profile=%+v session=%q", profile, credentials.ChatGPTSession)
	}
	if !profile.TotpSecretPresent || credentials.TotpSecret != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("totp secret was not saved: profile=%+v secret=%q", profile, credentials.TotpSecret)
	}
	if accounts := dataStore.FreeAccounts(); len(accounts) != 0 {
		t.Fatalf("pure turb push entered Team rotation: %#v", accounts)
	}

	// A later push without 2FA must preserve the existing secret.
	body, _ = json.Marshal(map[string]any{"email": email, "access_token": token})
	req = httptest.NewRequest(http.MethodPost, "/api/integrations/turb/register", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	server.importTurbRegistration(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second push status=%d body=%s", rec.Code, rec.Body.String())
	}
	_, credentials, err = dataStore.MailAccountCredential(email)
	if err != nil || credentials.TotpSecret != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("push without 2FA erased stored secret: credentials=%+v err=%v", credentials, err)
	}

	// A manual "push to Space" with newer credentials updates the same
	// mailbox record instead of creating a duplicate or a Team rotation row.
	body, _ = json.Marshal(map[string]any{
		"email": email, "access_token": token,
		"gpt_password": "new-chatgpt-password",
		"totp_secret":  "NEW-TOTP-SECRET",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/integrations/turb/register", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	server.importTurbRegistration(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update push status=%d body=%s", rec.Code, rec.Body.String())
	}
	profile, credentials, err = dataStore.MailAccountCredential(email)
	if err != nil || credentials.GptPassword != "new-chatgpt-password" || credentials.TotpSecret != "NEW-TOTP-SECRET" {
		t.Fatalf("manual push did not update password/2FA: profile=%+v credentials=%+v err=%v", profile, credentials, err)
	}
	if accounts := dataStore.FreeAccounts(); len(accounts) != 0 {
		t.Fatalf("manual update push unexpectedly entered Team rotation: %#v", accounts)
	}
}
