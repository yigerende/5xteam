package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestRedactMailPayloadRemovesSecretsRecursively(t *testing.T) {
	payload := map[string]any{
		"email":         "person@example.com",
		"mail_password": "mail-secret",
		"pickup_url":    "https://mail.example/pickup?token=pickup-secret",
		"chatgpt_session": map[string]any{
			"accessToken": "full-session-secret",
		},
		"nested": map[string]any{
			"client_id":          "client-secret",
			"mail_refresh_token": "mail-rt-secret",
			"gpt_password":       "gpt-secret",
			"totp_secret":        "totp-secret",
		},
		"items": []any{map[string]any{
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"id_token":      "id-secret",
			"session_token": "session-secret",
		}},
	}

	redactMailPayload(payload)

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal redacted payload: %v", err)
	}
	text := string(encoded)
	for _, secret := range []string{
		"mail-secret", "pickup-secret", "client-secret", "mail-rt-secret", "gpt-secret",
		"totp-secret", "at-secret", "rt-secret", "id-secret", "session-secret",
		"full-session-secret",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted payload still contains secret %q: %s", secret, text)
		}
	}
	for _, field := range mailSecretFields {
		if strings.Contains(text, `"`+field+`"`) {
			t.Fatalf("redacted payload still contains field %q: %s", field, text)
		}
		if !strings.Contains(text, `"`+field+`_present":true`) {
			t.Fatalf("redacted payload is missing presence marker for %q: %s", field, text)
		}
	}
}

func TestRedactMailPayloadMarksEmptySecretAsMissing(t *testing.T) {
	payload := map[string]any{"pickup_url": "  "}

	redactMailPayload(payload)

	if _, exists := payload["pickup_url"]; exists {
		t.Fatal("pickup_url was not removed")
	}
	if present, ok := payload["pickup_url_present"].(bool); !ok || present {
		t.Fatalf("pickup_url_present = %#v, want false", payload["pickup_url_present"])
	}
}

func TestRedactMailPayloadMarksObjectSessionPresent(t *testing.T) {
	payload := map[string]any{
		"chatgpt_session": map[string]any{"accessToken": "session-at"},
	}

	redactMailPayload(payload)

	if _, exists := payload["chatgpt_session"]; exists {
		t.Fatal("chatgpt_session was not removed")
	}
	if present, ok := payload["chatgpt_session_present"].(bool); !ok || !present {
		t.Fatalf("chatgpt_session_present = %#v, want true", payload["chatgpt_session_present"])
	}
}

func TestNormalizeAndReadChatGPTSession(t *testing.T) {
	raw := json.RawMessage(`{"accessToken":"at","user":{"email":"session@example.com"}}`)
	stored, err := normalizeChatGPTSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := chatGPTSessionValue(stored).(map[string]any)
	if !ok || value["accessToken"] != "at" {
		t.Fatalf("unexpected session value: %#v", value)
	}
}

func TestMailAccountLoginInputDisablesPhoneVerification(t *testing.T) {
	input := mailAccountLoginInput("free@example.com", "http://proxy.example:8080")

	if skip, ok := input["skip_phone_verification"].(bool); !ok || !skip {
		t.Fatalf("skip_phone_verification = %#v, want true", input["skip_phone_verification"])
	}
	if proxy, _ := input["proxy_url"].(string); proxy != "http://proxy.example:8080" {
		t.Fatalf("proxy_url = %q, want configured global proxy", proxy)
	}
	if mode, _ := input["credential_mode"].(string); mode != "chatgpt_at" {
		t.Fatalf("credential_mode = %q, want chatgpt_at", mode)
	}
}

