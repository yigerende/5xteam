package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProxyProfilesPersistAndTrackSelectedURL(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	profile, err := dataStore.SaveProxy(model.ProxyProfile{Name: "local", URL: "http://127.0.0.1:7890"})
	if err != nil {
		t.Fatal(err)
	}
	settings := dataStore.Settings()
	settings.ProxyURL = profile.URL
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	profile.URL = "socks5://127.0.0.1:1080"
	if _, err := dataStore.SaveProxy(profile); err != nil {
		t.Fatal(err)
	}
	if got := dataStore.Settings().ProxyURL; got != profile.URL {
		t.Fatalf("selected proxy URL = %q", got)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if len(reopened.Proxies()) != 1 || reopened.Proxies()[0].Name != "local" {
		t.Fatalf("profiles were not persisted: %+v", reopened.Proxies())
	}
	if err := reopened.DeleteProxy(profile.ID); err != nil {
		t.Fatal(err)
	}
	if reopened.Settings().ProxyURL != "" {
		t.Fatal("deleting selected proxy did not clear settings")
	}
}

func TestEmptyCollectionsAreJSONArrays(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	for name, value := range map[string]any{
		"accounts": dataStore.AdminAccounts(),
		"proxies":  dataStore.Proxies(),
		"history":  dataStore.History(),
		"progress": dataStore.AccountProgress(""),
	} {
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "[]" {
			t.Fatalf("%s encoded as %s, err=%v", name, encoded, err)
		}
	}
}

func TestMailAccountsKeepEntryTimeAndSortNewestFirst(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	first, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: "first@example.com", Label: "first"}, model.MailAccountCredentials{Email: "first@example.com", PickupURL: "https://mail.example/1"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: "second@example.com", Label: "second"}, model.MailAccountCredentials{Email: "second@example.com", PickupURL: "https://mail.example/2"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: "first@example.com", Label: "first-updated"}, model.MailAccountCredentials{Email: "first@example.com", PickupURL: "https://mail.example/1-updated"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || !updated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("re-import changed mailbox identity or entry time: first=%+v updated=%+v", first, updated)
	}
	accounts := dataStore.MailAccounts()
	if len(accounts) != 2 || accounts[0].Email != second.Email || accounts[1].Email != first.Email {
		t.Fatalf("mailboxes are not sorted by entry time descending: %+v", accounts)
	}
}

func TestMailAccountChatGPTSessionRoundTripsEncrypted(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	const session = `{"user":{"email":"session@example.com"},"accessToken":"session-at","expires":"2026-12-01T00:00:00.000Z"}`
	profile, err := dataStore.SaveMailAccount(
		model.MailAccountProfile{Email: "session@example.com"},
		model.MailAccountCredentials{Email: "session@example.com", AccessToken: "session-at", ChatGPTSession: session},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.ChatGPTSessionPresent {
		t.Fatal("chatgpt_session_present = false, want true")
	}

	reopenedProfile, credentials, err := dataStore.MailAccountCredential("session@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !reopenedProfile.ChatGPTSessionPresent || credentials.ChatGPTSession != session {
		t.Fatalf("session did not round-trip: profile=%+v session=%q", reopenedProfile, credentials.ChatGPTSession)
	}
}

func TestMailAccountDeadStatusPersistsAcrossReimport(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	email := "dead@example.com"
	if _, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: email, Label: email}, model.MailAccountCredentials{Email: email, PickupURL: "https://mail.example/messages/one"}); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.MarkMailAccountDead(email, "account_deactivated"); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "reimported"}, model.MailAccountCredentials{Email: email, PickupURL: "https://mail.example/messages/two"}); err != nil {
		t.Fatal(err)
	}
	profile, _, err := dataStore.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ChatGPTStatus != "dead" || profile.RegistrationStatus != "dead" || profile.ChatGPTStatusAt == nil || profile.ChatGPTStatusMessage != "account_deactivated" {
		t.Fatalf("dead status was reset by reimport: %+v", profile)
	}
}

func TestMailAccountManagementScopeMovesWithoutDuplicatingCredentials(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "pro-managed@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "Pro managed"}, model.MailAccountCredentials{
		Email: email, PickupURL: "https://mail.example/pickup", AccessToken: "saved-at", RefreshToken: "saved-rt",
	}); err != nil {
		t.Fatal(err)
	}
	profile, err := dataStore.UpdateMailAccountManagementScope(email, "pro")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ManagementScope != "pro" || profile.ProManagedAt == nil {
		t.Fatalf("account was not moved to Pro management: %+v", profile)
	}
	if got := dataStore.MailAccountsByManagementScope("mail"); len(got) != 0 {
		t.Fatalf("Pro account still appears in mail scope: %+v", got)
	}
	if got := dataStore.MailAccountsByManagementScope("pro"); len(got) != 1 || got[0].Email != email {
		t.Fatalf("Pro scope does not contain moved account: %+v", got)
	}
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "Reimported"}, model.MailAccountCredentials{
		Email: email, PickupURL: "https://mail.example/new", AccessToken: "updated-at", RefreshToken: "updated-rt",
	}); err != nil {
		t.Fatal(err)
	}
	reimported, credentials, err := dataStore.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if reimported.ManagementScope != "pro" || reimported.ProManagedAt == nil || credentials.AccessToken != "updated-at" {
		t.Fatalf("reimport reset Pro scope or credentials: profile=%+v credentials=%+v", reimported, credentials)
	}
	returned, err := dataStore.UpdateMailAccountManagementScope(email, "mail")
	if err != nil {
		t.Fatal(err)
	}
	if returned.ManagementScope != "mail" || returned.ProManagedAt != nil {
		t.Fatalf("account was not returned to mail management: %+v", returned)
	}
}

