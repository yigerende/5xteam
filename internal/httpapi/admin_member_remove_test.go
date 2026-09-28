package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/cpa"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

type memberRemovalFixture struct {
	s        *Server
	admin    model.AdminAccountProfile
	mu       sync.Mutex
	members  map[string]string
	requests []string
	fail     bool
	hook     func(*http.Request)
}

func newMemberRemovalFixture(t *testing.T) *memberRemovalFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &memberRemovalFixture{s: &Server{store: st, sub2: sub2.New(), cpa: cpa.New(), auditQueue: make(chan model.AutoRotationEvent, 512)}, members: map[string]string{"child": "standard-user", "other": "standard-user", "owner": "account-owner"}}
	handler := func(source string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if source == "mother" && r.Header.Get("Authorization") != "Bearer mother-at" {
				t.Error("mother credential not used")
			}
			if source == "child" && r.Header.Get("Authorization") != "Bearer child-at" {
				t.Error("latest mail AT not used")
			}
			if r.Method == "GET" {
				if source != "mother" {
					t.Error("member query used global proxy")
				}
				name := strings.TrimSuffix(r.URL.Query().Get("query"), "@example.com")
				f.mu.Lock()
				role, exists := f.members[name]
				f.mu.Unlock()
				items := []map[string]any{}
				if exists {
					items = append(items, map[string]any{"id": name, "email": name + "@example.com", "role": role, "seat_type": "prolite"})
				}
				json.NewEncoder(w).Encode(map[string]any{"items": items, "total": len(items)})
				return
			}
			if r.Method != "DELETE" || !strings.Contains(r.URL.Path, "/users/") {
				t.Error("unexpected mutation")
			}
			if f.hook != nil {
				f.hook(r)
			}
			f.mu.Lock()
			f.requests = append(f.requests, source+":"+r.URL.Path)
			if f.fail {
				f.mu.Unlock()
				http.Error(w, "fixture 429", 429)
				return
			}
			parts := strings.Split(r.URL.Path, "/")
			delete(f.members, parts[len(parts)-1])
			f.mu.Unlock()
			w.WriteHeader(204)
		}
	}
	mother := httptest.NewServer(handler("mother"))
	t.Cleanup(mother.Close)
	child := httptest.NewServer(handler("child"))
	t.Cleanup(child.Close)
	settings := model.DefaultSettings()
	settings.BaseURL, settings.ProxyURL = "http://127.0.0.1:1/backend-api", child.URL
	settings.NetworkRetryCount = 0
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	rotation := model.DefaultAutoRotationSettings()
	rotation.TeamOperationIntervalSeconds = 0
	if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
		t.Fatal(err)
	}
	f.admin = saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", Email: "mother@example.com", TeamAccountID: "team", RotationDisabled: true}, "mother-at", mother.URL)
	return f
}

