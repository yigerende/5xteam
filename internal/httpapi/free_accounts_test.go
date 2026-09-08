package httpapi

import (
	"encoding/json"
	"errors"
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

func TestCPAAccountNameAndPayload(t *testing.T) {
	profile := model.FreeAccountProfile{ID: "id-1", Email: "user@example.com", Label: "User", AcceptStatus: "completed", PlanType: "free", OAuthAccountID: "acct-1"}
	name := cpaAccountName(profile, false)
	if !strings.HasPrefix(name, "User--") || strings.HasSuffix(name, "-重登") {
		t.Fatalf("unexpected CPA account name: %q", name)
	}
	reloginName := cpaAccountName(profile, true)
	if !strings.HasPrefix(reloginName, "User--") || !strings.HasSuffix(reloginName, "-重登") {
		t.Fatalf("unexpected CPA relogin account name: %q", reloginName)
	}
	payload := buildCPAAuthPayloadNamed(profile, store.FreeAccountCredentials{OAuthAccessToken: "at", OAuthRefreshToken: "rt"}, nil, name)
	if payload["name"] != name || payload["plan_type"] != downstreamProlitePlanType || payload["chatgpt_plan_type"] != downstreamProlitePlanType {
		encoded, _ := json.Marshal(payload)
		t.Fatalf("payload name/plan type mismatch: %s", encoded)
	}
	sub2Credentials := buildSub2OAuthCredentials(profile, store.FreeAccountCredentials{OAuthAccessToken: "at", OAuthRefreshToken: "rt"})
	if sub2Credentials["plan_type"] != downstreamProlitePlanType || sub2Credentials["chatgpt_plan_type"] != downstreamProlitePlanType {
		encoded, _ := json.Marshal(sub2Credentials)
		t.Fatalf("Sub2 credentials plan type mismatch: %s", encoded)
	}
}

func TestManualFreeAccountStageUpdatesProgress(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	profile := model.FreeAccountProfile{
		InviteStatus: "completed", AcceptStatus: "failed", OAuthStatus: "pending",
		PushStatus: "pending", QuotaStatus: "pending", RemoveStatus: "pending", LastError: "old failure",
	}

	applyManualFreeAccountStage(&profile, "accept", "completed", "", now)
	if profile.AcceptStatus != "completed" || profile.JoinedAt == nil || !profile.JoinedAt.Equal(now) || profile.Status != "joined" || profile.LastError != "" {
		t.Fatalf("manual completion was not applied: %+v", profile)
	}

	applyManualFreeAccountStage(&profile, "oauth", "failed", "manual oauth failed", now)
	if profile.OAuthStatus != "failed" || profile.Status != "oauth_failed" || profile.LastError != "manual oauth failed" || profile.OAuthReadyAt != nil {
		t.Fatalf("manual failure was not applied: %+v", profile)
	}

	applyManualFreeAccountStage(&profile, "oauth", "pending", "", now)
	if profile.OAuthStatus != "pending" || profile.Status != "joined" || profile.LastError != "" {
		t.Fatalf("manual reset was not applied: %+v", profile)
	}

	applyManualFreeAccountStage(&profile, "push", "completed", "", now)
	if profile.PushStatus != "completed" || profile.PushedAt == nil || profile.Status != "monitoring" {
		t.Fatalf("manual push completion was not applied: %+v", profile)
	}
}

func TestFreeAccountJoinSkipsCompletedStages(t *testing.T) {
	tests := []struct {
		name                   string
		invite, accept         string
		wantInvite, wantAccept bool
	}{
		{name: "new account", invite: "pending", accept: "pending", wantInvite: true, wantAccept: true},
		{name: "manually invited", invite: "completed", accept: "pending", wantInvite: false, wantAccept: true},
		{name: "already joined", invite: "completed", accept: "completed", wantInvite: false, wantAccept: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInvite, gotAccept := freeAccountJoinSteps(model.FreeAccountProfile{InviteStatus: tt.invite, AcceptStatus: tt.accept})
			if gotInvite != tt.wantInvite || gotAccept != tt.wantAccept {
				t.Fatalf("steps = invite:%v accept:%v, want invite:%v accept:%v", gotInvite, gotAccept, tt.wantInvite, tt.wantAccept)
			}
		})
	}
}