func TestProOAuthSessionRoundTripAndExpiry(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	now := time.Now()
	session := model.ProOAuthSession{ID: "session-1", State: "state", RedirectURI: "http://localhost", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err = dataStore.SaveProOAuthSession(session, "secret-verifier"); err != nil {
		t.Fatal(err)
	}
	got, verifier, err := dataStore.ProOAuthSession(session.ID)
	if err != nil || got.State != "state" || verifier != "secret-verifier" {
		t.Fatalf("got=%+v verifier=%q err=%v", got, verifier, err)
	}
	expired := session
	expired.ID = "expired"
	expired.ExpiresAt = now.Add(-time.Minute)
	if err = dataStore.SaveProOAuthSession(expired, "old"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = dataStore.ProOAuthSession(expired.ID); err == nil {
		t.Fatal("expected expired session rejection")
	}
}

func TestSaveMailAccountOAuthBundleRotatesTokensAtomically(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	_, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: "pro@example.com"}, model.MailAccountCredentials{Email: "pro@example.com", AccessToken: "old-at", RefreshToken: "old-rt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dataStore.UpdateMailAccountManagementScope("pro@example.com", "pro"); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if err = dataStore.SaveMailAccountOAuthBundle("pro@example.com", "new-at", "", "new-id", "acc", "user", expires); err != nil {
		t.Fatal(err)
	}
	p, c, err := dataStore.MailAccountCredential("pro@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessToken != "new-at" || c.RefreshToken != "old-rt" || c.IDToken != "new-id" || p.OAuthStatus != "completed" || p.OAuthAccountID != "acc" || p.OAuthUserID != "user" || !p.IDTokenPresent {
		t.Fatalf("profile=%+v credentials=%+v", p, c)
	}
}

func TestMailAccountPlanCheckFailureKeepsLastSuccessfulPlan(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "plan@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "saved-at"}); err != nil {
		t.Fatal(err)
	}
	checkedAt := time.Now().Add(-time.Minute)
	if _, err = dataStore.UpdateMailAccountPlanCheck(email, model.AccountPlanCheckResult{
		OK: true, HTTPStatus: 200, CurrentPlanType: "free", SubscriptionPlan: "chatgptfreeplan",
		PlusTrialEligible: true, CheckedAt: checkedAt,
	}); err != nil {
		t.Fatal(err)
	}
	failedAt := time.Now()
	profile, err := dataStore.UpdateMailAccountPlanCheck(email, model.AccountPlanCheckResult{HTTPStatus: 429, CheckedAt: failedAt, Error: "HTTP 429"})
	if err != nil {
		t.Fatal(err)
	}
	if profile.PlanCheckStatus != "failed" || profile.PlanCheckError != "HTTP 429" || profile.CurrentPlanType != "free" || !profile.PlusTrialEligible || profile.PlanLastSuccessAt == nil || !profile.PlanLastSuccessAt.Equal(checkedAt) {
		t.Fatalf("last successful plan was not preserved: %+v", profile)
	}
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "reimported"}, model.MailAccountCredentials{Email: email, AccessToken: "new-at"}); err != nil {
		t.Fatal(err)
	}
	reimported, _, err := dataStore.MailAccountCredential(email)
	if err != nil || reimported.CurrentPlanType != "free" || reimported.PlanCheckStatus != "failed" || !reimported.PlusTrialEligible {
		t.Fatalf("reimport erased plan state: %+v err=%v", reimported, err)
	}
}

func TestLegacySettingsReceiveNetworkRetryDefaults(t *testing.T) {
	directory := t.TempDir()
	legacy := `{"settings":{"base_url":"https://chatgpt.com/backend-api","accepted_tos_version":"2024-12-17","role":"standard-user","seat_type":"default","request_timeout_seconds":45,"concurrency":1}}`
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	settings := reopened.Settings()
	defaults := model.DefaultSettings()
	if settings.NetworkRetryCount != defaults.NetworkRetryCount || settings.NetworkRetryInterval != defaults.NetworkRetryInterval {
		t.Fatalf("retry defaults not migrated: %+v", settings)
	}
}

