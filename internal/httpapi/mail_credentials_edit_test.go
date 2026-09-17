package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestMailCredentialsEditPersistsAndExports(t *testing.T) {
	for _, scope := range []string{"mail", "pro"} {
		t.Run(scope, func(t *testing.T) {
			dir := t.TempDir()
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { st.Close() }()
			const email = "edit@example.com"
			_, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "keep-label", Group: "keep-group"}, model.MailAccountCredentials{Email: email, MailPassword: "keep-mail-password", PickupURL: "https://example.com/pickup", IDToken: "keep-id-token"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.UpdateMailAccountManagementScope(email, scope); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st}
			read := func(format string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest("GET", "/credentials?format="+format, nil)
				r.SetPathValue("email", email)
				w := httptest.NewRecorder()
				s.exportMailAccountCredentials(w, r)
				return w
			}
			var view struct {
				Data map[string]any `json:"data"`
			}
			if err = json.Unmarshal(read("raw").Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"revision": view.Data["revision"], "gpt_password": "new-password", "totp_secret": "JBSWY3DPEHPK3PXP", "access_token": "new-at", "refresh_token": "new-rt", "chatgpt_session": `{"accessToken":"new-at","user":{"email":"edit@example.com"}}`, "chatgpt_account_id": "new-account-id"}
			patch := func(values map[string]any) *httptest.ResponseRecorder {
				t.Helper()
				encoded, _ := json.Marshal(values)
				r := httptest.NewRequest("PATCH", "/credentials", strings.NewReader(string(encoded)))
				r.SetPathValue("email", email)
				w := httptest.NewRecorder()
				s.updateMailAccountCredentials(w, r)
				return w
			}
			w := patch(body)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
			}
			for _, secret := range []string{"new-password", "new-at", "new-rt", "JBSWY3DPEHPK3PXP"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("save response leaked a credential")
				}
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.store = st
			p, c, err := st.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			if p.ManagementScope != scope || p.Label != "keep-label" || p.Group != "keep-group" || c.MailPassword != "keep-mail-password" || c.IDToken != "keep-id-token" || c.PickupURL != "https://example.com/pickup" {
				t.Fatal("save changed unrelated fields")
			}
			if c.GptPassword != "new-password" || c.TotpSecret != "JBSWY3DPEHPK3PXP" || c.AccessToken != "new-at" || c.RefreshToken != "new-rt" || p.OAuthAccountID != "new-account-id" || c.ChatGPTSession != body["chatgpt_session"] {
				t.Fatal("edited credentials did not persist")
			}
			for _, format := range []string{"raw", "cpa", "sub2"} {
				w = read(format)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "new-at") || !strings.Contains(w.Body.String(), "new-rt") || !strings.Contains(w.Body.String(), "new-account-id") {
					t.Fatalf("saved export %s failed: %s", format, w.Body.String())
				}
			}
			if w = patch(body); w.Code != 409 {
				t.Fatal("stale save must be rejected")
			}
			p, c, _ = st.MailAccountCredential(email)
			if w = patch(map[string]any{"revision": st.MailCredentialsRevision(p, c), "chatgpt_session": `{"accessToken":"session-only-at"}`}); w.Code != 200 {
				t.Fatal("Session-only save failed", w.Body.String())
			}
			_, c, _ = st.MailAccountCredential(email)
			if c.AccessToken != "session-only-at" {
				t.Fatal("Session AT was not extracted")
			}
			before, beforeC, _ := st.MailAccountCredential(email)
			for _, invalid := range []map[string]any{{"totp_secret": "not!base32"}, {"chatgpt_session": "not-json"}, {"chatgpt_session": "null"}, {"chatgpt_session": "[]"}} {
				invalid["revision"] = st.MailCredentialsRevision(before, beforeC)
				if w = patch(invalid); w.Code != 400 {
					t.Fatalf("invalid credentials accepted: %s", w.Body.String())
				}
				p, c, _ = st.MailAccountCredential(email)
				if !reflect.DeepEqual(p, before) || !reflect.DeepEqual(c, beforeC) {
					t.Fatal("invalid save mutated account")
				}
			}
			clear := map[string]any{"revision": st.MailCredentialsRevision(before, beforeC), "refresh_token": "", "gpt_password": "", "totp_secret": "", "chatgpt_session": ""}
			if w = patch(clear); w.Code != 200 {
				t.Fatal("clear failed", w.Body.String())
			}
			p, c, _ = st.MailAccountCredential(email)
			if c.RefreshToken != "" || c.GptPassword != "" || c.TotpSecret != "" || c.ChatGPTSession != "" || !p.RefreshTokenEdited || p.RefreshTokenPresent || p.GptPasswordPresent || p.TotpSecretPresent {
				t.Fatal("cleared fields retained old values")
			}
			if w = read("cpa"); w.Code != 409 {
				t.Fatal("export with cleared RT must fail")
			}
			legacy, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: "legacy-user"}, "legacy-source-at")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.SaveFreeAccountOAuth(legacy.ID, "legacy-at", "legacy-rt", "legacy-team"); err != nil {
				t.Fatal(err)
			}
			list := httptest.NewRecorder()
			s.listMailAccounts(list, httptest.NewRequest("GET", "/api/mail/accounts?page=1&page_size=10", nil))
			if list.Code != 200 {
				t.Fatal("mail list failed")
			}
			_, c, _ = st.MailAccountCredential(email)
			if c.RefreshToken != "" {
				t.Fatal("list restored explicitly cleared RT from old Team data")
			}
			if w = read("cpa"); w.Code != 409 {
				t.Fatal("export restored explicitly cleared RT from old Team data")
			}
		})
	}
}

func TestMailCredentialsEditRejectsBackgroundOAuthOverwrite(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveMailAccount(model.MailAccountProfile{Email: "concurrent@example.com"}, model.MailAccountCredentials{Email: "concurrent@example.com", AccessToken: "old-at", RefreshToken: "old-rt"})
	if err != nil {
		t.Fatal(err)
	}
	p, c, _ := st.MailAccountCredential(p.Email)
	revision := st.MailCredentialsRevision(p, c)
	if err = st.SaveMailAccountOAuth(p.Email, "rotated-at", "rotated-rt"); err != nil {
		t.Fatal(err)
	}
	password := "new-password"
	if _, err = st.PatchMailCredentials(p.Email, store.MailCredentialsPatch{Revision: revision, GPTPassword: &password}); err != store.ErrMailCredentialsChanged {
		t.Fatal("concurrent OAuth overwrite not blocked", err)
	}
	_, c, _ = st.MailAccountCredential(p.Email)
	if c.AccessToken != "rotated-at" || c.RefreshToken != "rotated-rt" || c.GptPassword != "" {
		t.Fatal("stale edit overwrote OAuth")
	}
}
