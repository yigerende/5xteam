package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRedactMailPayloadRemovesSecretsRecursively(t *testing.T) {
	payload := map[string]any{
		"email":         "person@example.com",
		"mail_password": "mail-secret",
		"pickup_url":    "https://mail.example/pickup?token=pickup-secret",
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
