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
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestChildApprovalReconciliationKeepsTeamLockAndCooldown(t *testing.T) {
	for _, sameTeam := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_team_%v", sameTeam), func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			kicked := make(chan time.Time, 1)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/invites"):
					fmt.Fprint(w, `{"items":[]}`)
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/users"):
					close(entered)
					<-release
					fmt.Fprint(w, `{"items":[{"id":"joining","email":"joining@example.com","seat_type":"prolite"}]}`)
				case r.Method == "DELETE":
					kicked <- time.Now()
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			defer unblock()
			settings := model.DefaultSettings()
			settings.BaseURL = upstream.URL
			if err := st.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			rotation := model.DefaultAutoRotationSettings()
			rotation.TeamOperationIntervalSeconds = 1
			if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
				t.Fatal(err)
			}
			mother := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team"}, "mother-at", upstream.URL)
			other := mother
			if !sameTeam {
				other = saveTestAdmin(t, st, model.AdminAccountProfile{Label: "other", TeamAccountID: "other-team"}, "other-at", upstream.URL)
			}
			makeChild := func(email string, admin model.AdminAccountProfile, accepted bool) model.FreeAccountProfile {
				p, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "child-at")
				if err != nil {
					t.Fatal(err)
				}
				p, err = st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
					p.AdminAccountID, p.TeamAccountID, p.JoinMethod, p.RemoveMethod = admin.ID, admin.TeamAccountID, "child_request", "mother_kick"
					p.InviteStatus, p.AcceptStatus = "completed", "pending"
					if accepted {
						p.AcceptStatus = "completed"
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			joining, leaving := makeChild("joining@example.com", mother, false), makeChild("leaving@example.com", other, true)
			s, err := New(st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			defer unblock()
			joinedDone, removedDone := make(chan error, 1), make(chan error, 1)
			go func() {
				joinedDone <- s.invokeFreeHandler(context.Background(), "join", joining.ID, map[string]any{"admin_account_id": mother.ID, "seat_type": "prolite"})
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("member query did not start")
			}
			go func() { _, err := s.performFreeAccountRemove(context.Background(), leaving.ID); removedDone <- err }()
			if sameTeam {
				select {
				case <-kicked:
					t.Fatal("removal overlapped membership reconciliation")
				case <-time.After(150 * time.Millisecond):
				}
			} else {
				select {
				case <-kicked:
				case <-time.After(3 * time.Second):
					t.Fatal("different Team unnecessarily blocked")
				}
			}
			releasedAt := time.Now()
			unblock()
			if sameTeam {
				select {
				case kickedAt := <-kicked:
					if kickedAt.Sub(releasedAt) < 900*time.Millisecond {
						t.Fatal("reconciled approval skipped configured cooldown")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("removal did not resume")
				}
			}
			for _, done := range []chan error{joinedDone, removedDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("operation did not finish")
				}
			}
		})
	}
}

