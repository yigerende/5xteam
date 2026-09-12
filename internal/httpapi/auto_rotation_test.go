package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestAutoRotationSkipsOverlappingScheduledRun(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.autoRunning = true
	run, started, err := s.startAutoRotation(context.Background(), "automatic")
	if err != nil {
		t.Fatal(err)
	}
	if started || run.Status != "running" {
		t.Fatalf("overlapping run was not rejected: started=%v run=%+v", started, run)
	}
	s.autoRunning = false
}

func TestAutoRotationSkipsWithoutCapacitySnapshot(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveAutoRotationSettings(model.AutoRotationSettings{Enabled: true, ThresholdPercent: 50, IntervalSeconds: 60, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	run, started, err := s.startAutoRotation(context.Background(), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !started || run.Status != "skipped" || run.Reason != "没有可用的席位统计快照，请先在 Team 账号管理页面刷新 5x 席位" {
		t.Fatalf("unexpected no-snapshot decision: started=%v run=%+v", started, run)
	}
}

func TestAverageFreeQuotaUsesOnlyInsideAccountsAndSeatTotal(t *testing.T) {
	usedA, usedB, usedRemoved := 20.0, 60.0, 0.0
	got := averageFreeQuota([]model.FreeAccountProfile{
		{AcceptStatus: "completed", RemoveStatus: "pending", Quota7D: &model.FreeQuotaWindow{UsedPercent: usedA}},
		{AcceptStatus: "completed", RemoveStatus: "pending", Quota7D: &model.FreeQuotaWindow{UsedPercent: usedB}},
		{AcceptStatus: "completed", RemoveStatus: "completed", Quota7D: &model.FreeQuotaWindow{UsedPercent: usedRemoved}},
		{AcceptStatus: "pending", Quota7D: &model.FreeQuotaWindow{UsedPercent: 0}},
	}, 4)
	if got != 30 {
		t.Fatalf("average=%v, want 30", got)
	}
}

func TestAverageFreeQuotaReturnsUnknownWithoutQuota(t *testing.T) {
	if got := averageFreeQuota([]model.FreeAccountProfile{{AcceptStatus: "completed"}}, 4); got >= 0 {
		t.Fatalf("expected unknown, got %v", got)
	}
}

func TestEmptySpaceBootstrapDecision(t *testing.T) {
	for _, tc := range []struct {
		name           string
		accounts       []model.FreeAccountProfile
		total, pending int
		wantAverage    float64
		wantRun        bool
	}{
		{"empty", nil, 19, 0, 0, true},
		{"only_removed", []model.FreeAccountProfile{{AcceptStatus: "completed", RemoveStatus: "completed", Quota7D: &model.FreeQuotaWindow{UsedPercent: 0}}}, 19, 0, 0, true},
		{"invitations_reserve_all", nil, 19, 19, 0, false},
		{"invitations_reserve_some", nil, 19, 18, 0, true},
		{"zero_capacity", nil, 0, 0, 0, false},
		{"inside_without_capacity", []model.FreeAccountProfile{{AcceptStatus: "completed", Quota7D: &model.FreeQuotaWindow{UsedPercent: 50}}}, 0, 0, -1, false},
		{"inside_missing_quota", []model.FreeAccountProfile{{AcceptStatus: "completed"}}, 19, 0, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			avg := averageFreeQuota(tc.accounts, tc.total)
			allowed, _ := autoRotationDecision(avg, 50, availablePremiumFromSnapshot(tc.total, 0, tc.pending))
			if avg != tc.wantAverage || allowed != tc.wantRun {
				t.Fatalf("average=%v allowed=%v", avg, allowed)
			}
		})
	}
}

func TestAutoRotationStartsWithoutSpaceQuota(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, err := st.SaveAdminAccount(model.AdminAccountProfile{Email: "admin@example.com", TeamAccountID: "team-1"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveAdminCapacitySnapshot(admin.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 19}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 200)}
	run, started, err := s.startAutoRotation(context.Background(), "automatic")
	if err != nil || !started || run.Status != "running" || run.AveragePercent != 0 || !strings.Contains(run.Reason, "空空间") {
		t.Fatalf("started=%v run=%+v err=%v", started, run, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.autoMu.Lock()
		running := s.autoRunning
		s.autoMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("empty candidate run did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEmptyRotationImportsMailboxCandidatesWithinLimits(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		seats, max, mail, rotation, wantMail int
	}{
		{"max_per_run", 4, 2, 5, 0, 2},
		{"seat_limit", 2, 10, 5, 0, 2},
		{"mail_shortage", 4, 0, 1, 0, 1},
		{"no_capacity", 0, 2, 5, 0, 0},
		{"rotation_first", 4, 2, 5, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			// No Team ID: stop at the invitation precondition without any remote calls.
			admin, err := st.SaveAdminAccount(model.AdminAccountProfile{Email: "admin@example.com"}, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SaveAdminCapacitySnapshot(admin.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: tc.seats}}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.mail; i++ {
				email := fmt.Sprintf("mail-%d@example.com", i)
				claims, err := json.Marshal(map[string]any{
					"https://api.openai.com/auth":    map[string]string{"chatgpt_user_id": email, "chatgpt_account_id": email, "chatgpt_plan_type": "free"},
					"https://api.openai.com/profile": map[string]string{"email": email},
				})
				if err != nil {
					t.Fatal(err)
				}
				token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
				if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: token}); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < tc.rotation; i++ {
				email := fmt.Sprintf("rotation-%d@example.com", i)
				if _, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, turbIntegrationTestToken(email)); err != nil {
					t.Fatal(err)
				}
			}
			run := model.AutoRotationRun{ID: "empty-bootstrap", Status: "running", StartedAt: time.Now()}
			if err := st.SaveAutoRotationRun(run); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 500)}
			s.executeAutoRotation(context.Background(), run, model.AutoRotationSettings{MaxPerRun: tc.max, Concurrency: 2})
			mailCount := 0
			for _, account := range st.FreeAccounts() {
				if strings.HasPrefix(account.Email, "mail-") {
					mailCount++
				}
			}
			if mailCount != tc.wantMail {
				t.Fatalf("imported mailbox candidates=%d, want %d", mailCount, tc.wantMail)
			}
			if tasks := st.AutoRotationTasks(run.ID); len(tasks) != tc.wantMail+tc.rotation {
				t.Fatalf("planned tasks=%d, want %d", len(tasks), tc.wantMail+tc.rotation)
			}
		})
	}
}

