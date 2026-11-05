package httpapi

import (
	"errors"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func temporaryATFixture(t *testing.T, scope string) (*Server, model.MailAccountProfile, model.MailAccountCredentials) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const email = "temporary-at@example.com"
	if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: scope}, model.MailAccountCredentials{Email: email, RefreshToken: "keep-rt"}); err != nil {
		t.Fatal(err)
	}
	if scope == "pro" {
		if err = st.StartProManualStage(email, "login", "job"); err != nil {
			t.Fatal(err)
		}
	}
	p, c, err := st.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 10), registrationJobs: map[string]map[string]any{
		"job": {"email": email, "pro_stage": "login", "status": "running", "logs": []any{}},
	}}
	return s, p, c
}

func TestTemporaryATTerminalFailuresDeleteAccount(t *testing.T) {
	for _, scope := range []string{"mail", "pro"} {
		for _, tc := range []struct {
			name   string
			result map[string]any
			err    error
		}{
			{"network_exhausted", nil, errors.New("curl: (28) timeout after retries")},
			{"rate_limit_exhausted", map[string]any{"success": false, "error": "HTTP 429 retries exhausted"}, nil},
			{"deactivated", map[string]any{"success": false, "dead": true, "error": "account_deactivated"}, nil},
			{"missing_token", map[string]any{"success": true}, nil},
			{"empty_result", nil, nil},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				s, p, c := temporaryATFixture(t, scope)
				s.finishTemporaryATLogin("job", p, c, tc.result, tc.err)
				if _, _, err := s.store.MailAccountCredential(p.Email); err == nil {
					t.Fatal("terminal failure left account present")
				}
				job, _ := s.localRegistrationStatus("job")
				if job["status"] != "failed" || !strings.Contains(job["error"].(string), "账号已自动删除") {
					t.Fatal(job)
				}
				if tc.err != nil && !strings.Contains(job["error"].(string), tc.err.Error()) {
					t.Fatal("lost original error")
				}
				event := <-s.auditQueue
				if event.Details["account_deleted"] != true || event.AccountID != p.ID {
					t.Fatal(event)
				}
			})
		}
	}
}

func TestTemporaryATRetryThenSuccessKeepsAccount(t *testing.T) {
	s, p, c := temporaryATFixture(t, "pro")
	for _, event := range []protocolOAuthDiagnostic{
		{Stage: "login", Event: "exception", Message: "HTTP 429", Level: "error"},
		{Stage: "login", Event: "retry_wait", Message: "retry pending"},
	} {
		s.appendRegistrationDiagnostic("job", event)
		if _, _, err := s.store.MailAccountCredential(p.Email); err != nil {
			t.Fatal("retry deleted account", err)
		}
	}
	s.finishTemporaryATLogin("job", p, c, map[string]any{"success": true, "access_token": "fresh-at", "rate_limit_retries": float64(2)}, nil)
	_, saved, err := s.store.MailAccountCredential(p.Email)
	if err != nil || saved.AccessToken != "fresh-at" || saved.RefreshToken != "keep-rt" || saved.ChatGPTSession == "" {
		t.Fatal("successful credentials not preserved", err)
	}
	job, _ := s.localRegistrationStatus("job")
	if job["status"] != "success" || len(s.auditQueue) != 0 {
		t.Fatal(job)
	}
}

func TestTemporaryATStaleFailureDoesNotDeleteNewAttempt(t *testing.T) {
	s, p, c := temporaryATFixture(t, "pro")
	if err := s.store.StartProManualStage(p.Email, "login", "new-job"); err != nil {
		t.Fatal(err)
	}
	s.finishTemporaryATLogin("job", p, c, nil, errors.New("late timeout"))
	current, _, err := s.store.MailAccountCredential(p.Email)
	if err != nil || current.ProManualStages["login"].ID != "new-job" || current.ProManualStages["login"].Status != "running" {
		t.Fatal("late failure affected new task", err)
	}
	job, _ := s.localRegistrationStatus("job")
	if !strings.Contains(job["error"].(string), "账号自动删除未完成") {
		t.Fatal(job)
	}
	if (<-s.auditQueue).Details["account_deleted"] != false {
		t.Fatal("false deletion report")
	}
}

func TestTemporaryATSuccessfulAcquisitionWithSaveFailureKeepsAccount(t *testing.T) {
	s, p, c := temporaryATFixture(t, "mail")
	s.finishTemporaryATLogin("job", p, c, map[string]any{"success": true, "access_token": "fresh-at", "invalid_session": make(chan int)}, nil)
	if _, _, err := s.store.MailAccountCredential(p.Email); err != nil {
		t.Fatal("local persistence failure deleted an account whose login succeeded", err)
	}
	job, _ := s.localRegistrationStatus("job")
	if job["status"] != "failed" || len(s.auditQueue) != 0 {
		t.Fatal(job)
	}
}
