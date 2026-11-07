package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func mergeV2RecoveryFixture(t *testing.T) (*proExecutionFixture, model.MailAccountProfile, context.Context) {
	f := mergeV2Fixture(t)
	p, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMergeV2 = &model.ProMergeV2{TeamID: "original-team", Status: "running", Stage: "login", Steps: map[string]string{"invite": "completed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := &mergeV2LoginOptions{TeamID: p.ProMergeV2.TeamID, Call: func(method string, args map[string]any) (any, error) {
		return f.s.mergeV2Checkpoint(t.Context(), f.email, p.ProMergeV2.TeamID, method, args)
	}}
	return f, p, context.WithValue(t.Context(), mergeV2LoginKey{}, opts)
}

func teamMismatchResult() map[string]any {
	return map[string]any{"success": false, "retryable": false, "error_code": "team_session_mismatch", "error": "team mismatch"}
}

func TestProMergeV2IncompleteLoginUsesConfiguredRetryBudget(t *testing.T) {
	checkProMergeV2ConfiguredLoginRetry(t, "login_response_incomplete", true)
}

func TestProMergeV2ExpiredLoginSessionUsesConfiguredRetryBudget(t *testing.T) {
	checkProMergeV2ConfiguredLoginRetry(t, "login_session_expired", true)
}

func TestProMergeV2PasswordlessEntryUsesConfiguredRetryBudget(t *testing.T) {
	checkProMergeV2ConfiguredLoginRetry(t, "password_login_restart_required", false)
}

func checkProMergeV2ConfiguredLoginRetry(t *testing.T, code string, retryable bool) {
	for _, retries := range []int{0, 2} {
		for _, recover := range []bool{false, true} {
			f := mergeV2Fixture(t)
			cfg, _, _, _ := f.s.store.ProSettings()
			cfg.RetryCount, cfg.RetryIntervalSeconds = retries, 17
			if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.s.proMergeV2Login = func(ctx context.Context, email string) (map[string]any, error) {
				calls++
				if email != f.email {
					t.Fatal("account changed")
				}
				if recover && calls == 2 {
					return mockMergeV2Submit(ctx)
				}
				return map[string]any{"success": false, "error_code": code, "retryable": retryable}, nil
			}
			for n := 1; n <= retries+1; n++ {
				p, err := f.s.performProMergeV2(t.Context(), f.email)
				if calls != n || f.s.proMergeRunning != 0 {
					t.Fatal("unbounded or occupied retry", calls, n)
				}
				if recover && n == 2 {
					if err != nil || !p.MergeV2Completed() {
						t.Fatal(p.ProMergeV2, err)
					}
					break
				}
				if err == nil || p.ProMergeV2.Recovery.NextAt == nil {
					t.Fatal("missing durable retry", p.ProMergeV2, err)
				}
				if n <= retries {
					if p.ProMergeV2.Recovery.Phase != "login" || time.Until(*p.ProMergeV2.Recovery.NextAt) < 16*time.Second {
						t.Fatal("wrong budget/delay", p.ProMergeV2.Recovery)
					}
					if _, e := f.s.performProMergeV2(t.Context(), f.email); e == nil || calls != n {
						t.Fatal("early retry")
					}
					if e := f.s.store.RecoverProWorkflows(); e != nil {
						t.Fatal(e)
					}
					dueRecovery(t, f)
				} else {
					if p.ProMergeV2.Recovery.Phase != "cleanup" {
						t.Fatal("exhausted login not cleaned", p.ProMergeV2.Recovery)
					}
					dueRecovery(t, f)
					p, err = f.s.performProMergeV2(t.Context(), f.email)
					if err == nil || !p.ProMergeV2.Recovery.Exhausted || p.MergeV2Completed() || p.ProMergeV2.Steps["remove"] != "completed" {
						t.Fatal("failed workflow not terminated truthfully", p.ProMergeV2, err)
					}
				}
			}
			if f.calls["invite"] != 1 {
				t.Fatal("reinvited during login retry", f.calls)
			}
		}
	}
}

