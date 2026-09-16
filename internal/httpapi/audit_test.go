package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestOAuthLoginMethodSurvivesAsyncPersistenceAndAccountExport(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "method@example.com", UserID: "method-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	details := selectOAuthLogin("password_totp", model.MailAccountCredentials{TotpSecret: "JBSWY3DPEHPK3PXP"}).details()
	details["login_method"], details["login_auth_status"] = "email_otp", "succeeded"
	server.auditOAuthProtocolDiagnostic(account.ID, "job-mode", "relogin", protocolOAuthDiagnostic{
		SchemaVersion: 1, Stage: "login_method", Event: "login_result", Message: "邮箱验证码登录成功", Details: details,
		Request: map[string]any{"password": "private-password", "totp_secret": "private-secret", "code": "765432"},
	})
	server.Close() // Drain the existing asynchronous writer.
	request := httptest.NewRequest(http.MethodGet, "/api/free-accounts/"+account.ID+"/events/export", nil)
	request.SetPathValue("id", account.ID)
	recorder := httptest.NewRecorder()
	server.exportFreeAccountEvents(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("export returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var exported executionLogExport
	if err := json.Unmarshal(recorder.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported.Events) != 1 {
		t.Fatalf("unexpected event count: %d", len(exported.Events))
	}
	event := exported.Events[0]
	if event.Source != "relogin" || event.Details["configured_login_mode"] != "password_totp" || event.Details["login_method"] != "email_otp" || event.Details["login_fallback_reason"] != "missing_password" || event.Details["login_auth_status"] != "succeeded" {
		t.Fatalf("login metadata lost: %+v", event)
	}
	for _, secret := range []string{"private-password", "private-secret", "765432"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("export leaked %s", secret)
		}
	}
}

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
		"auth_session_cookie_present":  true,
		"login_session_cookie_present": false,
		"access_token_present":         false,
		"refresh_token_present":        true,
		"id_token_present":             false,
		"access_token":                 "must-not-survive",
		"cookie_jar": []any{map[string]any{
			"name": "oai-client-auth-session", "domain": "auth.openai.com", "path": "/",
			"secure": true, "value": "must-not-survive", "extra": "must-not-survive",
		}},
		"set_cookie_names": []any{"oai-client-auth-session", "login_session"},
	}
	for i := 0; i < 2; i++ {
		input = redactMap(input)
		if input["auth_session_cookie_present"] != true || input["login_session_cookie_present"] != false || input["access_token_present"] != false || input["refresh_token_present"] != true {
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
		"login_session_cookie_present": "secret",
		"auth_session_cookie_present":  "secret", "access_token_present": "secret",
		"cookie_jar": "oai-client-auth-session=secret", "set_cookie_names": "session=secret",
	}
	for key, value := range redactMap(input) {
		if value != "***" {
			t.Fatalf("%s bypassed redaction: %v", key, value)
		}
	}
}