func TestDeadOAuthDetectionRequiresExplicitAccountSignal(t *testing.T) {
	dead := []struct {
		name    string
		result  map[string]any
		message string
	}{
		{"structured", map[string]any{"dead": true}, ""},
		{"status", map[string]any{"status": "deactivated"}, ""},
		{"code", nil, "email-otp/validate: account_deleted"},
		{"disabled code", nil, "account_disabled"},
		{"message", nil, "You do not have an account because it has been deleted or deactivated"},
		{"banned", nil, "account banned"},
		{"suspended", nil, "account suspended"},
	}
	for _, tc := range dead {
		t.Run(tc.name, func(t *testing.T) {
			if !isDeadOAuthResult(tc.result, tc.message) {
				t.Fatalf("expected dead result for %q", tc.message)
			}
		})
	}
	for _, message := range []string{
		"HTTP 403 forbidden", "HTTP 429 too many requests", "invalid_auth_step",
		"wrong_email_otp_code", "OAuth callback missing", "proxy timeout", "add_phone required",
	} {
		if isDeadOAuthResult(nil, message) {
			t.Fatalf("transient error was classified as dead: %q", message)
		}
	}
}

func TestDeadOAuthSynchronizesMailAndRemovesTeamMember(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()

	var kicked bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/accounts/team-1/users/user-1") {
			kicked = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":"removed"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	admin, err := dataStore.SaveAdminAccount(model.AdminAccountProfile{Label: "admin", Email: "admin@example.com", TeamAccountID: "team-1"}, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: "dead@example.com", Label: "dead@example.com"}, model.MailAccountCredentials{Email: "dead@example.com", PickupURL: "https://mail.example/messages/token"}); err != nil {
		t.Fatal(err)
	}
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "dead@example.com", UserID: "user-1", PersonalAccountID: "personal-1"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID, item.TeamAccountID, item.UserID = admin.ID, "team-1", "user-1"
		item.InviteStatus, item.AcceptStatus, item.OAuthStatus = "completed", "completed", "running"
		item.RemoveStatus, item.Status, item.AutoRemove = "pending", "oauthing", true
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.oauthJobs["job-dead"] = map[string]any{"job_id": "job-dead", "account_id": account.ID, "trigger": "relogin", "status": "running"}
	server.finishOAuthJob("job-dead", account.ID, map[string]any{
		"success": false, "status": "deactivated", "dead": true,
		"error_code": "account_deleted", "stage": "email_otp_validate", "http_status": float64(403),
		"error": "You do not have an account because it has been deleted or deactivated",
	}, nil)

	got, _, err := dataStore.FreeAccountCredential(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !kicked || !got.Dead || got.DeadReason == "" || got.DeadDetectedAt == nil || got.RemoveStatus != "completed" || got.Status != "removed" {
		t.Fatalf("dead account was not fully removed: kicked=%v profile=%+v", kicked, got)
	}
	if got.Sub2AccountID != 0 || got.PushStatus == "completed" {
		t.Fatalf("dead OAuth must not continue to Sub2 push: %+v", got)
	}
	mail, _, err := dataStore.MailAccountCredential(account.Email)
	if err != nil {
		t.Fatal(err)
	}
	if mail.ChatGPTStatus != "dead" || mail.RegistrationStatus != "dead" || mail.ChatGPTStatusAt == nil {
		t.Fatalf("mail status was not synchronized: %+v", mail)
	}
	events := dataStore.AutoRotationEvents("", "")
	var detected, removed, started bool
	for _, event := range events {
		if event.AccountID == account.ID && event.Type == "dead_detected" {
			detected = event.Details["source"] == "relogin"
		}
		started = started || event.AccountID == account.ID && event.Type == "dead_remove_start"
		removed = removed || event.AccountID == account.ID && event.Type == "dead_remove_success"
	}
	if !detected || !started || !removed {
		t.Fatalf("missing dead-account events: %+v", events)
	}
}