func (f *memberRemovalFixture) child(t *testing.T, name, team string, mail bool) model.FreeAccountProfile {
	t.Helper()
	p, _, err := f.s.store.SaveImportedFreeAccount(model.FreeAccountProfile{Email: name + "@example.com", UserID: name}, "stale-at")
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.store.UpdateFreeAccount(p.ID, func(v *model.FreeAccountProfile) {
		v.AdminAccountID = f.admin.ID
		v.TeamAccountID = team
		v.InviteStatus = "completed"
		v.AcceptStatus = "completed"
		v.RemoveStatus = "pending"
		v.RemoveMethod = "mother_kick"
	})
	if err != nil {
		t.Fatal(err)
	}
	if mail {
		_, err = f.s.store.SaveMailAccount(model.MailAccountProfile{Email: p.Email}, model.MailAccountCredentials{Email: p.Email, AccessToken: "child-at"})
		if err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (f *memberRemovalFixture) remove(ctx context.Context, name, method string, progress func(string, string)) error {
	return f.s.performAdminMemberRemoval(ctx, f.admin.ID, adminMemberRemoveInput{Email: name + "@example.com", UserID: name, TeamID: "team", Method: method}, progress)
}

func TestAdminMemberRemoveMethodsAndState(t *testing.T) {
	for _, method := range []string{"mother_kick", "child_leave"} {
		for _, local := range []string{"current", "untracked", "other_team"} {
			t.Run(method+"/"+local, func(t *testing.T) {
				f := newMemberRemovalFixture(t)
				var p model.FreeAccountProfile
				if local != "untracked" {
					team := "team"
					if local == "other_team" {
						team = "elsewhere"
					}
					p = f.child(t, "child", team, true)
				} else if method == "child_leave" {
					_, err := f.s.store.SaveMailAccount(model.MailAccountProfile{Email: "child@example.com"}, model.MailAccountCredentials{Email: "child@example.com", AccessToken: "child-at"})
					if err != nil {
						t.Fatal(err)
					}
				}
				var stages []string
				if err := f.remove(t.Context(), "child", method, func(s, m string) { stages = append(stages, s) }); err != nil {
					t.Fatal(err)
				}
				want := "mother"
				if method == "child_leave" {
					want = "child"
				}
				if len(f.requests) != 1 || !strings.HasPrefix(f.requests[0], want+":") {
					t.Fatalf("requests=%v", f.requests)
				}
				if strings.Join(stages, ",") == "" || stages[0] != "waiting" || stages[len(stages)-1] != "cooldown" {
					t.Fatalf("stages=%v", stages)
				}
				if local != "untracked" {
					got, _, _ := f.s.store.FreeAccountCredential(p.ID)
					if local == "current" {
						if got.RemoveStatus != "completed" || got.RemoveMethod != method || got.RemoteRemovedAt == nil || !got.DownstreamCleaned {
							t.Fatalf("bad completion: %+v", got)
						}
					} else if got.TeamAccountID != "elsewhere" || got.RemoveStatus != "pending" {
						t.Fatal("other cycle changed")
					}
				}
				if f.s.store.AutoRotationSettings().RemoveMethod != "mother_kick" {
					t.Fatal("global method changed")
				}
				if err := f.remove(t.Context(), "child", method, func(string, string) {}); err != nil {
					t.Fatal(err)
				}
				if len(f.requests) != 1 {
					t.Fatal("duplicate mutation")
				}
				found := false
				for len(f.s.auditQueue) > 0 {
					event := <-f.s.auditQueue
					if event.Stage == "member_remove_success" {
						found = event.Details["proxy"] != nil && event.Details["remove_method"] == method
					}
				}
				if !found {
					t.Fatal("missing operation/proxy audit")
				}
			})
		}
	}
}

func TestAdminMemberRemoveGuardsAndFailure(t *testing.T) {
	for _, scenario := range []string{"owner", "identity_changed", "no_at", "no_global_proxy", "no_mother_proxy", "wrong_team", "429"} {
		t.Run(scenario, func(t *testing.T) {
			f := newMemberRemovalFixture(t)
			p := f.child(t, "child", "team", true)
			input := adminMemberRemoveInput{Email: p.Email, UserID: p.UserID, TeamID: "team", Method: "child_leave"}
			switch scenario {
			case "owner":
				input.Email = "owner@example.com"
				input.UserID = "owner"
			case "identity_changed":
				input.UserID = "spoof"
			case "no_at":
				input.Email = "other@example.com"
				input.UserID = "other"
			case "no_global_proxy":
				settings := f.s.store.Settings()
				settings.ProxyURL = ""
				f.s.store.SaveSettings(settings)
			case "no_mother_proxy":
				f.s.store.UpdateAdminAccountProxy(f.admin.ID, "")
			case "wrong_team":
				input.TeamID = "wrong-team"
			case "429":
				f.fail = true
			}
			if err := f.s.performAdminMemberRemoval(t.Context(), f.admin.ID, input, func(string, string) {}); err == nil {
				t.Fatal("guard accepted")
			}
			want := 0
			if scenario == "429" {
				want = 1
			}
			if len(f.requests) != want {
				t.Fatalf("unexpected mutation: %v", f.requests)
			}
			got, _, _ := f.s.store.FreeAccountCredential(p.ID)
			if got.RemoveMethod != "mother_kick" || got.RemoteRemovedAt != nil {
				t.Fatal("failure changed policy or marked removed")
			}
			if scenario == "429" && got.RemoveStatus != "failed" {
				t.Fatal("failure state missing")
			}
		})
	}
}

func TestAdminMemberRemoveSharesTeamLockAndCooldown(t *testing.T) {
	f := newMemberRemovalFixture(t)
	f.child(t, "child", "team", true)
	other := f.child(t, "other", "team", true)
	rotation := f.s.store.AutoRotationSettings()
	rotation.TeamOperationIntervalSeconds = 1
	f.s.store.SaveAutoRotationSettings(rotation)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.hook = func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/child") {
			once.Do(func() { close(started) })
			<-release
		}
	}
	done := make(chan error, 2)
	go func() { done <- f.remove(context.Background(), "child", "child_leave", func(string, string) {}) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("leave did not start")
	}
	go func() { _, err := f.s.performFreeAccountRemove(context.Background(), other.ID); done <- err }()
	time.Sleep(80 * time.Millisecond)
	f.mu.Lock()
	before := len(f.requests)
	f.mu.Unlock()
	if before != 0 {
		t.Fatal("step 6 overlapped member leave")
	}
	released := time.Now()
	close(release)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("removal timeout")
		}
	}
	if time.Since(released) < 1900*time.Millisecond {
		t.Fatal("shared cooldown skipped")
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests=%v", f.requests)
	}
}