func TestSeatUsageForAdminSnapshotCountsInsideAndPendingOnce(t *testing.T) {
	accounts := []model.FreeAccountProfile{
		{ID: "standard-inside", AdminAccountID: "admin-1", SeatType: "default", AcceptStatus: "completed"},
		{ID: "standard-pending", AdminAccountID: "admin-1", SeatType: "default", InviteStatus: "completed"},
		{ID: "premium-inside", AdminAccountID: "admin-1", SeatType: "prolite", AcceptStatus: "completed"},
		{ID: "premium-pending", AdminAccountID: "admin-1", SeatType: "prolite", InviteStatus: "running"},
		{ID: "premium-queued", SeatType: "prolite"},
		{ID: "removed", AdminAccountID: "admin-1", SeatType: "prolite", AcceptStatus: "completed", RemoveStatus: "completed"},
	}
	tasks := []model.AutoRotationTask{
		{AccountID: "premium-pending", AdminAccountID: "admin-1", SeatType: "prolite", Status: "running"},
		{AccountID: "premium-queued", AdminAccountID: "admin-1", SeatType: "prolite", Status: "queued"},
	}
	standardInside, standardPending := seatUsageForAdminSnapshot("admin-1", false, accounts, tasks)
	if standardInside != 1 || standardPending != 1 {
		t.Fatalf("standard usage=%d/%d want=1/1", standardInside, standardPending)
	}
	premiumInside, premiumPending := seatUsageForAdminSnapshot("admin-1", true, accounts, tasks)
	if premiumInside != 1 || premiumPending != 2 {
		t.Fatalf("premium usage=%d/%d want=1/2", premiumInside, premiumPending)
	}
}

