package store

import (
	"os"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestProxyProfilesPersistAndTrackSelectedURL(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
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

func TestAdminAccountTokenIsEncryptedAtRest(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
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

func TestProxyProfileNamesAreUnique(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
	history := reopened.History()
	if len(history) != 1 || len(history[0].Results) != 2 || history[0].Results[1].Email != "child-two@example.com" {
		t.Fatalf("history was not persisted: %+v", history)
	}
}