func TestExtractOTPHandlesNestedMessagesAndHTMLNoise(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{"messages": []any{
			map[string]any{"verificationCode": "111111", "receivedAt": "2026-09-06T00:50:00Z"},
			map[string]any{"subject": "Your temporary ChatGPT login code", "body": `<style>.x{color:#202123}</style><p>Your code is&nbsp;556097</p>`, "receivedAt": "2026-09-06T00:51:00Z"},
		}},
	}
	if got := extractOTP(payload, ""); got != "556097" {
		t.Fatalf("extractOTP = %q, want newest/contextual code", got)
	}
}

func TestExtractOTPHandlesFieldAliasesAndFallbackText(t *testing.T) {
	payload := map[string]any{"emailCode": "654321", "bodyPreview": "Your verification code is 111111"}
	if got := extractOTP(payload, ""); got != "654321" {
		t.Fatalf("extractOTP direct alias = %q, want 654321", got)
	}
	if got := extractOTP(nil, "<div>验证码：789012</div>"); got != "789012" {
		t.Fatalf("extractOTP raw HTML = %q, want 789012", got)
	}
}

func TestMailAccountRegistrationInputIsRegistrationOnly(t *testing.T) {
	input := mailAccountRegistrationInput("free@example.com", "http://proxy.example:8080")

	if mode, _ := input["mode"].(string); mode != "signup" {
		t.Fatalf("mode = %q, want signup", mode)
	}
	if credentialMode, _ := input["credential_mode"].(string); credentialMode != "register_only" {
		t.Fatalf("credential_mode = %q, want register_only", credentialMode)
	}
	if loginOnly, ok := input["login_only"].(bool); !ok || !loginOnly {
		t.Fatalf("login_only = %#v, want true", input["login_only"])
	}
	if skip, ok := input["skip_phone_verification"].(bool); !ok || !skip {
		t.Fatalf("skip_phone_verification = %#v, want true", input["skip_phone_verification"])
	}
	if proxy, _ := input["proxy_url"].(string); proxy != "http://proxy.example:8080" {
		t.Fatalf("proxy_url = %q, want configured global proxy", proxy)
	}
	for _, forbidden := range []string{"force_email_code", "email_code_login", "password"} {
		if _, exists := input[forbidden]; exists {
			t.Fatalf("registration input must not contain %q", forbidden)
		}
	}
}

func TestMailAccountRegistrationWithATInputStopsBeforeCodexOAuth(t *testing.T) {
	input := mailAccountRegistrationWithATInput("free@example.com", "http://proxy.example:8080")

	if mode, _ := input["mode"].(string); mode != "signup_at" {
		t.Fatalf("mode = %q, want signup_at", mode)
	}
	if credentialMode, _ := input["credential_mode"].(string); credentialMode != "chatgpt_at" {
		t.Fatalf("credential_mode = %q, want chatgpt_at", credentialMode)
	}
	if skip, ok := input["skip_phone_verification"].(bool); !ok || !skip {
		t.Fatalf("skip_phone_verification = %#v, want true", input["skip_phone_verification"])
	}
	if proxy, _ := input["proxy_url"].(string); proxy != "http://proxy.example:8080" {
		t.Fatalf("proxy_url = %q, want configured global proxy", proxy)
	}
}

func TestCodexOAuthLoginInputUsesSeparateCredentialMode(t *testing.T) {
	input := codexOAuthLoginInput("free@example.com", "http://proxy.example:8080", "hero_sms", true)

	if mode, _ := input["credential_mode"].(string); mode != "codex_oauth" {
		t.Fatalf("credential_mode = %q, want codex_oauth", mode)
	}
	if skip, ok := input["skip_phone_verification"].(bool); !ok || skip {
		t.Fatalf("skip_phone_verification = %#v, want false", input["skip_phone_verification"])
	}
	if allow, ok := input["allow_sms"].(bool); !ok || !allow {
		t.Fatalf("allow_sms = %#v, want true", input["allow_sms"])
	}
	if provider, _ := input["sms_realtime_provider"].(string); provider != "hero_sms" {
		t.Fatalf("sms_realtime_provider = %q, want hero_sms", provider)
	}
	if loginOnly, ok := input["login_only"].(bool); !ok || !loginOnly {
		t.Fatalf("login_only = %#v, want true", input["login_only"])
	}
}

