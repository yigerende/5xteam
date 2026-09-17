package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/cpa"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

type manualRemoveFixture struct {
	s             *Server
	admin         model.AdminAccountProfile
	motherURL     string
	kicks, leaves atomic.Int32
	onKick        func(http.ResponseWriter, *http.Request)
}

func newManualRemoveFixture(t *testing.T) *manualRemoveFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &manualRemoveFixture{s: &Server{store: st, sub2: sub2.New(), cpa: cpa.New(), auditQueue: make(chan model.AutoRotationEvent, 512)}}
	mother := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.kicks.Add(1)
		if r.Method != "DELETE" || !strings.Contains(r.URL.Path, "/users/") || r.Header.Get("Authorization") != "Bearer mother-token" {
			t.Errorf("unexpected mother request: %s %s", r.Method, r.URL.Path)
		}
		if f.onKick != nil {
			f.onKick(w, r)
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(mother.Close)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.leaves.Add(1)
		if r.Method != "DELETE" || r.Header.Get("Authorization") != "Bearer child-token" {
			t.Error("wrong child leave token or request")
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(child.Close)
	f.motherURL = mother.URL
	settings := model.DefaultSettings()
	settings.BaseURL, settings.ProxyURL = mother.URL, child.URL
	settings.NetworkRetryCount = 0
	if err = st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	rotation := model.DefaultAutoRotationSettings()
	rotation.RemoveMethod, rotation.TeamOperationIntervalSeconds = "child_leave", 0
	if _, err = st.SaveAutoRotationSettings(rotation); err != nil {
		t.Fatal(err)
	}
	f.admin = saveTestAdmin(t, st, model.AdminAccountProfile{Label: "manual-mother", TeamAccountID: "manual-team"}, "mother-token", mother.URL)
	return f
}

func (f *manualRemoveFixture) account(t *testing.T, name, method string, mail bool) model.FreeAccountProfile {
	t.Helper()
	p, _, err := f.s.store.SaveImportedFreeAccount(model.FreeAccountProfile{Email: name + "@example.com", UserID: name}, "stale-child-token")
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
		p.AdminAccountID, p.TeamAccountID = f.admin.ID, f.admin.TeamAccountID
		p.InviteStatus, p.AcceptStatus, p.RemoveStatus = "completed", "completed", "pending"
		p.RemoveMethod = method
	})
	if err != nil {
		t.Fatal(err)
	}
	if mail {
		if _, err = f.s.store.SaveMailAccount(model.MailAccountProfile{Email: p.Email}, model.MailAccountCredentials{Email: p.Email, AccessToken: "child-token"}); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func callManualRemoval(s *Server, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/free-accounts/"+id+"/remove", strings.NewReader(`{}`))
	r.SetPathValue("id", id)
	out := httptest.NewRecorder()
	s.removeFreeAccount(out, r)
	return out
}

func TestManualRemovalForcesMotherWithoutChangingAutomaticPolicy(t *testing.T) {
	for _, manual := range []bool{true, false} {
		for _, method := range []string{"", "child_leave", "mother_kick"} {
			t.Run(fmt.Sprintf("manual=%v/pinned=%s", manual, method), func(t *testing.T) {
				f := newManualRemoveFixture(t)
				p := f.account(t, "child", method, !manual) // Manual must also work with no usable child credentials.
				if manual {
					out := callManualRemoval(f.s, p.ID)
					if out.Code != 200 {
						t.Fatalf("manual remove: %d %s", out.Code, out.Body.String())
					}
				} else if _, err := f.s.performFreeAccountRemove(t.Context(), p.ID); err != nil {
					t.Fatal(err)
				}
				got, _, err := f.s.store.FreeAccountCredential(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantMethod := "child_leave"
				if manual || method == "mother_kick" {
					wantMethod = "mother_kick"
				}
				if got.RemoveMethod != wantMethod || got.RemoveStatus != "completed" || got.RemoteRemovedAt == nil || got.RemovedAt == nil || !got.DownstreamCleaned {
					t.Fatalf("incomplete removal: %+v", got)
				}
				if (f.kicks.Load() == 1) != (wantMethod == "mother_kick") || f.kicks.Load()+f.leaves.Load() != 1 {
					t.Fatal("wrong credential/proxy/number of requests")
				}
				if got.Dead || got.ReloginExhausted || f.s.store.AutoRotationSettings().RemoveMethod != "child_leave" {
					t.Fatal("manual override changed account classification or global policy")
				}
				// Completed child-leave records must not be rewritten as mother-kick by a later manual click.
				if out := callManualRemoval(f.s, p.ID); out.Code != 200 {
					t.Fatal("idempotent repeat failed")
				}
				again, _, _ := f.s.store.FreeAccountCredential(p.ID)
				if again.RemoveMethod != wantMethod || f.kicks.Load()+f.leaves.Load() != 1 {
					t.Fatal("repeat changed history or sent a duplicate request")
				}
				if manual {
					found := false
					for len(f.s.auditQueue) > 0 {
						event := <-f.s.auditQueue
						if event.Stage == "remove_request_start" {
							proxy, _ := event.Details["proxy"].(map[string]any)
							found = event.Details["forced_mother_kick"] == true && event.Details["remove_method"] == "mother_kick" && proxy["name"] == "manual-mother-proxy"
						}
					}
					if !found {
						t.Fatal("forced method or dedicated proxy missing from audit")
					}
				}
			})
		}
	}
}

func TestManualRemovalFailureDoesNotFallBackToChild(t *testing.T) {
	f := newManualRemoveFixture(t)
	p := f.account(t, "failed", "child_leave", true)
	f.onKick = func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture 429", 429) }
	if out := callManualRemoval(f.s, p.ID); out.Code != 400 {
		t.Fatal("expected upstream rejection")
	}
	got, _, _ := f.s.store.FreeAccountCredential(p.ID)
	if got.RemoveStatus != "failed" || got.RemoveMethod != "child_leave" || got.RemoteRemovedAt != nil || got.Dead || got.ReloginExhausted || f.leaves.Load() != 0 {
		t.Fatal("failure changed policy, marked dead or used child fallback")
	}
	f.onKick = nil
	if out := callManualRemoval(f.s, p.ID); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if f.kicks.Load() != 2 || f.leaves.Load() != 0 {
		t.Fatal("manual retry did not force mother")
	}
}

func TestManualRemovalGuardsDoNotContactOpenAI(t *testing.T) {
	for _, state := range []string{"not_entered", "missing_admin", "missing_proxy", "unknown"} {
		t.Run(state, func(t *testing.T) {
			f := newManualRemoveFixture(t)
			p := f.account(t, "guard", "child_leave", true)
			if state == "missing_proxy" {
				if _, err := f.s.store.UpdateAdminAccountProxy(f.admin.ID, ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
				if state == "not_entered" {
					p.AcceptStatus = "pending"
				}
				if state == "missing_admin" {
					p.AdminAccountID = "missing"
				}
			}); err != nil {
				t.Fatal(err)
			}
			if state == "unknown" {
				p.ID = "missing"
			}
			if out := callManualRemoval(f.s, p.ID); out.Code != 400 {
				t.Fatalf("guard did not fail: %s", out.Body.String())
			}
			if f.kicks.Load()+f.leaves.Load() != 0 {
				t.Fatal("invalid request contacted upstream")
			}
		})
	}
}

func TestManualRemovalDownstreamCleanupRetry(t *testing.T) {
	for _, provider := range []string{"sub2", "cpa"} {
		t.Run(provider, func(t *testing.T) {
			f := newManualRemoveFixture(t)
			p := f.account(t, "cleanup", "child_leave", false)
			var deletes, costs atomic.Int32
			down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-admin"}}`)
				case strings.HasSuffix(r.URL.Path, "/stats"):
					costs.Add(1)
					fmt.Fprint(w, `{"code":0,"data":{"summary":{"total_standard_cost":12,"total_user_cost":7}}}`)
				case r.Method == "DELETE":
					if f.kicks.Load() != 1 {
						t.Error("cleanup happened before kick or kick repeated")
					}
					if provider == "sub2" && r.URL.Path != "/api/v1/admin/accounts/42" {
						t.Error("wrong Sub2 delete target")
					}
					if provider == "cpa" && (r.URL.Path != "/v0/management/auth-files" || r.URL.Query().Get("name") != "cleanup.json") {
						t.Error("wrong CPA delete target")
					}
					if deletes.Add(1) == 1 {
						http.Error(w, "fixture cleanup failure", 502)
						return
					}
					fmt.Fprint(w, `{"code":0,"data":{}}`)
				default:
					t.Errorf("unexpected downstream %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(down.Close)
			push := model.DefaultSub2Settings()
			push.Provider, push.URL, push.Email = provider, down.URL, "fixture@example.com"
			if _, err := f.s.store.SaveSub2Settings(push, "fixture-password"); err != nil {
				t.Fatal(err)
			}
			cpaSettings := model.DefaultCPASettings()
			cpaSettings.URL = down.URL
			if _, err := f.s.store.SaveCPASettings(cpaSettings, "fixture-key"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
				p.PushProvider, p.PushStatus, p.Sub2AccountID, p.CPAAuthFileName = provider, "completed", 42, "cleanup.json"
			}); err != nil {
				t.Fatal(err)
			}
			if out := callManualRemoval(f.s, p.ID); out.Code != 400 {
				t.Fatal("cleanup failure not returned")
			}
			got, _, _ := f.s.store.FreeAccountCredential(p.ID)
			if got.RemoveStatus != "cleanup_pending" || got.RemoteRemovedAt == nil || got.RemoveMethod != "mother_kick" {
				t.Fatal("cleanup retry state lost")
			}
			if out := callManualRemoval(f.s, p.ID); out.Code != 200 {
				t.Fatal(out.Body.String())
			}
			if f.kicks.Load() != 1 || f.leaves.Load() != 0 || deletes.Load() != 2 {
				t.Fatal("cleanup retry sent another kick/leave")
			}
			got, _, _ = f.s.store.FreeAccountCredential(p.ID)
			if got.RemoveStatus != "completed" || !got.DownstreamCleaned {
				t.Fatal("cleanup incomplete")
			}
			if provider == "sub2" && (costs.Load() != 1 || got.TotalCostUSD != 12 || got.TotalUserCostUSD == nil || *got.TotalUserCostUSD != 7) {
				t.Fatal("final cost snapshot missing")
			}
			if provider == "cpa" && costs.Load() != 0 {
				t.Fatal("CPA queried Sub2 costs")
			}
		})
	}
}

func TestManualRemovalConcurrentRequests(t *testing.T) {
	for _, scenario := range []string{"same_account", "same_team_interval", "different_teams", "automatic_and_manual"} {
		t.Run(scenario, func(t *testing.T) {
			f := newManualRemoveFixture(t)
			a := f.account(t, "first", "child_leave", false)
			b := a
			if scenario != "same_account" {
				b = f.account(t, "second", "child_leave", true)
			}
			if scenario == "different_teams" {
				admin := saveTestAdmin(t, f.s.store, model.AdminAccountProfile{Label: "second-mother", TeamAccountID: "other-team"}, "mother-token", f.motherURL)
				var err error
				b, err = f.s.store.UpdateFreeAccount(b.ID, func(p *model.FreeAccountProfile) { p.AdminAccountID, p.TeamAccountID = admin.ID, admin.TeamAccountID })
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "automatic_and_manual" {
				if _, err := f.s.store.UpdateFreeAccount(b.ID, func(p *model.FreeAccountProfile) { p.Dead = true }); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "same_team_interval" {
				rotation := f.s.store.AutoRotationSettings()
				rotation.TeamOperationIntervalSeconds = 1
				if _, err := f.s.store.SaveAutoRotationSettings(rotation); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var starts []time.Time
			var active, maximum int
			both := make(chan struct{})
			f.onKick = func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				active++
				if active > maximum {
					maximum = active
				}
				starts = append(starts, time.Now())
				if len(starts) == 2 {
					close(both)
				}
				mu.Unlock()
				if scenario == "different_teams" {
					select {
					case <-both:
					case <-time.After(3 * time.Second):
						t.Error("different teams were serialized")
					}
				} else {
					time.Sleep(40 * time.Millisecond)
				}
				mu.Lock()
				active--
				mu.Unlock()
				w.WriteHeader(204)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for index, p := range []model.FreeAccountProfile{a, b} {
				go func(index int, id string) {
					<-start
					if scenario == "automatic_and_manual" && index == 1 {
						_, err := f.s.performFreeAccountRemove(t.Context(), id)
						results <- err
						return
					}
					out := callManualRemoval(f.s, id)
					if out.Code != 200 {
						results <- fmt.Errorf("remove failed: %s", out.Body.String())
					} else {
						results <- nil
					}
				}(index, p.ID)
			}
			close(start)
			for range 2 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			want := int32(2)
			if scenario == "same_account" {
				want = 1
			}
			if f.kicks.Load() != want || f.leaves.Load() != 0 {
				t.Fatal("duplicate request or child leave")
			}
			if scenario == "different_teams" {
				if maximum != 2 {
					t.Fatal("different teams not parallel")
				}
			} else if maximum != 1 {
				t.Fatal("same team requests overlapped")
			}
			if scenario == "same_team_interval" && starts[1].Sub(starts[0]) < 990*time.Millisecond {
				t.Fatal("mother operation cooldown was skipped")
			}
		})
	}
}