func TestDeadOAuthKeepsDeadStateWhenAutomaticRemovalFails(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"temporary team failure"}`, http.StatusBadGateway)
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	admin, err := dataStore.SaveAdminAccount(model.AdminAccountProfile{Label: "admin-fail", TeamAccountID: "team-fail"}, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: "dead-fail@example.com"}, model.MailAccountCredentials{Email: "dead-fail@example.com", PickupURL: "https://mail.example/messages/token"}); err != nil {
		t.Fatal(err)
	}
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "dead-fail@example.com", UserID: "user-fail"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID, item.TeamAccountID = admin.ID, "team-fail"
		item.InviteStatus, item.AcceptStatus, item.OAuthStatus = "completed", "completed", "running"
		item.RemoveStatus = "pending"
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.oauthJobs["job-dead-fail"] = map[string]any{"trigger": "oauth", "status": "running"}
	server.finishOAuthJob("job-dead-fail", account.ID, map[string]any{
		"success": false, "dead": true, "status": "deactivated", "error_code": "account_banned",
		"stage": "email_otp_validate", "http_status": float64(403), "error": "account banned",
	}, nil)
	got, _, err := dataStore.FreeAccountCredential(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dead || got.RemoveStatus != "failed" || got.DeadDetectedAt == nil {
		t.Fatalf("dead marker must survive removal failure: %+v", got)
	}
	mail, _, err := dataStore.MailAccountCredential(account.Email)
	if err != nil || mail.ChatGPTStatus != "dead" {
		t.Fatalf("mail dead state missing after removal failure: profile=%+v err=%v", mail, err)
	}
	var failedEvent bool
	for _, event := range dataStore.AutoRotationEvents("", "") {
		failedEvent = failedEvent || event.AccountID == account.ID && event.Type == "dead_remove_failed"
	}
	if !failedEvent {
		t.Fatal("missing dead_remove_failed event")
	}
	if _, _, err := server.performFreeAccountQuotaInternal(t.Context(), account.ID, true, true); err == nil || !strings.Contains(err.Error(), "死号") {
		t.Fatalf("dead account should not re-enter quota/relogin flow: %v", err)
	}
	if err := server.reloginAndRepush(t.Context(), account.ID); !errors.Is(err, errDeadAccountHandled) {
		t.Fatalf("dead account should not start another 401 relogin: %v", err)
	}
}

func TestOrdinaryOAuthFailureDoesNotRemoveAccount(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "retry@example.com", UserID: "retry-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.InviteStatus, item.AcceptStatus, item.OAuthStatus = "completed", "completed", "running"
		item.RemoveStatus = "pending"
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.oauthJobs["job-retry"] = map[string]any{"trigger": "relogin", "status": "running"}
	server.finishOAuthJob("job-retry", account.ID, map[string]any{"success": false, "error": "HTTP 429 too many requests"}, nil)
	got, _, err := dataStore.FreeAccountCredential(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dead || got.RemoveStatus != "pending" || got.OAuthStatus != "failed" || got.Status != "oauth_failed" {
		t.Fatalf("ordinary OAuth error must remain retryable: %+v", got)
	}
}

func TestReloginOAuthCompletionWritesNewTokensToMailAccount(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	if _, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: "relogin-sync@example.com"}, model.MailAccountCredentials{
		Email: "relogin-sync@example.com", AccessToken: "old-at", RefreshToken: "old-rt",
	}); err != nil {
		t.Fatal(err)
	}
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "relogin-sync@example.com", UserID: "relogin-sync-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.oauthJobs["job-relogin-sync"] = map[string]any{"trigger": "relogin", "status": "running"}
	server.finishOAuthJob("job-relogin-sync", account.ID, map[string]any{
		"success": true, "access_token": "new-at", "refresh_token": "new-rt", "account_id": "oauth-account",
	}, nil)
	_, credentials, err := dataStore.MailAccountCredential(account.Email)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "new-at" || credentials.RefreshToken != "new-rt" {
		t.Fatalf("mail credentials were not replaced after relogin: at=%q rt=%q", credentials.AccessToken, credentials.RefreshToken)
	}
}

func TestConcurrentRemovalOnlyKicksTeamMemberOnce(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	var kicks atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		kicks.Add(1)
		time.Sleep(75 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"removed"}`))
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	admin, err := dataStore.SaveAdminAccount(model.AdminAccountProfile{Label: "admin-concurrent", TeamAccountID: "team-concurrent"}, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "concurrent@example.com", UserID: "user-concurrent"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID, item.TeamAccountID = admin.ID, "team-concurrent"
		item.InviteStatus, item.AcceptStatus, item.RemoveStatus = "completed", "completed", "pending"
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, removeErr := server.performFreeAccountRemove(t.Context(), account.ID)
			results <- removeErr
		}()
	}
	wg.Wait()
	close(results)
	for removeErr := range results {
		if removeErr != nil {
			t.Fatal(removeErr)
		}
	}
	if kicks.Load() != 1 {
		t.Fatalf("concurrent removal sent %d upstream DELETE requests, want 1", kicks.Load())
	}
}