func TestCodexOAuthLoginInputCanDisablePhoneFallback(t *testing.T) {
	input := codexOAuthLoginInput("free@example.com", "http://proxy.example:8080", "hero_sms", false)
	if allow, ok := input["allow_sms"].(bool); !ok || allow {
		t.Fatalf("allow_sms = %#v, want false", input["allow_sms"])
	}
}

func TestMailOAuthCompletionSavesTokensWithoutTeamAccount(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "mail-only@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{
		Email: email, PickupURL: "https://mail.example/pickup", AccessToken: "old-at", RefreshToken: "old-rt",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore, oauthJobs: map[string]map[string]any{
		"mail-job": {"job_id": "mail-job", "email": email, "status": "running"},
	}}
	server.finishMailAccountOAuthJob("mail-job", email, map[string]any{
		"success": true, "access_token": "new-at", "refresh_token": "new-rt",
	}, nil)
	_, credentials, err := dataStore.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "new-at" || credentials.RefreshToken != "new-rt" {
		t.Fatalf("mail OAuth credentials were not saved: at=%q rt=%q", credentials.AccessToken, credentials.RefreshToken)
	}
	if accounts := dataStore.FreeAccounts(); len(accounts) != 0 {
		t.Fatalf("mail OAuth must not create a Team account: %#v", accounts)
	}
	if status := server.oauthJobs["mail-job"]["status"]; status != "success" {
		t.Fatalf("job status = %#v, want success", status)
	}
}

func TestMailOAuthDeadResultMarksMailboxWithoutTeamRemoval(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "mail-dead@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{
		Email: email, PickupURL: "https://mail.example/pickup",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore, oauthJobs: map[string]map[string]any{
		"dead-job": {"job_id": "dead-job", "email": email, "status": "running"},
	}}
	server.finishMailAccountOAuthJob("dead-job", email, map[string]any{
		"success": false, "dead": true, "error_code": "account_deactivated", "error": "account_deactivated",
	}, nil)
	profile, _, err := dataStore.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ChatGPTStatus != "dead" || profile.RegistrationStatus != "dead" {
		t.Fatalf("mail account was not marked dead: %+v", profile)
	}
	if accounts := dataStore.FreeAccounts(); len(accounts) != 0 {
		t.Fatalf("mail-only dead handling must not create/remove Team accounts: %#v", accounts)
	}
}

func TestTemporaryATDeadResultDeletesMailbox(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "temporary-at-dead@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{
		Email: email, PickupURL: "https://mail.example/pickup",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore}
	if !server.deleteMailAccountOnDeadLogin(email, map[string]any{
		"success": false, "dead": true, "error_code": "account_deactivated",
	}, "You do not have an account because it has been deleted or deactivated") {
		t.Fatal("explicitly deactivated temporary-AT account was not deleted")
	}
	if _, _, err = dataStore.MailAccountCredential(email); err == nil {
		t.Fatal("deleted temporary-AT account is still present")
	}
}

func TestTemporaryATTransientFailureKeepsMailbox(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "temporary-at-rate-limit@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{
		Email: email, PickupURL: "https://mail.example/pickup",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore}
	if server.deleteMailAccountOnDeadLogin(email, nil, "HTTP 429: rate_limit_exceeded") {
		t.Fatal("transient rate limit must not delete the mailbox")
	}
	if _, _, err = dataStore.MailAccountCredential(email); err != nil {
		t.Fatalf("transient failure removed mailbox: %v", err)
	}
}