func TestChildApprovalReconcilesActualMembership(t *testing.T) {
	for _, mode := range []string{"manual", "automatic"} {
		for _, tc := range []struct {
			name, approval, member string
			missing, success       bool
			wantPatch, wantQuery   int32
		}{
			{"normal", "ok", "prolite", false, true, 1, 0},
			{"404_already_joined", "404", "prolite", false, true, 1, 1},
			{"lost_response", "disconnect", "prolite", false, true, 1, 1},
			{"missing_request", "", "prolite", true, true, 0, 1},
			{"absent", "404", "absent", false, false, 1, 1},
			{"lookup_denied", "404", "denied", false, false, 1, 1},
			{"wrong_seat", "404", "default", false, false, 1, 1},
			{"inactive", "404", "inactive", false, false, 1, 1},
			{"missing_and_absent", "", "absent", true, false, 0, 1},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				st, err := store.Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				var patches, queries, globalCalls atomic.Int32
				global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					globalCalls.Add(1)
					http.Error(w, "must use mother's dedicated proxy", 500)
				}))
				defer global.Close()
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer mother-at" || r.Header.Get("chatgpt-account-id") != "team" {
						t.Error("wrong mother credentials")
					}
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.Method == "GET" && r.URL.Path == "/accounts/team/invites":
						if tc.missing {
							fmt.Fprint(w, `{"items":[]}`)
						} else {
							fmt.Fprint(w, `{"items":[{"id":"request-1","email_address":"child@example.com"}]}`)
						}
					case r.Method == "PATCH" && r.URL.Path == "/accounts/team/invites/request-1":
						patches.Add(1)
						if tc.approval == "disconnect" {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							conn.Close()
							return
						}
						if tc.approval == "404" {
							http.Error(w, "Invite not found", 404)
						} else {
							fmt.Fprint(w, `{}`)
						}
					case r.Method == "GET" && r.URL.Path == "/accounts/team/users":
						queries.Add(1)
						if r.URL.Query().Get("query") != "child@example.com" {
							t.Error("missing full email search")
						}
						switch tc.member {
						case "absent":
							fmt.Fprint(w, `{"items":[]}`)
						case "denied":
							http.Error(w, "denied", 403)
						case "inactive":
							fmt.Fprint(w, `{"items":[{"id":"child","email":"child@example.com","seat_type":"prolite","deactivated_time":"2026-09-27T00:00:00Z"}]}`)
						default:
							fmt.Fprintf(w, `{"items":[{"id":"child","email":"child@example.com","seat_type":%q,"deactivated_time":null}]}`, tc.member)
						}
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
						http.NotFound(w, r)
					}
				}))
				defer upstream.Close()
				settings := model.DefaultSettings()
				settings.BaseURL, settings.ProxyURL = upstream.URL, global.URL
				settings.NetworkRetryCount, settings.NetworkRetryInterval = 2, 0
				if err := st.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
				rotation := model.DefaultAutoRotationSettings()
				rotation.TeamOperationIntervalSeconds = 0
				if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
					t.Fatal(err)
				}
				admin := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team"}, "mother-at", upstream.URL)
				child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child"}, "child-at")
				if err != nil {
					t.Fatal(err)
				}
				_, err = st.UpdateFreeAccount(child.ID, func(p *model.FreeAccountProfile) {
					p.AdminAccountID, p.TeamAccountID, p.JoinMethod = admin.ID, "team", "child_request"
					p.InviteStatus, p.AcceptStatus, p.Status = "completed", "failed", "accept_failed"
				})
				if err != nil {
					t.Fatal(err)
				}
				s, err := New(st, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				var succeeded bool
				if mode == "automatic" {
					succeeded = s.invokeFreeHandler(context.Background(), "join", child.ID, map[string]any{"admin_account_id": admin.ID, "seat_type": "prolite"}) == nil
				} else {
					req := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"admin_account_id":%q,"seat_type":"prolite"}`, admin.ID)))
					req.SetPathValue("id", child.ID)
					out := httptest.NewRecorder()
					s.joinFreeAccount(out, req)
					succeeded = out.Code == 200
				}
				p, _, _ := st.FreeAccountCredential(child.ID)
				if succeeded != tc.success || (p.AcceptStatus == "completed") != tc.success {
					t.Fatalf("success=%v profile=%+v", succeeded, p)
				}
				if tc.success && (p.JoinedAt == nil || p.Status != "joined" || p.LastError != "" || p.VisitedTeamCount != 1) {
					t.Fatalf("membership not persisted: %+v", p)
				}
				if !tc.success && (p.JoinedAt != nil || p.LastError == "") {
					t.Fatalf("failure incorrectly reconciled: %+v", p)
				}
				if patches.Load() != tc.wantPatch || queries.Load() != tc.wantQuery || globalCalls.Load() != 0 {
					t.Fatalf("patch=%d query=%d global=%d", patches.Load(), queries.Load(), globalCalls.Load())
				}
				if err := s.flushAuditEvents(context.Background()); err != nil {
					t.Fatal(err)
				}
				var confirmed bool
				for _, e := range st.AutoRotationEventsByAccount(child.ID) {
					if e.Stage == "member_reconcile_success" {
						confirmed = true
					}
				}
				if confirmed != (tc.success && tc.wantQuery > 0) {
					t.Fatal("missing/incorrect membership reconciliation audit")
				}
			})
		}
	}
}