func TestProMergeV2RecoveryCompletesTailWithoutReinvite(t *testing.T) {
	checkProMergeV2ConfiguredLoginRetry(t, "team_session_mismatch", false)
}

func TestProMergeV2InvitationRecoveryCheckpointsAndFreshLogin(t *testing.T) {
	f, p, ctx := mergeV2RecoveryFixture(t)
	calls := 0
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		calls++
		if calls == 1 {
			args := map[string]any{"team_id": p.ProMergeV2.TeamID}
			if _, err := f.s.mergeV2Checkpoint(ctx, f.email, p.ProMergeV2.TeamID, "merge_v2_accepting", args); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.mergeV2Checkpoint(ctx, f.email, p.ProMergeV2.TeamID, "merge_v2_accepting", args); err == nil {
				t.Fatal("duplicate acceptance allowed")
			}
			if _, err := f.s.mergeV2Checkpoint(ctx, f.email, p.ProMergeV2.TeamID, "merge_v2_invite_accepted", args); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"success": false, "error_code": "team_invitation_accepted"}, nil
		}
		return mockMergeV2Submit(ctx)
	}
	_, firstErr := f.s.loginProMergeV2(ctx, p, "sub2api", nil)
	var retry *mergeV2RetryError
	if !errors.As(firstErr, &retry) {
		t.Fatal(firstErr)
	}
	result, err := f.s.loginProMergeV2(ctx, p, "sub2api", nil)
	if err != nil || result["success"] != true || calls != 2 {
		t.Fatal(result, err, calls)
	}
	latest, _, _ := f.s.store.MailAccountCredential(f.email)
	if latest.ProMergeV2.InviteAcceptedAt == nil || latest.ProMergeV2.InviteAcceptAttemptedAt == nil {
		t.Fatal("invitation checkpoint lost")
	}
}

func TestProMergeV2InvitationAcceptanceHonorsStopAndTarget(t *testing.T) {
	for _, kind := range []string{"stop", "target", "uninvited", "submitted"} {
		t.Run(kind, func(t *testing.T) {
			f, p, ctx := mergeV2RecoveryFixture(t)
			args := map[string]any{"team_id": p.ProMergeV2.TeamID}
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				switch kind {
				case "stop":
					p.ProAuto.StopRequested = true
				case "target":
					args["team_id"] = "wrong"
				case "uninvited":
					p.ProMergeV2.Steps["invite"] = "pending"
				case "submitted":
					now := time.Now()
					p.ProMergeV2.SubmittedAt = &now
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.mergeV2Checkpoint(ctx, f.email, p.ProMergeV2.TeamID, "merge_v2_accepting", args); err == nil {
				t.Fatal("unsafe invitation acceptance")
			}
			latest, _, _ := f.s.store.MailAccountCredential(f.email)
			if latest.ProMergeV2.InviteAcceptAttemptedAt != nil {
				t.Fatal("rejected mutation changed checkpoint")
			}
		})
	}
}

func TestProMergeV2TeamRecoveryBudgetAndSuccess(t *testing.T) {
	for _, code := range []string{"email_code_source_missing", "email_code_unavailable", "team_session_expired", "export_not_sent", "network_error"} {
		t.Run(code, func(t *testing.T) { checkProMergeV2ConfiguredLoginRetry(t, code, true) })
	}
}

func TestProMergeV2TeamRecoveryNeverRetriesOtherErrorsOrSubmitted(t *testing.T) {
	for _, mode := range []string{"identity", "dead", "network", "run_error", "submitted", "accepted", "email_code_source_missing", "email_code_unavailable", "submitted_incomplete", "submitted_passwordless"} {
		t.Run(mode, func(t *testing.T) {
			f, p, ctx := mergeV2RecoveryFixture(t)
			calls := 0
			f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
				calls++
				result := teamMismatchResult()
				switch mode {
				case "identity":
					result["error_code"] = "login_failed"
				case "dead":
					result["dead"] = true
				case "network":
					result["error_code"] = "network_error"
					result["retryable"] = true
				case "email_code_source_missing", "email_code_unavailable":
					result["error_code"] = mode
					result["retryable"] = false
				case "run_error":
					return result, errors.New("child exited")
				case "submitted", "accepted", "submitted_incomplete", "submitted_passwordless":
					if mode == "submitted_incomplete" {
						result["error_code"], result["retryable"] = "login_response_incomplete", true
					}
					if mode == "submitted_passwordless" {
						result["error_code"], result["retryable"] = "password_login_restart_required", false
					}
					o := ctx.Value(mergeV2LoginKey{}).(*mergeV2LoginOptions)
					for _, method := range []string{"merge_v2_ready", "merge_v2_submitting"} {
						if _, err := o.Call(method, map[string]any{"team_id": o.TeamID, "user_id": "pro-user"}); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "accepted" {
						if _, err := o.Call("merge_v2_accepted", nil); err != nil {
							t.Fatal(err)
						}
					}
				}
				return result, nil
			}
			result, err := f.s.loginProMergeV2(ctx, p, "sub2api", func(context.Context, time.Duration) error { t.Fatal("unexpected retry"); return nil })
			if calls != 1 {
				t.Fatal(calls)
			}
			if (mode == "submitted" || mode == "submitted_incomplete" || mode == "submitted_passwordless") && !errors.Is(err, errMergeV2Unknown) {
				t.Fatal(err)
			}
			if mode == "accepted" && (err != nil || result["success"] != true) {
				t.Fatal(result, err)
			}
		})
	}
}

func TestProMergeV2TeamRecoveryStopDuringWait(t *testing.T) {
	for _, mode := range []string{"context", "stop", "team_changed"} {
		t.Run(mode, func(t *testing.T) {
			f, p, base := mergeV2RecoveryFixture(t)
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			calls := 0
			f.s.proMergeV2Login = func(context.Context, string) (map[string]any, error) { calls++; return teamMismatchResult(), nil }
			_, _ = f.s.loginProMergeV2(ctx, p, "sub2api", nil)
			if mode == "context" {
				cancel()
			} else {
				_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
					if mode == "stop" {
						p.ProAuto.StopRequested = true
					} else {
						p.ProMergeV2.TeamID = "changed"
					}
				})
			}
			if _, err := f.s.loginProMergeV2(ctx, p, "sub2api", nil); err == nil || calls != 1 {
				t.Fatal("stop allowed another login", err, calls)
			}
		})
	}
}