func TestAccountExportFlushesPendingOAuthEventsAndPreservesTrace(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "export@example.com", UserID: "export-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	trace := autoRotationTraceContext{RunID: "run-1", TaskID: "task-1", CycleID: account.CycleID, Attempt: 2, MaxAttempts: 4}
	ctx := context.WithValue(t.Context(), autoRotationTraceContextKey{}, trace)
	job := s.newAccountOAuthJob(ctx, account, "oauth")
	jobID := job["job_id"].(string)
	s.updateOAuthJob(jobID, "running", "running")
	s.auditOAuthProtocolDiagnostic(account.ID, jobID, "oauth", protocolOAuthDiagnostic{
		SchemaVersion: 1, Stage: "authorize", Event: "request_complete", HTTPStatus: 403, DurationMS: 42,
		Details:  map[string]any{"round": 3, "quality_attempt": 1},
		Response: map[string]any{"login_session_cookie_present": false, "access_token": "private-token"},
	})
	payload, err := s.executionLogSnapshot(t.Context(), account.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Coverage.FlushCompleted || payload.Coverage.EventCount != 2 || payload.Coverage.RunningOAuthJobs != 1 || payload.Coverage.HistoricalEvents != 0 {
		t.Fatalf("incomplete snapshot: %+v", payload.Coverage)
	}
	if len(payload.OAuthJobs) != 1 || payload.OAuthJobs[0]["source"] != "auto_rotation" || payload.OAuthJobs[0]["round"] != 3 {
		t.Fatalf("job context missing: %+v", payload.OAuthJobs)
	}
	for _, event := range payload.Events {
		if event.RunID != trace.RunID || event.TaskID != trace.TaskID || event.CycleID != account.CycleID || event.Details["outer_attempt"] != float64(2) {
			t.Fatalf("trace lost: %+v", event)
		}
		if event.Type == "oauth_protocol" && (event.DurationMS != 42 || event.Response["login_session_cookie_present"] != false || event.Response["access_token"] != "***") {
			t.Fatalf("diagnostics lost or secret leaked: %+v", event)
		}
	}
	s.finishOAuthJob(jobID, account.ID, map[string]any{"success": false, "error": "fixture failure", "retryable": false}, nil)
	payload, err = s.executionLogSnapshot(t.Context(), "", trace.RunID, trace.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 3 || payload.Coverage.RunningOAuthJobs != 0 || payload.OAuthJobs[0]["completed_at"] == nil {
		t.Fatalf("terminal event or status lost: %+v", payload)
	}
	terminal := payload.Events[0]
	if terminal.Stage != "job_finished" || terminal.Details["job_status"] != "failed" || terminal.Details["retryable"] != false {
		t.Fatalf("terminal outcome lost: %+v", terminal)
	}
}

func TestAuditFailedBatchIsRetriedWithoutLosingEvents(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := time.Now().Add(-time.Hour)
	if err := st.AddAutoRotationEvent(model.AutoRotationEvent{ID: "duplicate", AccountID: "account", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	s.enqueueAuditEvent(model.AutoRotationEvent{ID: "duplicate", AccountID: "account", Message: "pending"})
	if err := s.flushAuditEvents(t.Context()); err == nil {
		t.Fatal("expected duplicate ID write failure")
	}
	if s.auditWriteErrors.Load() == 0 {
		t.Fatal("write failure was hidden")
	}
	if err := st.PurgeAutoRotationHistory(old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	payload, err := s.executionLogSnapshot(t.Context(), "account", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Coverage.FlushCompleted || len(payload.Events) != 1 || payload.Events[0].Message != "pending" || payload.AuditHealth.WriteErrors == 0 {
		t.Fatalf("retry lost the original batch: %+v", payload)
	}
}

func TestExportReportsQueueOverflowAndUnavailableWriter(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 1)}
	s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: "account"})
	s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: "account", Level: "error"})
	payload, err := s.executionLogSnapshot(t.Context(), "account", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if payload.AuditHealth.DroppedEvents != 1 || payload.Coverage.FlushCompleted || payload.Coverage.FlushError == "" {
		t.Fatalf("export hid logging gaps: %+v", payload)
	}
}

func TestOAuthRoundDiagnosticSeparatesRetryFromExhaustion(t *testing.T) {
	for _, test := range []struct {
		round     int
		retryable bool
		event     string
		willRetry bool
	}{{1, true, "round_retry", true}, {3, true, "round_exhausted", false}, {1, false, "round_stopped", false}} {
		event := oauthRoundDecision(test.round, map[string]any{"success": false, "retryable": test.retryable, "error_code": "fixture_error"}, nil)
		if event.Event != test.event || event.Details["will_retry"] != test.willRetry || event.Details["retry_scope"] != "oauth_job" || event.Details["error_code"] != "fixture_error" {
			t.Fatalf("incorrect retry diagnosis: %+v", event)
		}
	}
}