func TestBuildCPACredentialExportMatchesGPTAccountManagerShape(t *testing.T) {
	expiresAt := time.Unix(1_800_000_000, 0).UTC()
	exportedAt := time.Unix(1_700_000_000, 0).UTC()
	result := buildCPACredentialExport(mailGPTCredentials{
		Email: "free@example.com", Name: "Free Account", AccessToken: "at", RefreshToken: "rt",
		IDToken: "id-token", SessionToken: "session-token", PlanType: "pro", AccountID: "account-1",
	}, expiresAt, exportedAt)

	for key, want := range map[string]any{
		"type": "codex", "account_id": "account-1", "chatgpt_account_id": "account-1",
		"access_token": "at", "refresh_token": "rt", "id_token": "id-token",
		"session_token": "session-token", "plan_type": "pro",
	} {
		if got := result[key]; got != want {
			t.Fatalf("%s = %#v, want %#v", key, got, want)
		}
	}
	if synthetic, ok := result["id_token_synthetic"].(bool); !ok || synthetic {
		t.Fatalf("id_token_synthetic = %#v, want false", result["id_token_synthetic"])
	}
}

func TestBuildSub2CredentialExportMatchesGPTAccountManagerShape(t *testing.T) {
	expiresAt := time.Unix(1_800_000_000, 0).UTC()
	result := buildSub2CredentialExport(mailGPTCredentials{
		Email: "free@example.com", AccessToken: "at", RefreshToken: "rt", IDToken: "id-token",
		SessionToken: "session-token", PlanType: "pro", AccountID: "account-1",
	}, expiresAt, time.Unix(1_700_000_000, 0).UTC())

	accounts, ok := result["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("accounts = %#v", result["accounts"])
	}
	account, ok := accounts[0].(map[string]any)
	if !ok || account["platform"] != "openai" || account["type"] != "oauth" || account["expires_at"] != expiresAt.Unix() {
		t.Fatalf("unexpected account: %#v", accounts[0])
	}
	credentials, ok := account["credentials"].(map[string]any)
	if !ok || credentials["access_token"] != "at" || credentials["refresh_token"] != "rt" || credentials["chatgpt_account_id"] != "account-1" {
		t.Fatalf("unexpected credentials: %#v", account["credentials"])
	}
}

func TestBuildCPABatchCredentialArchiveContainsOneJSONPerAccount(t *testing.T) {
	exportedAt := time.Unix(1_700_000_000, 0).UTC()
	archiveBytes, err := buildCPABatchCredentialArchive([]preparedMailCredentialExport{
		{Credentials: mailGPTCredentials{Email: "first@example.com", AccessToken: "at-1", RefreshToken: "rt-1"}},
		{Credentials: mailGPTCredentials{Email: "second@example.com", AccessToken: "at-2", RefreshToken: "rt-2"}},
	}, exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("open CPA ZIP: %v", err)
	}
	if len(reader.File) != 2 {
		t.Fatalf("ZIP entries = %d, want 2", len(reader.File))
	}
	for index, entry := range reader.File {
		if strings.ContainsAny(entry.Name, `/\\`) {
			t.Fatalf("unsafe ZIP entry name %q", entry.Name)
		}
		stream, openErr := entry.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		var payload map[string]any
		decodeErr := json.NewDecoder(stream).Decode(&payload)
		_ = stream.Close()
		if decodeErr != nil {
			t.Fatalf("decode %s: %v", entry.Name, decodeErr)
		}
		wantRT := []string{"rt-1", "rt-2"}[index]
		if payload["type"] != "codex" || payload["refresh_token"] != wantRT {
			t.Fatalf("unexpected CPA payload in %s: %#v", entry.Name, payload)
		}
	}
}

func TestBuildSub2BatchCredentialExportCombinesAccounts(t *testing.T) {
	payload := buildSub2BatchCredentialExport([]preparedMailCredentialExport{
		{Credentials: mailGPTCredentials{Email: "first@example.com", AccessToken: "at-1", RefreshToken: "rt-1"}},
		{Credentials: mailGPTCredentials{Email: "second@example.com", AccessToken: "at-2", RefreshToken: "rt-2"}},
	}, time.Unix(1_700_000_000, 0).UTC())
	accounts, ok := payload["accounts"].([]any)
	if !ok || len(accounts) != 2 {
		t.Fatalf("accounts = %#v, want two accounts", payload["accounts"])
	}
	for index, raw := range accounts {
		account, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("account %d = %#v", index, raw)
		}
		wantEmail := []string{"first@example.com", "second@example.com"}[index]
		if account["name"] != wantEmail {
			t.Fatalf("account %d name = %#v, want %q", index, account["name"], wantEmail)
		}
	}
}

