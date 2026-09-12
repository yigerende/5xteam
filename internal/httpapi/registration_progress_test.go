package httpapi

import "testing"

func TestTemporaryATProgressDoesNotTurnIntermediateFailureIntoFinalFailure(t *testing.T) {
	s := &Server{registrationJobs: map[string]map[string]any{"job": {"status": "running", "logs": []any{}}}}
	s.appendRegistrationDiagnostic("job", protocolOAuthDiagnostic{Stage: "identifier", Event: "exception", HTTPStatus: 429, Level: "error", Message: "attempt failed"})
	job, _ := s.localRegistrationStatus("job")
	if job["status"] != "running" {
		t.Fatal(job)
	}
	s.appendRegistrationDiagnostic("job", protocolOAuthDiagnostic{Stage: "rate_limit", Event: "retry_wait", HTTPStatus: 429, Level: "warning", Message: "waiting", Details: map[string]any{"retry_number": 1}})
	job, _ = s.localRegistrationStatus("job")
	if job["status"] != "running" || job["state"] != "retry_wait" || job["retry_count"] != 1 {
		t.Fatal(job)
	}
	s.appendRegistrationDiagnostic("job", protocolOAuthDiagnostic{Stage: "session", Event: "progress", Level: "success", Message: "token obtained"})
	job, _ = s.localRegistrationStatus("job")
	if job["status"] != "running" {
		t.Fatal("success before persistence", job)
	}
	s.finishRegistration("job", "success", "AT saved after retry")
	job, _ = s.localRegistrationStatus("job")
	logs := job["logs"].([]any)
	if job["status"] != "success" || job["error"] != "" || logs[0].(map[string]any)["level"] != "error" || logs[len(logs)-1].(map[string]any)["level"] != "success" {
		t.Fatal(job)
	}
}

func TestTemporaryATFinalFailureHasErrorLog(t *testing.T) {
	s := &Server{registrationJobs: map[string]map[string]any{"job": {"status": "running", "logs": []any{}}}}
	s.finishRegistration("job", "failed", "HTTP 429 retries exhausted")
	job, _ := s.localRegistrationStatus("job")
	logs := job["logs"].([]any)
	if job["status"] != "failed" || job["result"].(map[string]any)["success"] != false || logs[0].(map[string]any)["level"] != "error" {
		t.Fatal(job)
	}
}
