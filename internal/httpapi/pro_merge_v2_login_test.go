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

func TestProMergeV2PasswordlessEntryUsesConfiguredRetryBudget(t *testing.T) {
	checkProMergeV2ConfiguredLoginRetry(t, "password_login_restart_required", false)
}

func checkProMergeV2ConfiguredLoginRetry(t *testing.T, code string, retryable bool) {
	for _, retries := range []int{0, 2} {
		for _, recover := range []bool{false, true} {
			f, p, ctx := mergeV2RecoveryFixture(t)
			cfg, _, _, _ := f.s.store.ProSettings()
			cfg.RetryCount, cfg.RetryIntervalSeconds = retries, 17
			if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
				t.Fatal(err)
			}
			calls, waits := 0, 0
			f.s.proMergeV2Login = func(ctx context.Context, email string) (map[string]any, error) {
				calls++
				if email != f.email {
					t.Fatal("changed original account")
				}
				if recover && calls == 2 {
					return mockMergeV2Submit(ctx)
				}
				return map[string]any{"success": false, "error_code": code, "retryable": retryable}, nil
			}
			result, err := f.s.loginProMergeV2(ctx, p, "sub2api", func(_ context.Context, d time.Duration) error {
				waits++
				if d != 17*time.Second {
					t.Fatal("configuration ignored", d)
				}
				latest, _, _ := f.s.store.MailAccountCredential(f.email)
				if latest.ProMergeV2.SubmittedAt != nil || latest.ProMergeV2.Steps["invite"] != "completed" {
					t.Fatal("retry changed checkpoints")
				}
				return nil
			})
			want := retries + 1
			if recover && retries > 0 {
				want = 2
			}
			if err != nil || calls != want || waits != want-1 {
				t.Fatal(result, err, calls, waits)
			}
			if (result["success"] == true) != (recover && retries > 0) {
				t.Fatal(result)
			}
			if f.calls["invite"] != 0 || f.calls["remove"] != 0 {
				t.Fatal("repeated invitation or cleanup")
			}
		}
	}
}

func TestProMergeV2RecoveryCompletesTailWithoutReinvite(t *testing.T) {
	f := mergeV2Fixture(t)
	calls := 0
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		calls++
		if calls == 1 {
			return teamMismatchResult(), nil
		}
		return mockMergeV2Submit(ctx)
	}
	if _, err := f.s.performProMergeV2(t.Context(), f.email); err != nil {
		t.Fatal(err)
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if !p.MergeV2Completed() || calls != 2 || f.calls["invite"] != 1 || f.calls["remove"] != 1 {
		t.Fatal(p.ProMergeV2, calls, f.calls)
	}
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
	result, err := f.s.loginProMergeV2(ctx, p, "sub2api", func(context.Context, time.Duration) error { return nil })
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
	for _, recover := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "recovered"}[recover], func(t *testing.T) {
			f, p, ctx := mergeV2RecoveryFixture(t)
			calls, waits := 0, 0
			f.s.proMergeV2Login = func(ctx context.Context, email string) (map[string]any, error) {
				calls++
				if email != f.email {
					t.Fatal("changed child")
				}
				if recover && calls == 3 {
					return mockMergeV2Submit(ctx)
				}
				return teamMismatchResult(), nil
			}
			result, err := f.s.loginProMergeV2(ctx, p, "sub2api", func(_ context.Context, d time.Duration) error {
				waits++
				if d != 5*time.Second {
					t.Fatalf("unexpected delay: %s", d)
				}
				latest, _, _ := f.s.store.MailAccountCredential(f.email)
				if latest.ProMergeV2.SubmittedAt != nil || latest.ProMergeV2.Steps["invite"] != "completed" {
					t.Fatal("recovery changed completed invite or submitted early")
				}
				return nil
			})
			if err != nil || calls != 3 || waits != 2 || result["success"] != recover {
				t.Fatalf("result=%v error=%v calls=%d waits=%d", result, err, calls, waits)
			}
			latest, _, _ := f.s.store.MailAccountCredential(f.email)
			if latest.MergeV2Accepted() != recover || f.calls["remove"] != 0 {
				t.Fatal("unexpected destructive tail")
			}
			if err = f.s.flushAuditEvents(t.Context()); err != nil {
				t.Fatal(err)
			}
			starts, waitEvents, results := 0, 0, 0
			for _, e := range f.s.store.AutoRotationEventsByAccount(p.ID) {
				switch e.Details["protocol_event"] {
				case "team_login_start":
					starts++
				case "team_relogin_wait":
					waitEvents++
				case "team_login_result":
					results++
				}
				if e.Type == "pro_merge_v2_protocol" && (e.Email != p.Email || e.Details["execution_id"] == nil) {
					t.Fatal("missing trace identity")
				}
			}
			if starts != 3 || waitEvents != 2 || results != 3 {
				t.Fatalf("trace missing: %d %d %d", starts, waitEvents, results)
			}
		})
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
			_, err := f.s.loginProMergeV2(ctx, p, "sub2api", func(ctx context.Context, d time.Duration) error {
				if mode == "context" {
					cancel()
					return waitProAuto(ctx, d)
				}
				_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
					if mode == "stop" {
						p.ProAuto.StopRequested = true
					} else {
						p.ProMergeV2.TeamID = "changed"
					}
				})
				return err
			})
			if err == nil || calls != 1 {
				t.Fatal(err, calls)
			}
			latest, _, _ := f.s.store.MailAccountCredential(f.email)
			if latest.ProMergeV2.SubmittedAt != nil {
				t.Fatal("stop allowed submission")
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
