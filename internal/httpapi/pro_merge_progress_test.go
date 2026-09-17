package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestProMergeProgressAndKeepsDownstream(t *testing.T) {
	for _, provider := range []string{"sub2", "cpa"} {
		for _, failure := range []string{"", "invite", "accept", "transfer", "remove", "team_removed"} {
			t.Run(provider+"/"+failure, func(t *testing.T) {
				st, err := store.Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				const email = "merge-fixture@example.com"
				s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 32)}
				var mu sync.Mutex
				calls := map[string]int{}
				failed := false
				stageState := func(p model.MailAccountProfile, stage string) string {
					switch stage {
					case "invite":
						return p.InviteStatus
					case "accept":
						return p.AcceptStatus
					case "transfer":
						return p.TransferStatus
					default:
						return p.RemoveStatus
					}
				}
				handler := func(mother bool) http.HandlerFunc {
					return func(w http.ResponseWriter, r *http.Request) {
						stage := ""
						switch {
						case r.Method == "DELETE" && r.URL.Path == "/accounts/fixture-team/users/fixture-child":
							stage = "remove"
						case strings.HasSuffix(r.URL.Path, "/invites/accept"):
							stage = "accept"
						case r.URL.Path == "/accounts/transfer":
							stage = "transfer"
						case strings.HasSuffix(r.URL.Path, "/invites"):
							stage = "invite"
						default:
							t.Errorf("unexpected remote request: %s %s", r.Method, r.URL.Path)
							http.NotFound(w, r)
							return
						}
						if mother != (stage == "invite" || stage == "remove") {
							t.Error("wrong global/dedicated proxy")
						}
						token := "Bearer child-token"
						if mother {
							token = "Bearer mother-token"
						}
						if r.Header.Get("Authorization") != token {
							t.Error("wrong account token")
						}
						p, _, e := st.MailAccountCredential(email)
						if e != nil || !p.ProWorkflowRunning || stageState(p, stage) != "running" {
							t.Errorf("stage %s not persisted as running: %+v %v", stage, p, e)
						}
						// A separate list request must observe progress while the action POST is still pending.
						list := httptest.NewRecorder()
						s.listProAccounts(list, httptest.NewRequest("GET", "/api/pro-accounts?page=1&page_size=10", nil))
						if list.Code != 200 || !strings.Contains(list.Body.String(), fmt.Sprintf("%q:%q", "pro_"+stage+"_status", "running")) {
							t.Errorf("running stage not visible in list: %d %s", list.Code, list.Body.String())
						}
						mu.Lock()
						calls[stage]++
						reject := stage == failure && !failed
						if reject {
							failed = true
						}
						mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						if reject {
							w.WriteHeader(400)
							fmt.Fprint(w, `{"error":"fixture step failure"}`)
							return
						}
						fmt.Fprint(w, `{"message":"ok"}`)
					}
				}
				mother := httptest.NewServer(handler(true))
				defer mother.Close()
				child := httptest.NewServer(handler(false))
				defer child.Close()
				var downstreamCalls atomic.Int32
				downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					downstreamCalls.Add(1)
					http.Error(w, "must not contact downstream", 500)
				}))
				defer downstream.Close()
				settings := model.DefaultSettings()
				settings.BaseURL = mother.URL
				settings.ProxyURL = child.URL
				if err = st.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
				admin := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "fixture-mother", TeamAccountID: "fixture-team"}, "mother-token", mother.URL)
				pro := model.DefaultProSettings()
				pro.Provider = provider
				pro.TargetAdminID = admin.ID
				pro.RetryCount = 0
				pro.Sub2.URL = downstream.URL
				pro.Sub2.Email = "admin@example.com"
				pro.CPA.URL = downstream.URL
				if _, err = st.SaveProSettings(pro, "fixture-password", "fixture-key"); err != nil {
					t.Fatal(err)
				}
				if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "child-token", RefreshToken: "child-rt"}); err != nil {
					t.Fatal(err)
				}
				if _, err = st.UpdateMailAccountManagementScope(email, "pro"); err != nil {
					t.Fatal(err)
				}
				if _, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.OAuthAccountID = "personal-account"
					p.OAuthUserID = "fixture-child"
					p.PushProvider = provider
					p.PushStatus = "completed"
					p.Sub2AccountID = 42
					p.CPAAuthFileName = "fixture.json"
					if failure == "team_removed" {
						p.InviteStatus = "completed"
						p.AcceptStatus = "completed"
						p.TransferStatus = "completed"
						p.SpaceMergedOnce = true
						p.RemoveStatus = "team_removed"
					}
				}); err != nil {
					t.Fatal(err)
				}
				got, err := s.performProMerge(context.Background(), email)
				if failure != "" && failure != "team_removed" {
					if err == nil || got.ProWorkflowRunning || stageState(got, failure) != "failed" {
						t.Fatalf("failed step not visible: %+v %v", got, err)
					}
					got, err = s.performProMerge(context.Background(), email)
				}
				if err != nil || got.ProWorkflowRunning || !got.SpaceMergedOnce || got.InviteStatus != "completed" || got.AcceptStatus != "completed" || got.TransferStatus != "completed" || got.RemoveStatus != "completed" {
					t.Fatalf("merge failed: %+v %v", got, err)
				}
				if got.PushStatus != "completed" || got.PushProvider != provider || got.Sub2AccountID != 42 || got.CPAAuthFileName != "fixture.json" || downstreamCalls.Load() != 0 {
					t.Fatalf("Pro downstream was touched: %+v calls=%d", got, downstreamCalls.Load())
				}
				mu.Lock()
				for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
					want := 1
					if failure == "team_removed" {
						want = 0
					} else if failure == stage {
						want = 2
					}
					if calls[stage] != want {
						t.Errorf("%s calls=%d want=%d", stage, calls[stage], want)
					}
				}
				mu.Unlock()
				if _, err = s.performProMerge(context.Background(), email); err != nil {
					t.Fatal("completed rerun failed", err)
				}
				if downstreamCalls.Load() != 0 {
					t.Fatal("completed rerun touched downstream")
				}
			})
		}
	}
}