func TestBatchCredentialExportRejectsMissingRT(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	for _, item := range []struct {
		email string
		rt    string
	}{
		{email: "ready@example.com", rt: "ready-rt"},
		{email: "missing@example.com"},
	} {
		_, err = dataStore.SaveMailAccount(
			model.MailAccountProfile{Email: item.email},
			model.MailAccountCredentials{Email: item.email, AccessToken: "access-token", RefreshToken: item.rt},
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{store: dataStore}
	req := httptest.NewRequest(http.MethodPost, "/api/mail/accounts/credentials/export-batch", strings.NewReader(`{"emails":["ready@example.com","missing@example.com"],"format":"cpa"}`))
	rec := httptest.NewRecorder()
	server.exportMailAccountCredentialsBatch(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "missing@example.com") || !strings.Contains(rec.Body.String(), "尚未获取 Codex RT") {
		t.Fatalf("missing account is not explained: %s", rec.Body.String())
	}
}

func TestBatchCredentialExportResponses(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	for _, email := range []string{"first@example.com", "second@example.com"} {
		_, err = dataStore.SaveMailAccount(
			model.MailAccountProfile{Email: email},
			model.MailAccountCredentials{Email: email, AccessToken: "at-" + email, RefreshToken: "rt-" + email},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{store: dataStore}

	t.Run("CPA ZIP", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/mail/accounts/credentials/export-batch", strings.NewReader(`{"emails":["first@example.com","second@example.com"],"format":"cpa"}`))
		rec := httptest.NewRecorder()
		server.exportMailAccountCredentialsBatch(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("unexpected response: status=%d type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		reader, zipErr := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if zipErr != nil || len(reader.File) != 2 {
			t.Fatalf("invalid CPA ZIP: entries=%d err=%v", len(reader.File), zipErr)
		}
	})

	t.Run("Sub2 JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/mail/accounts/credentials/export-batch", strings.NewReader(`{"emails":["first@example.com","second@example.com"],"format":"sub2"}`))
		rec := httptest.NewRecorder()
		server.exportMailAccountCredentialsBatch(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("unexpected response: status=%d type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if accounts, ok := payload["accounts"].([]any); !ok || len(accounts) != 2 {
			t.Fatalf("accounts = %#v, want two", payload["accounts"])
		}
	})
}

func TestNormalizeBatchCredentialEmailsValidatesAndDeduplicates(t *testing.T) {
	emails, err := normalizeBatchCredentialEmails([]string{" First@Example.com ", "first@example.com", "second@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(emails, ",") != "first@example.com,second@example.com" {
		t.Fatalf("emails = %#v", emails)
	}
	if _, err = normalizeBatchCredentialEmails([]string{"../bad@example.com"}); err == nil {
		t.Fatal("invalid email should be rejected")
	}
	allPages := make([]string, 501)
	for index := range allPages {
		allPages[index] = fmt.Sprintf("account-%d@example.com", index)
	}
	if normalized, err := normalizeBatchCredentialEmails(allPages); err != nil || len(normalized) != 501 {
		t.Fatal("all-page export must not be capped at 500", err)
	}
}

func TestSafeCredentialExportFilenamePreventsPathTraversal(t *testing.T) {
	name := safeCredentialExportFilename(`../folder\\account@example.com`)
	if strings.ContainsAny(name, `/\\`) || strings.HasPrefix(name, ".") {
		t.Fatalf("unsafe filename %q", name)
	}
}