func TestConcurrentRemovalSerializesDifferentAccountsForSameTeam(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	var active, maxActive, kicks atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		kicks.Add(1)
		current := active.Add(1)
		for current > maxActive.Load() && !maxActive.CompareAndSwap(maxActive.Load(), current) {
		}
		time.Sleep(100 * time.Millisecond)
		active.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"removed"}`))
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	accounts := make([]model.FreeAccountProfile, 0, 2)
	for index := range 2 {
		admin, saveErr := dataStore.SaveAdminAccount(model.AdminAccountProfile{
			Label:         fmt.Sprintf("admin-serial-%d", index),
			TeamAccountID: "team-serial",
		}, "admin-token")
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		account, _, saveErr := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{
			Email:  fmt.Sprintf("serial-%d@example.com", index),
			UserID: fmt.Sprintf("serial-user-%d", index),
		}, "source-token")
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		account, saveErr = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
			item.AdminAccountID, item.TeamAccountID = admin.ID, admin.TeamAccountID
			item.InviteStatus, item.AcceptStatus, item.RemoveStatus = "completed", "completed", "pending"
		})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		accounts = append(accounts, account)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	start := make(chan struct{})
	results := make(chan error, len(accounts))
	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			<-start
			_, removeErr := server.performFreeAccountRemove(t.Context(), accountID)
			results <- removeErr
		}(account.ID)
	}
	close(start)
	wg.Wait()
	close(results)
	for removeErr := range results {
		if removeErr != nil {
			t.Fatal(removeErr)
		}
	}
	if kicks.Load() != 2 {
		t.Fatalf("same-Team removal sent %d upstream DELETE requests, want 2", kicks.Load())
	}
	if maxActive.Load() != 1 {
		t.Fatalf("same-Team removal reached %d concurrent upstream requests, want 1", maxActive.Load())
	}
}

func TestConcurrentRemovalAllowsDifferentTeamsInParallel(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	var active, maxActive atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		current := active.Add(1)
		for current > maxActive.Load() && !maxActive.CompareAndSwap(maxActive.Load(), current) {
		}
		time.Sleep(150 * time.Millisecond)
		active.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"removed"}`))
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	accounts := make([]model.FreeAccountProfile, 0, 2)
	for index := range 2 {
		admin, saveErr := dataStore.SaveAdminAccount(model.AdminAccountProfile{
			Label:         fmt.Sprintf("admin-parallel-%d", index),
			TeamAccountID: fmt.Sprintf("team-parallel-%d", index),
		}, "admin-token")
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		account, _, saveErr := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{
			Email:  fmt.Sprintf("parallel-%d@example.com", index),
			UserID: fmt.Sprintf("parallel-user-%d", index),
		}, "source-token")
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		account, saveErr = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
			item.AdminAccountID, item.TeamAccountID = admin.ID, admin.TeamAccountID
			item.InviteStatus, item.AcceptStatus, item.RemoveStatus = "completed", "completed", "pending"
		})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		accounts = append(accounts, account)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	start := make(chan struct{})
	results := make(chan error, len(accounts))
	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			<-start
			_, removeErr := server.performFreeAccountRemove(t.Context(), accountID)
			results <- removeErr
		}(account.ID)
	}
	close(start)
	wg.Wait()
	close(results)
	for removeErr := range results {
		if removeErr != nil {
			t.Fatal(removeErr)
		}
	}
	if maxActive.Load() < 2 {
		t.Fatalf("different-Team removals reached only %d concurrent upstream request, want at least 2", maxActive.Load())
	}
}
