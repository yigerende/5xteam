package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestSIWCConsentFailureRetriesThenFinal(t *testing.T) {
	for _, tc := range []struct {
		name, source                                     string
		enabled, other, laterOther, paused, changeTarget bool
		logins, finals                                   int
	}{
		{name: "local", source: "local", enabled: true, logins: 3, finals: 1},
		{name: "s2", source: "sub2api", enabled: true, logins: 3, finals: 1},
		{name: "disabled", source: "local", logins: 1},
		{name: "unrelated401", source: "sub2api", enabled: true, other: true, logins: 1},
		{name: "lastFailureIsNetwork", source: "local", enabled: true, laterOther: true, logins: 2},
		{name: "finalPaused", source: "local", enabled: true, paused: true, logins: 3, finals: 1},
		{name: "targetChanged", source: "local", enabled: true, changeTarget: true, logins: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payments, logins, finals atomic.Int32
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/status") {
					fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"auto-remote","status":"success"}]}}`)
					return
				}
				payments.Add(1)
				fmt.Fprint(w, `{"code":0,"data":{"id":"auto-remote","status":"success"}}`)
			})
			configureProAutoFixture(t, s)
			s.proAutoCancel()
			cfg, _, _, _ := st.ProSettings()
			cfg.Sub2.AuthMode, cfg.Sub2.SIWCAuthorizationSource = "siwc", tc.source
			cfg.Sub2.SIWCConsentDeniedFinal = tc.enabled
			cfg.RetryCount, cfg.RetryIntervalSeconds = 2, 1
			cfg.PostMergeGroupIDs = []int64{9}
			cfg.AutoDeletePaused = tc.paused
			if _, err := st.SaveProSettings(cfg, "test-password", ""); err != nil {
				t.Fatal(err)
			}
			if err := st.SetProAutoDeletePaused(tc.paused); err != nil {
				t.Fatal(err)
			}
			s.proAutoHooks = &proAutomationHooks{
				Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
					n := logins.Add(1)
					if (n > 1) != isProPaidRecovery(ctx) {
						t.Error("retry did not use payment-free recovery")
					}
					_, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(email, "before"), "accessToken": proAutoToken(email, "before"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}})
					if err != nil {
						return err
					}
					if tc.other || tc.laterOther && n > 1 {
						return errors.New("network or unrelated HTTP 401")
					}
					if tc.changeTarget {
						changed := cfg
						changed.Sub2.URL = "http://changed.invalid"
						if _, err := st.SaveProSettings(changed, "", ""); err != nil {
							return err
						}
					}
					return &siwcConsentDeniedError{Source: siwcAuthorizationSource(cfg.Sub2), Downstream: model.ProDownstream(cfg)}
				},
				Push: func(context.Context, string) (model.MailAccountProfile, error) {
					t.Error("unexpected push")
					return model.MailAccountProfile{}, errors.New("push")
				},
				Quota: func(context.Context, string, bool) (model.MailAccountProfile, error) {
					t.Error("unexpected quota")
					return model.MailAccountProfile{}, errors.New("quota")
				},
				Merge: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
					finals.Add(1)
					p, _, err := st.MailAccountCredential(email)
					if err != nil {
						return p, err
					}
					p, err = s.prepareProPostMergeGroups(p, cfg)
					if err != nil {
						return p, err
					}
					if p.ProPostMergeGroups != nil {
						t.Error("scheduled groups for an account that was never imported")
					}
					return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
						p.InviteStatus = "completed"
						p.AcceptStatus = "completed"
						p.TransferStatus = "completed"
						p.RemoveStatus = "completed"
						p.SpaceMergedOnce = true
					})
				},
			}
			rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": "tokenseek", "card_id": "test-card", "plan_code": "pro20"})
			if rec.Code != 202 {
				t.Fatal(rec.Body.String())
			}
			p := waitProAutoFinished(t, s, email)
			if tc.paused {
				if finals.Load() != 0 || p.ProAuto.Status != "waiting_quota" || p.ProAuto.Steps["quota"] != "skipped" {
					t.Fatalf("pause lost: %+v", p.ProAuto)
				}
				if err := st.RecoverProAutomations(); err != nil {
					t.Fatal(err)
				}
				changed := cfg
				changed.Sub2.SIWCConsentDeniedFinal = false
				if _, err := st.SaveProSettings(changed, "", ""); err != nil {
					t.Fatal(err)
				}
				if err := s.runProAutomationTail(context.Background(), email); err == nil {
					t.Fatal("disabled policy still executed final action")
				}
				if _, err := st.SaveProSettings(cfg, "", ""); err != nil {
					t.Fatal(err)
				}
				if err := st.SetProAutoDeletePaused(false); err != nil {
					t.Fatal(err)
				}
				if err := s.runProAutomationTail(context.Background(), email); err != nil {
					t.Fatal(err)
				}
				p, _, _ = st.MailAccountCredential(email)
			}
			if payments.Load() != 1 || int(logins.Load()) != tc.logins || int(finals.Load()) != tc.finals {
				t.Fatalf("payments=%d logins=%d finals=%d state=%+v", payments.Load(), logins.Load(), finals.Load(), p.ProAuto)
			}
			if tc.finals > 0 {
				if !strings.Contains(p.ProStages().Errors["oauth"], "not_authorized") {
					t.Fatal("lost consent failure evidence in display")
				}
				if p.ProAuto.Status != "completed" || p.ProAuto.Steps["oauth"] != "failed" || p.ProAuto.Steps["push"] != "skipped" || p.ProAuto.Steps["quota"] != "skipped" {
					t.Fatalf("incorrect evidence: %+v", p.ProAuto)
				}
				if err := s.runProAutomationTail(context.Background(), email); err != nil {
					t.Fatal(err)
				}
				if int(finals.Load()) != tc.finals {
					t.Fatal("repeated final action")
				}
			}
		})
	}
}

func TestSIWCConsentFinalStopGeneration(t *testing.T) {
	a := model.ProAutoState{OrderID: "paid", StopVersion: 4, SIWCConsentFailure: &model.ProSIWCConsentFailure{OrderID: "paid", StopVersion: 4, Exhausted: true}}
	if !a.SIWCConsentFinal() {
		t.Fatal("missing valid final evidence")
	}
	a.StopRequested = true
	if a.SIWCConsentFinal() {
		t.Fatal("ignored stop")
	}
	a.StopRequested = false
	a.StopVersion++
	if a.SIWCConsentFinal() {
		t.Fatal("reused pre-stop evidence")
	}
	a.StopVersion = 4
	a.OrderID = "other"
	if a.SIWCConsentFinal() {
		t.Fatal("reused different order")
	}
}
