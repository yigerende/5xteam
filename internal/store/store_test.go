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