func TestAdminMemberRemovePersistsAfterDisconnect(t *testing.T) {
	f := newMemberRemovalFixture(t)
	p := f.child(t, "child", "team", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.hook = func(*http.Request) { cancel() }
	if err := f.remove(ctx, "child", "mother_kick", func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.s.store.FreeAccountCredential(p.ID)
	if got.RemoveStatus != "completed" {
		t.Fatal("disconnect abandoned persistence")
	}
}

func TestAdminMemberRemoveConcurrentIdentityAndTeams(t *testing.T) {
	for _, sameAccount := range []bool{true, false} {
		t.Run(fmt.Sprint(sameAccount), func(t *testing.T) {
			f := newMemberRemovalFixture(t)
			f.child(t, "child", "team", true)
			admin2 := f.admin
			if !sameAccount {
				admin2 = saveTestAdmin(t, f.s.store, model.AdminAccountProfile{Label: "second", TeamAccountID: "team-2", ProxyID: f.admin.ProxyID}, "mother-at", "")
				f.child(t, "other", "team-2", true)
			}
			var mu sync.Mutex
			active, maximum := 0, 0
			both := make(chan struct{})
			f.hook = func(*http.Request) {
				mu.Lock()
				active++
				if active > maximum {
					maximum = active
				}
				if active == 2 {
					close(both)
				}
				mu.Unlock()
				if !sameAccount {
					select {
					case <-both:
					case <-time.After(3 * time.Second):
						t.Error("different mothers blocked")
					}
				}
				time.Sleep(40 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
			}
			start, done := make(chan struct{}), make(chan error, 2)
			go func() { <-start; done <- f.remove(t.Context(), "child", "child_leave", func(string, string) {}) }()
			go func() {
				<-start
				name, team := "other", "team-2"
				if sameAccount {
					name, team = "child", "team"
				}
				done <- f.s.performAdminMemberRemoval(t.Context(), admin2.ID, adminMemberRemoveInput{Email: name + "@example.com", UserID: name, TeamID: team, Method: "mother_kick"}, func(string, string) {})
			}()
			close(start)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			want := 2
			if sameAccount {
				want = 1
			}
			if maximum != want || len(f.requests) != want {
				t.Fatalf("maximum=%d requests=%v", maximum, f.requests)
			}
		})
	}
}

func TestAdminMemberRemoveCleanupRetryDoesNotRepeatOpenAI(t *testing.T) {
	f := newMemberRemovalFixture(t)
	p := f.child(t, "child", "team", true)
	var deletes int
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case strings.HasSuffix(r.URL.Path, "/stats"):
			fmt.Fprint(w, `{"code":0,"data":{"summary":{"total_standard_cost":12,"total_user_cost":7}}}`)
		case r.Method == "DELETE":
			deletes++
			if deletes == 1 {
				http.Error(w, "cleanup failure", 502)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		default:
			t.Errorf("unexpected downstream %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer down.Close()
	settings := model.DefaultSub2Settings()
	settings.URL, settings.Email = down.URL, "fixture@example.com"
	if _, err := f.s.store.SaveSub2Settings(settings, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.Sub2AccountID = 42; p.PushProvider = "sub2" }); err != nil {
		t.Fatal(err)
	}
	if err := f.remove(t.Context(), "child", "child_leave", func(string, string) {}); err == nil || !strings.Contains(err.Error(), "已移出") {
		t.Fatalf("cleanup error=%v", err)
	}
	got, _, _ := f.s.store.FreeAccountCredential(p.ID)
	if got.RemoveStatus != "cleanup_pending" || got.RemoteRemovedAt == nil {
		t.Fatal("lost remote success")
	}
	if err := f.remove(t.Context(), "child", "child_leave", func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = f.s.store.FreeAccountCredential(p.ID)
	if deletes != 2 || len(f.requests) != 1 || got.RemoveStatus != "completed" || got.TotalCostUSD != 12 {
		t.Fatal("cleanup retry repeated mutation or lost cost")
	}
}

func TestAdminMemberRemoveStreamsProgress(t *testing.T) {
	f := newMemberRemovalFixture(t)
	for _, method := range []string{"invalid", "mother_kick"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"email":"child@example.com","user_id":"child","team_account_id":"team","method":%q}`, method)))
		r.SetPathValue("id", f.admin.ID)
		w := httptest.NewRecorder()
		f.s.removeAdminMember(w, r)
		if method == "invalid" {
			if w.Code != 400 {
				t.Fatal("invalid method accepted")
			}
			continue
		}
		if w.Header().Get("Content-Type") != "application/x-ndjson" || !w.Flushed {
			t.Fatal("progress not streamed")
		}
		lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
		var event map[string]any
		for _, line := range lines {
			if json.Unmarshal([]byte(line), &event) != nil {
				t.Fatal("invalid stream")
			}
		}
		if event["type"] != "done" {
			t.Fatal(w.Body.String())
		}
	}
}