func TestAutoRotationPlanLimitsByMaxSeatsAndCandidates(t *testing.T) {
	cases := []struct {
		name                                    string
		max, remote, reserved, candidates, want int
	}{
		{"max first", 3, 10, 2, 20, 3},
		{"seat shortage", 10, 4, 3, 20, 1},
		{"candidate shortage", 10, 10, 0, 2, 2},
		{"zero max", 0, 5, 1, 20, 4},
		{"reserved over remote", 10, 1, 4, 20, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoRotationPlan(tc.max, tc.remote, tc.reserved, tc.candidates); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestAutoRotationDecisionSkipsWhenThresholdReachedButNoSeats(t *testing.T) {
	if run, reason := autoRotationDecision(48, 50, 0); run || reason != "已达到额度阈值，但当前没有可用的 5x 剩余席位" {
		t.Fatalf("unexpected decision: run=%v reason=%q", run, reason)
	}
	if run, _ := autoRotationDecision(48, 50, 2); !run {
		t.Fatal("expected run when seats are available")
	}
	if run, _ := autoRotationDecision(60, 50, 2); run {
		t.Fatal("should skip above threshold")
	}
}

func TestPremiumSeatSnapshotCountsOnlyConfiguredAdminsAndInsidePremium(t *testing.T) {
	admins := []model.AdminAccountProfile{{ID: "a"}, {ID: "b"}, {ID: "missing"}}
	snapshots := map[string]model.AdminSeatCapacity{
		"a":      {Premium: model.AdminSeatBucket{Total: 9}},
		"b":      {Premium: model.AdminSeatBucket{Total: 4}},
		"orphan": {Premium: model.AdminSeatBucket{Total: 100}},
	}
	accounts := []model.FreeAccountProfile{
		{AcceptStatus: "completed", RemoveStatus: "pending", SeatType: "prolite"},
		{AcceptStatus: "completed", RemoveStatus: "pending", SeatType: "5x"},
		{AcceptStatus: "completed", RemoveStatus: "pending", SeatType: "default"},
		{AcceptStatus: "completed", RemoveStatus: "completed", SeatType: "prolite"},
		{AcceptStatus: "pending", RemoveStatus: "pending", SeatType: "prolite"},
	}
	total, inside, found := premiumSeatSnapshot(admins, snapshots, accounts)
	if total != 13 || inside != 2 || found != 2 {
		t.Fatalf("got total=%d inside=%d found=%d", total, inside, found)
	}
}

func TestAvailablePremiumFromSnapshotIncludesInFlightReservations(t *testing.T) {
	if got := availablePremiumFromSnapshot(9, 8, 1); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
	if got := availablePremiumFromSnapshot(9, 8, 0); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	if got := availablePremiumFromSnapshot(2, 4, 0); got != 0 {
		t.Fatalf("negative availability should clamp to zero, got %d", got)
	}
}

func TestAutoRotationNeverSelectsDeadAccounts(t *testing.T) {
	ready := model.FreeAccountProfile{ImportMode: "", InviteStatus: "pending", AcceptStatus: "pending", RemoveStatus: "pending"}
	if !eligibleAutoRotationAccount(ready) {
		t.Fatal("normal queued account should be eligible")
	}
	ready.Dead = true
	if eligibleAutoRotationAccount(ready) {
		t.Fatal("dead Team account must not be selected again")
	}
	if eligibleAutoRotationMail(model.MailAccountProfile{ChatGPTStatus: "dead"}) {
		t.Fatal("mail account with dead ChatGPT status must not be selected")
	}
	if eligibleAutoRotationMail(model.MailAccountProfile{RegistrationStatus: "dead"}) {
		t.Fatal("legacy dead mail status must not be selected")
	}
	if !eligibleAutoRotationMail(model.MailAccountProfile{RegistrationStatus: "success"}) {
		t.Fatal("normal registered mailbox should remain eligible")
	}
}

func TestAutoRotationRetryExhaustionRemovesJoinedAccount(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var kickCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/accounts/team-cleanup/users/user-cleanup") {
			kickCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":"removed"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	admin, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "cleanup-admin", TeamAccountID: "team-cleanup"}, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "cleanup@example.com", UserID: "user-cleanup"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = st.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID, item.TeamAccountID = admin.ID, "team-cleanup"
		item.InviteStatus, item.AcceptStatus, item.RemoveStatus = "completed", "completed", "pending"
		item.SeatType = "prolite"
	})
	if err != nil {
		t.Fatal(err)
	}
	task := model.AutoRotationTask{
		ID: "cleanup-task", RunID: "cleanup-run", AccountID: account.ID, Email: account.Email,
		AdminAccountID: admin.ID, SeatType: "prolite", Status: "queued", StartedAt: time.Now(), Steps: autoSteps(),
	}
	if err := st.SaveAutoRotationTask(task); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	run := model.AutoRotationRun{ID: task.RunID, Status: "running", StartedAt: time.Now(), Planned: 1}
	var runMu sync.Mutex
	server.executeAutoTask(context.Background(), task, &run, &runMu, model.AutoRotationSettings{RetryCount: 1})
	updated, _, err := st.FreeAccountCredential(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kickCount != 1 || updated.RemoveStatus != "completed" || updated.Status != "removed" {
		t.Fatalf("joined failed account was not removed: kicks=%d account=%+v", kickCount, updated)
	}
	storedTasks := st.AutoRotationTasks(task.RunID)
	if len(storedTasks) != 1 {
		t.Fatalf("stored tasks=%d, want 1", len(storedTasks))
	}
	stored := storedTasks[0]
	if stored.Status != "failed" || stored.CompletedAt == nil || stored.RetryCount != 1 || !strings.Contains(stored.Error, "已自动移出空间") {
		t.Fatalf("failed task did not retain retry and cleanup result: %+v", stored)
	}
	removeCompleted := false
	for _, step := range stored.Steps {
		removeCompleted = removeCompleted || step.Key == "remove" && step.Status == "completed"
	}
	if !removeCompleted || run.Failed != 1 || run.Succeeded != 0 {
		t.Fatalf("remove cleanup step/run counts incorrect: steps=%+v run=%+v", stored.Steps, run)
	}
}

func TestAccountExecutionLogExportIsDownloadableAndRedacted(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "logs@example.com", UserID: "logs-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddAutoRotationEvent(model.AutoRotationEvent{
		ID: "logs-event", AccountID: account.ID, Type: "request", CreatedAt: time.Now(),
		Request: map[string]any{"access_token": "must-not-export", "operation": "oauth"},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	req := httptest.NewRequest(http.MethodGet, "/api/free-accounts/"+account.ID+"/events/export", nil)
	req.SetPathValue("id", account.ID)
	rec := httptest.NewRecorder()
	server.exportFreeAccountEvents(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("unexpected export response: status=%d headers=%v", rec.Code, rec.Header())
	}
	var payload executionLogExport
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Account == nil || payload.Account.ID != account.ID || len(payload.Events) != 1 {
		t.Fatalf("unexpected export payload: %+v", payload)
	}
	if got := payload.Events[0].Request["access_token"]; got != "***" {
		t.Fatalf("secret was not redacted: %v", got)
	}
}

func TestAccountLifecycleEventsIgnorePaginationAndReturnAllEvents(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "timeline@example.com", UserID: "timeline-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	events := make([]model.AutoRotationEvent, 0, 12)
	for index := 0; index < 12; index++ {
		events = append(events, model.AutoRotationEvent{ID: fmt.Sprintf("timeline-%02d", index), AccountID: account.ID, Type: "step", CreatedAt: time.Now().Add(time.Duration(index) * time.Second)})
	}
	if err := st.AddAutoRotationEvents(events); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	req := httptest.NewRequest(http.MethodGet, "/api/free-accounts/"+account.ID+"/events?page=1&page_size=10", nil)
	req.SetPathValue("id", account.ID)
	rec := httptest.NewRecorder()
	server.listFreeAccountEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK   bool `json:"ok"`
		Data struct {
			Events []model.AutoRotationEvent `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || len(payload.Data.Events) != len(events) {
		t.Fatalf("account lifecycle returned %d events, want %d", len(payload.Data.Events), len(events))
	}
}

func TestAutoRotationFailureDoesNotRemoveAccountThatNeverEnteredSpace(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "outside@example.com", UserID: "outside-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	task := model.AutoRotationTask{ID: "outside-task", RunID: "outside-run", AccountID: account.ID, Email: account.Email, Status: "failed", StartedAt: time.Now(), Steps: autoSteps()}
	if err := st.SaveAutoRotationTask(task); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.cleanupFailedAutoTask(context.Background(), &task, "invite", errors.New("invite retries exhausted"))
	updated, _, err := st.FreeAccountCredential(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RemoveStatus == "completed" {
		t.Fatalf("account outside Team space must not be marked removed: %+v", updated)
	}
	for _, step := range task.Steps {
		if step.Key == "remove" {
			t.Fatalf("outside account must not receive a remove step: %+v", task.Steps)
		}
	}
}

func TestAutoRotationFailureRecordsAutomaticRemovalFailure(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"team unavailable"}`, http.StatusBadGateway)
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	admin, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "failed-cleanup-admin", TeamAccountID: "team-failed-cleanup"}, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "failed-cleanup@example.com", UserID: "failed-cleanup-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = st.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID, item.TeamAccountID = admin.ID, "team-failed-cleanup"
		item.InviteStatus, item.AcceptStatus, item.RemoveStatus = "completed", "completed", "pending"
	})
	if err != nil {
		t.Fatal(err)
	}
	task := model.AutoRotationTask{ID: "failed-cleanup-task", RunID: "failed-cleanup-run", AccountID: account.ID, Email: account.Email, AdminAccountID: admin.ID, Status: "failed", StartedAt: time.Now(), Steps: autoSteps()}
	if err := st.SaveAutoRotationTask(task); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.cleanupFailedAutoTask(context.Background(), &task, "push", errors.New("push retries exhausted"))
	updated, _, err := st.FreeAccountCredential(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RemoveStatus != "failed" || task.Status != "failed" || !strings.Contains(task.Error, "自动移出空间失败") {
		t.Fatalf("automatic cleanup failure was not retained: account=%+v task=%+v", updated, task)
	}
	removeFailed := false
	for _, step := range task.Steps {
		removeFailed = removeFailed || step.Key == "remove" && step.Status == "failed"
	}
	if !removeFailed {
		t.Fatalf("failed remove step missing: %+v", task.Steps)
	}
}