func TestAdminAccountTokenIsEncryptedAtRest(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	const token = "header.sensitive-access-token.signature"
	profile, err := dataStore.SaveAdminAccountCredentials(model.AdminAccountProfile{Label: "team-admin", Email: "admin@example.com", TeamAccountID: "team-1"}, token, "refresh-secret")
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(dataStore.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), token) {
		t.Fatal("plaintext token was written to state file")
	}
	if strings.Contains(string(state), "refresh-secret") {
		t.Fatal("plaintext refresh token was written to state file")
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	gotProfile, gotToken, err := reopened.AdminAccountToken(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != token || gotProfile.Email != "admin@example.com" {
		t.Fatalf("unexpected decrypted account: %+v %q", gotProfile, gotToken)
	}
	_, credentials, err := reopened.AdminAccountCredential(profile.ID)
	if err != nil || credentials.RefreshToken != "refresh-secret" {
		t.Fatalf("unexpected decrypted refresh token: %+v %v", credentials, err)
	}
}

func TestAdminTeamRotationChildCountPersistsAndIncrements(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	profile, err := dataStore.SaveAdminAccountCredentials(model.AdminAccountProfile{Label: "count-admin", TeamAccountID: "team-count"}, "access-token", "")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := dataStore.IncrementAdminTeamRotationChildCount(profile.ID); err != nil {
			t.Fatal(err)
		}
	}
	got := dataStore.AdminAccounts()
	if len(got) != 1 || got[0].TeamRotationChildCount != 2 {
		t.Fatalf("unexpected cumulative count: %+v", got)
	}
	updated, err := dataStore.SaveAdminAccountCredentials(model.AdminAccountProfile{ID: profile.ID, Label: "count-admin-renamed", TeamAccountID: "team-count"}, "access-token-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.TeamRotationChildCount != 2 {
		t.Fatalf("credential update reset cumulative count: %+v", updated)
	}
}

func TestOpenAIAccountTokenIsEncryptedAndStatusPersists(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	const token = "header.openai-sensitive.signature"
	const refresh = "refresh-openai-sensitive"
	profile, err := dataStore.SaveOpenAIAccount(model.OpenAIAccountProfile{Label: "codex", Email: "a@example.com", AccountID: "acct-1"}, token, refresh)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dataStore.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("OpenAI token was written in plaintext")
	}
	if strings.Contains(string(raw), refresh) {
		t.Fatal("OpenAI refresh token was written in plaintext")
	}
	updated, err := dataStore.UpdateOpenAIAccountStatus(profile.ID, func(item *model.OpenAIAccountProfile) {
		count := 3
		item.ResetCredits = &count
		item.LastCheckValid = true
	})
	if err != nil || updated.ResetCredits == nil || *updated.ResetCredits != 3 || !updated.LastCheckValid {
		t.Fatalf("unexpected status update: %+v %v", updated, err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	gotProfile, credentials, err := reopened.OpenAIAccountCredential(profile.ID)
	if err != nil || credentials.AccessToken != token || credentials.RefreshToken != refresh || !gotProfile.RefreshTokenPresent {
		t.Fatalf("unexpected decrypted token: %q %v", credentials.AccessToken, err)
	}
}

func TestProxyProfileNamesAreUnique(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	if _, err := dataStore.SaveProxy(model.ProxyProfile{Name: "primary", URL: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.SaveProxy(model.ProxyProfile{Name: "PRIMARY", URL: "http://127.0.0.1:7891"}); err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func TestHistoryPersistsChildEmails(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	entry := model.HistoryEntry{
		ID: "job-1", Status: "completed", AdminEmail: "admin@example.com", Total: 2, Succeeded: 2,
		Results: []model.HistoryResult{
			{Email: "child-one@example.com", Status: "completed"},
			{Email: "child-two@example.com", Status: "completed"},
		},
	}
	if err := dataStore.AddHistory(entry); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	history := reopened.History()
	if len(history) != 1 || len(history[0].Results) != 2 || history[0].Results[1].Email != "child-two@example.com" {
		t.Fatalf("history was not persisted: %+v", history)
	}
}

func TestHistoryUpdatesPersistentAccountProgress(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	now := time.Now()
	entries := []model.HistoryEntry{
		{ID: "enter", Operation: "enter", TeamAccountID: "team-1", AdminEmail: "admin@example.com", CompletedAt: now, Results: []model.HistoryResult{{UserID: "user-1", Email: "child@example.com", Status: "completed", CompletedSteps: []string{"invite", "accept"}}}},
		{ID: "transfer", Operation: "transfer", TeamAccountID: "team-1", AdminEmail: "admin@example.com", CompletedAt: now.Add(time.Minute), Results: []model.HistoryResult{{UserID: "user-1", Email: "child@example.com", Status: "completed", CompletedSteps: []string{"transfer"}}}},
		{ID: "kick", Operation: "kick", TeamAccountID: "team-1", AdminEmail: "admin@example.com", CompletedAt: now.Add(2 * time.Minute), Results: []model.HistoryResult{{UserID: "user-1", Email: "child@example.com", Status: "completed", CompletedSteps: []string{"kick"}}}},
	}
	for _, entry := range entries {
		if err := dataStore.AddHistory(entry); err != nil {
			t.Fatal(err)
		}
	}
	progress := dataStore.AccountProgress("team-1")
	if len(progress) != 1 || progress[0].EnteredAt == nil || progress[0].TransferredAt == nil || progress[0].RemovedAt == nil || progress[0].LastOperation != "kick" {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}
