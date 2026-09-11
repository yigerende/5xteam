package httpapi

import (
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestParseProtocolOAuthDiagnostic(t *testing.T) {
	line := `[protocol-event] {"schema_version":1,"stage":"email_otp_validate","event":"request_complete","message":"OTP returned","http_status":200,"attempt":2,"level":"info","request":{"method":"POST"},"response":{"page_type":"consent"},"details":{"needs_add_phone":false}}`
	event, ok := parseProtocolOAuthDiagnostic(line)
	if !ok {
		t.Fatal("structured protocol event was not parsed")
	}
	if event.Stage != "email_otp_validate" || event.Event != "request_complete" || event.HTTPStatus != 200 || event.Attempt != 2 {
		t.Fatalf("unexpected protocol event: %+v", event)
	}
	if _, ok := parseProtocolOAuthDiagnostic(`[protocol] ordinary progress`); ok {
		t.Fatal("ordinary progress must not be treated as a structured event")
	}
	if _, ok := parseProtocolOAuthDiagnostic(`[protocol-event] {broken`); ok {
		t.Fatal("malformed event must be ignored")
	}
}

func TestRedactMapRemovesEmbeddedOAuthSecrets(t *testing.T) {
	jwt := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyLTEifQ.signature123"
	input := map[string]any{
		"callback": "http://localhost:1455/auth/callback?code=ac_super-secret-code&state=0123456789abcdef",
		"message":  "Authorization: Bearer " + jwt + "; OTP: 123456; refresh rt_secret-value-12345; phone +14155550123",
		"nested":   map[string]any{"cookie": "oai-client-auth-session=secret", "safe": "account_deactivated"},
	}
	redacted := redactMap(input)
	combined := redacted["callback"].(string) + " " + redacted["message"].(string)
	for _, secret := range []string{"ac_super-secret-code", "0123456789abcdef", jwt, "123456", "rt_secret-value-12345", "+14155550123"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("embedded secret %q was not redacted: %s", secret, combined)
		}
	}
	if redacted["nested"].(map[string]any)["cookie"] != "***" {
		t.Fatalf("cookie value was not redacted: %+v", redacted)
	}
	if redacted["nested"].(map[string]any)["safe"] != "account_deactivated" {
		t.Fatalf("diagnostic error code should remain visible: %+v", redacted)
	}
}

func TestOAuthProtocolDiagnosticUsesAsyncAccountTimeline(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "trace@example.com", UserID: "trace-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.auditOAuthProtocolDiagnostic(account.ID, "job-trace", "relogin", protocolOAuthDiagnostic{
		SchemaVersion: 1, Stage: "workspace_context", Event: "cookie_snapshot",
		Message: "选择 Workspace 前检查认证 Cookie", Level: "error",
		Response: map[string]any{"auth_session_cookie_present": false, "cookie_jar": []any{map[string]any{"name": "oai-did", "domain": "auth.openai.com"}}},
		Details:  map[string]any{"continue_url": "https://auth.openai.com/consent?code=must-not-survive"},
	})
	server.Close()

	events := dataStore.AutoRotationEventsByAccount(account.ID)
	if len(events) != 1 {
		t.Fatalf("stored protocol events=%d, want 1", len(events))
	}
	event := events[0]
	if event.Type != "oauth_protocol" || event.Stage != "workspace_context" || event.Source != "relogin" || event.Details["protocol_event"] != "cookie_snapshot" {
		t.Fatalf("unexpected stored protocol event: %+v", event)
	}
	if strings.Contains(event.Details["continue_url"].(string), "must-not-survive") {
		t.Fatalf("structured event retained callback secret: %+v", event.Details)
	}
	if present, ok := event.Response["auth_session_cookie_present"].(bool); !ok || present {
		t.Fatalf("cookie presence diagnostics were lost: %+v", event.Response)
	}
	if rows, ok := event.Response["cookie_jar"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("cookie metadata was lost: %+v", event.Response)
	}
}

func TestOAuthDiagnosticMetadataPreservesShapeWithoutSecrets(t *testing.T) {
	input := map[string]any{
		"auth_session_cookie_present": true,
		"access_token_present":        false,
		"refresh_token_present":       true,
		"id_token_present":            false,
		"access_token":                "must-not-survive",
		"cookie_jar": []any{map[string]any{
			"name": "oai-client-auth-session", "domain": "auth.openai.com", "path": "/",
			"secure": true, "value": "must-not-survive", "extra": "must-not-survive",
		}},
		"set_cookie_names": []any{"oai-client-auth-session", "login_session"},
	}
	for i := 0; i < 2; i++ {
		input = redactMap(input)
		if input["auth_session_cookie_present"] != true || input["access_token_present"] != false || input["refresh_token_present"] != true {
			t.Fatalf("diagnostic booleans were masked: %+v", input)
		}
		if input["access_token"] != "***" {
			t.Fatal("access token was exposed")
		}
		row := input["cookie_jar"].([]any)[0].(map[string]any)
		if row["name"] != "oai-client-auth-session" || row["domain"] != "auth.openai.com" || row["secure"] != true {
			t.Fatalf("cookie metadata was lost: %+v", row)
		}
		if _, ok := row["value"]; ok {
			t.Fatal("cookie value was exposed")
		}
		if _, ok := row["extra"]; ok {
			t.Fatal("unexpected cookie field was exposed")
		}
		if len(input["set_cookie_names"].([]any)) != 2 {
			t.Fatal("Set-Cookie names were lost")
		}
	}
}

func TestOAuthDiagnosticMetadataDoesNotWhitelistUnvalidatedValues(t *testing.T) {
	input := map[string]any{
		"auth_session_cookie_present": "secret", "access_token_present": "secret",
		"cookie_jar": "oai-client-auth-session=secret", "set_cookie_names": "session=secret",
	}
	for key, value := range redactMap(input) {
		if value != "***" {
			t.Fatalf("%s bypassed redaction: %v", key, value)
		}
	}
}