func TestProMergeV2DiagnosticsAreNonblockingAndStripSecrets(t *testing.T) {
	// No writer or database: publishing still returns immediately, including overflow.
	s := &Server{auditQueue: make(chan model.AutoRotationEvent, 1)}
	p := model.MailAccountProfile{ID: "account", Email: "child@example.com", ProMergeV2: &model.ProMergeV2{AdminID: "mother"}}
	d := protocolOAuthDiagnostic{Stage: "api_auth_session", Event: "request_complete", HTTPStatus: 200,
		Request:  map[string]any{"url": "https://user:private-pass@auth.openai.com/callback?code=private-code&unknown=private-query#private-fragment", "body": "private-body", "cookie": "private-cookie", "proxy": map[string]any{"endpoint": "http://u:private-proxy@proxy.invalid:8080", "configured": true}},
		Response: map[string]any{"page_type": "external_url", "accessToken": "private-at", "body": "private-response", "headers": map[string]any{"set-cookie": "private-cookie", "x-request-id": "trace-id", "retry-after": "5"}},
		Details:  map[string]any{"at_team_matches": false, "email_matches": true, "password": "private-password", "cookie_jar": "private-jar"}}
	done := make(chan struct{})
	go func() {
		s.auditMergeV2Login(p, "sub2api", "execution", 1, d)
		s.auditMergeV2Login(p, "sub2api", "execution", 1, d)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("diagnostics blocked workflow")
	}
	if s.auditDropped.Load() != 1 {
		t.Fatal("overflow invisible")
	}
	e := <-s.auditQueue
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "private-") {
		t.Fatalf("secret persisted: %s", b)
	}
	if e.Response["page_type"] != "external_url" || e.Details["at_team_matches"] != false || e.HTTPStatus != 200 {
		t.Fatal("lost useful evidence")
	}
}
