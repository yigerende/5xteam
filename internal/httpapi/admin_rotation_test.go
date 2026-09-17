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

func setRotationDisabled(t *testing.T, s *Server, id string, disabled bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/admin-accounts/"+id+"/rotation", strings.NewReader(fmt.Sprintf("{\"disabled\":%t}", disabled)))
	req.SetPathValue("id", id)
	out := httptest.NewRecorder()
	s.updateAdminRotation(out, req)
	if out.Code != 200 {
		t.Fatalf("toggle: %d %s", out.Code, out.Body.String())
	}
}

func TestAdminRotationPersistsAndPreservesCredentials(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	admin, err := st.SaveAdminAccountCredentials(model.AdminAccountProfile{Label: "mother", TeamAccountID: "team"}, "at", "rt")
	if err != nil {
		t.Fatal(err)
	}
	if admin.RotationDisabled {
		t.Fatal("new mothers must default to enabled")
	}
	setRotationDisabled(t, s, admin.ID, true)
	// A concurrent token refresh still holding the original profile must not re-enable scheduling.
	if _, err = st.SaveAdminAccountCredentials(admin, "new-at", "new-rt"); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.store = st
	saved, credentials, err := st.AdminAccountCredential(admin.ID)
	if err != nil || !saved.RotationDisabled || credentials.AccessToken != "new-at" || credentials.RefreshToken != "new-rt" {
		t.Fatalf("lost state/credentials: %+v %v", saved, err)
	}
	if !st.AdminAccounts()[0].RotationDisabled {
		t.Fatal("list lost disabled flag")
	}
	setRotationDisabled(t, s, admin.ID, false)
	if _, err = st.SaveAdminAccountCredentials(saved, "", ""); err != nil {
		t.Fatal(err)
	}
	saved, _, _ = st.AdminAccountCredential(admin.ID)
	if saved.RotationDisabled {
		t.Fatal("stale save undid re-enable")
	}
	for _, body := range []string{`{}`, `{"disabled":null}`, `{"disabled":"true"}`} {
		req := httptest.NewRequest("PUT", "/", strings.NewReader(body))
		req.SetPathValue("id", admin.ID)
		out := httptest.NewRecorder()
		s.updateAdminRotation(out, req)
		if out.Code != 400 {
			t.Fatalf("invalid toggle accepted: %s", body)
		}
	}
}

func TestAdminRotationKeepsOnlyCurrentInFlightWork(t *testing.T) {
	admin := model.AdminAccountProfile{ID: "mother", TeamAccountID: "team", RotationDisabled: true}
	base := model.FreeAccountProfile{ID: "child", CycleID: "cycle", AdminAccountID: "mother", TeamAccountID: "team"}
	for _, tc := range []struct {
		name, invite, accept, remove, taskStatus, cycle string
		remote                                          bool
		want                                            bool
	}{
		{"new", "pending", "pending", "pending", "", "", false, false},
		{"inviting", "running", "pending", "pending", "", "", false, true},
		{"invited", "completed", "failed", "pending", "", "", false, true},
		{"accepting", "failed", "running", "pending", "", "", false, true},
		{"inside", "completed", "completed", "pending", "", "", false, true},
		{"failed_before_invite", "failed", "pending", "pending", "", "", false, false},
		{"queued", "pending", "pending", "pending", "queued", "cycle", false, true},
		{"running", "failed", "pending", "pending", "running", "cycle", false, true},
		{"old_cycle", "pending", "pending", "pending", "queued", "old", false, false},
		{"failed_task", "pending", "pending", "pending", "failed", "cycle", false, false},
		{"removed", "completed", "completed", "completed", "running", "cycle", false, false},
		{"remote_removed", "completed", "completed", "pending", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := base
			child.InviteStatus, child.AcceptStatus, child.RemoveStatus = tc.invite, tc.accept, tc.remove
			if tc.remote {
				now := time.Now()
				child.RemoteRemovedAt = &now
			}
			tasks := []model.AutoRotationTask{{AccountID: child.ID, AdminAccountID: admin.ID, CycleID: tc.cycle, Status: tc.taskStatus}}
			if got := rotationAdminAllowed(admin, child, tasks); got != tc.want {
				t.Fatalf("allowed=%v want=%v", got, tc.want)
			}
		})
	}
	other := base
	other.AdminAccountID = "other"
	other.InviteStatus = "completed"
	if rotationAdminAllowed(admin, other, nil) {
		t.Fatal("different mother bypassed switch")
	}
}

func TestAdminRotationPlanningUsesFreshState(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 1000)}
	off := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "A", TeamAccountID: "off-team"}, "at", "")
	on := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "B", TeamAccountID: "on-team"}, "at", "")
	for _, a := range []model.AdminAccountProfile{off, on} {
		if err := st.SaveAdminCapacitySnapshot(a.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	stale := st.AdminAccounts()
	child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child"}, "at")
	if err != nil {
		t.Fatal(err)
	}
	setRotationDisabled(t, s, off.ID, true)
	if got := s.availablePremiumSlots(context.Background(), stale); got != 2 {
		t.Fatalf("disabled capacity counted: %d", got)
	}
	selected, err := s.selectPremiumAdmin(context.Background(), stale, child.ID, "")
	if err != nil || selected != on.ID {
		t.Fatalf("stale planner selected disabled mother: %s %v", selected, err)
	}
	setRotationDisabled(t, s, on.ID, true)
	if _, err = s.selectPremiumAdmin(context.Background(), stale, child.ID, ""); err == nil {
		t.Fatal("all-disabled selection succeeded")
	}
	saveImportLimitMail(t, st, "mail@example.com")
	for _, trigger := range []string{"manual", "automatic"} {
		run, _, err := s.startAutoRotation(context.Background(), trigger)
		if err != nil || run.Status != "skipped" {
			t.Fatalf("%s started with all disabled: %+v %v", trigger, run, err)
		}
	}
	run := model.AutoRotationRun{ID: "disabled-run", StartedAt: time.Now(), Status: "running"}
	if err := st.SaveAutoRotationRun(run); err != nil {
		t.Fatal(err)
	}
	s.executeAutoRotation(context.Background(), run, model.AutoRotationSettings{MaxPerRun: 10, Concurrency: 20})
	if len(st.FreeAccounts()) != 1 || len(st.AutoRotationTasks(run.ID)) != 0 {
		t.Fatal("disabled mother imported/queued new work")
	}
	setRotationDisabled(t, s, on.ID, false)
	if got := s.availablePremiumSlots(context.Background(), stale); got != 2 {
		t.Fatalf("enable did not restore capacity: %d", got)
	}
}

func TestAdminRotationDisabledJoinAndRemoval(t *testing.T) {
	for _, joinMethod := range []string{"mother_invite", "child_request"} {
		for _, removeMethod := range []string{"mother_kick", "child_leave"} {
			for _, existing := range []string{"pending", "invited", "queued"} {
				t.Run(joinMethod+"/"+removeMethod+"/"+existing, func(t *testing.T) {
					st, err := store.Open(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					defer st.Close()
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "application/json")
						if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/invites") {
							fmt.Fprint(w, `{"items":[{"id":"invite","email_address":"child@example.com"}]}`)
							return
						}
						fmt.Fprint(w, `{"message":"ok"}`)
					}))
					defer upstream.Close()
					settings := model.DefaultSettings()
					settings.BaseURL = upstream.URL
					if err := st.SaveSettings(settings); err != nil {
						t.Fatal(err)
					}
					rotation := model.DefaultAutoRotationSettings()
					rotation.TeamOperationIntervalSeconds = 0
					if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
						t.Fatal(err)
					}
					admin := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team"}, "at", upstream.URL)
					child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child"}, "child-at")
					if err != nil {
						t.Fatal(err)
					}
					child, err = st.UpdateFreeAccount(child.ID, func(p *model.FreeAccountProfile) {
						p.JoinMethod, p.RemoveMethod = joinMethod, removeMethod
						if existing == "invited" {
							p.AdminAccountID, p.TeamAccountID, p.InviteStatus = admin.ID, admin.TeamAccountID, "completed"
						}
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: child.Email}, model.MailAccountCredentials{Email: child.Email, AccessToken: "child-at"}); err != nil {
						t.Fatal(err)
					}
					s, err := New(st, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					if existing == "queued" {
						if err := st.SaveAutoRotationTask(model.AutoRotationTask{ID: "queued", RunID: "run", AccountID: child.ID, CycleID: child.CycleID, AdminAccountID: admin.ID, Status: "queued"}); err != nil {
							t.Fatal(err)
						}
					}
					setRotationDisabled(t, s, admin.ID, true)
					req := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf("{\"admin_account_id\":%q,\"seat_type\":\"prolite\"}", admin.ID)))
					req.SetPathValue("id", child.ID)
					out := httptest.NewRecorder()
					s.joinFreeAccount(out, req)
					if existing == "pending" {
						if out.Code != 409 || calls.Load() != 0 {
							t.Fatalf("new join not blocked before remote calls: %d calls=%d %s", out.Code, calls.Load(), out.Body.String())
						}
						saved, _, _ := st.FreeAccountCredential(child.ID)
						if saved.InviteStatus == "running" || saved.AdminAccountID != "" {
							t.Fatal("rejected join changed child state")
						}
						return
					}
					if out.Code != 200 {
						t.Fatalf("in-flight join blocked: %d %s", out.Code, out.Body.String())
					}
					beforeRemove := calls.Load()
					removed, err := s.performFreeAccountRemove(context.Background(), child.ID)
					if err != nil || removed.RemoveStatus != "completed" || calls.Load() != beforeRemove+1 {
						t.Fatalf("existing removal blocked: %+v %v", removed, err)
					}
				})
			}
		}
	}
}

func TestAdminRotationDisableWhileInvitationRuns(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/invites") {
			once.Do(func() { close(entered) })
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"ok"}`)
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	rotation := model.DefaultAutoRotationSettings()
	rotation.TeamOperationIntervalSeconds = 0
	if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
		t.Fatal(err)
	}
	admin := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team"}, "at", upstream.URL)
	child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child"}, "child-at")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf("{\"admin_account_id\":%q,\"seat_type\":\"prolite\"}", admin.ID)))
		req.SetPathValue("id", child.ID)
		out := httptest.NewRecorder()
		s.joinFreeAccount(out, req)
		done <- out
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("invitation did not start")
	}
	// Toggle must not wait for the mother's network lock.
	toggled := make(chan struct{})
	go func() { setRotationDisabled(t, s, admin.ID, true); close(toggled) }()
	select {
	case <-toggled:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("toggle blocked on active invitation")
	}
	close(release)
	select {
	case out := <-done:
		if out.Code != 200 {
			t.Fatalf("active invitation interrupted: %s", out.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("join did not finish")
	}
	saved, _, _ := st.FreeAccountCredential(child.ID)
	if saved.AcceptStatus != "completed" {
		t.Fatal("in-flight account did not enter")
	}
	if rotationAdminAllowed(st.AdminAccounts()[0], model.FreeAccountProfile{ID: "new"}, nil) {
		t.Fatal("new work allowed after disable")
	}
}
