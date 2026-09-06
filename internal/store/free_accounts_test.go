package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestFreeAccountSecretsAreEncryptedAndStatePersists(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	const sourceToken = "header.free-source-secret.signature"
	const oauthAccess = "header.codex-oauth-secret.signature"
	const oauthRefresh = "codex-refresh-secret"
	profile, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{
		Label: "free@example.com", Email: "free@example.com", UserID: "user-free-1", PersonalAccountID: "personal-1", PlanType: "free",
	}, sourceToken)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = dataStore.SaveFreeAccountOAuth(profile.ID, oauthAccess, oauthRefresh, "oauth-account-1")
	if err != nil {
		t.Fatal(err)
	}
	profile, err = dataStore.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
		item.Status, item.ExhaustionPolicy, item.AutoRemove = "monitoring", "5h", false
		item.Sub2AccountID, item.Sub2GroupID = 101, 44
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, created, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{
		Label: "free@example.com", Email: "free@example.com", UserID: "user-free-1", PersonalAccountID: "personal-1", PlanType: "free",
	}, sourceToken)
	if err != nil || created || !profile.OAuthAccessTokenPresent || !profile.OAuthRefreshTokenPresent || profile.ImportedAt.IsZero() {
		t.Fatalf("repeat import lost durable state: %+v created=%v err=%v", profile, created, err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "state.db-wal"} {
		raw, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		for _, secret := range []string{sourceToken, oauthAccess, oauthRefresh} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s contains plaintext Free account secret", name)
			}
		}
	}

	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, credentials, err := reopened.FreeAccountCredential(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.SourceAccessToken != sourceToken || credentials.OAuthAccessToken != oauthAccess || credentials.OAuthRefreshToken != oauthRefresh {
		t.Fatalf("secrets did not round-trip: %+v", credentials)
	}
	if got.Status != "monitoring" || got.ExhaustionPolicy != "5h" || got.AutoRemove || got.Sub2AccountID != 101 || got.OAuthAccountID != "oauth-account-1" {
		t.Fatalf("Free account state did not persist: %+v", got)
	}
}

func TestSub2PasswordIsEncryptedAndCanBeRetained(t *testing.T) {
	directory := t.TempDir()
	dataStore, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	const password = "sub2-admin-password-secret"
	settings, err := dataStore.SaveSub2Settings(model.Sub2Settings{
		URL: "https://sub2.example.com", Email: "admin@example.com", GroupIDs: []int64{44, 46}, GroupNames: []string{"Free pool", "Reserve"}, Models: []string{"gpt-5.2-codex", "gpt-5.1-codex-mini"}, AccountConcurrency: 10,
	}, password)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.PasswordPresent {
		t.Fatal("saved settings did not report password presence")
	}
	settings.GroupIDs, settings.GroupNames = []int64{45, 47}, []string{"Next pool", "Second pool"}
	if _, err := dataStore.SaveSub2Settings(settings, ""); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) {
		t.Fatal("state.db contains plaintext Sub2 password")
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, gotPassword, err := reopened.Sub2Settings()
	if err != nil || gotPassword != password || !slices.Equal(got.GroupIDs, []int64{45, 47}) || !slices.Equal(got.GroupNames, []string{"Next pool", "Second pool"}) || !slices.Equal(got.Models, []string{"gpt-5.2-codex", "gpt-5.1-codex-mini"}) {
		t.Fatalf("Sub2 settings did not persist: %+v password=%q err=%v", got, gotPassword, err)
	}
}

func TestSub2LegacySingleGroupIsNormalized(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	if _, err := dataStore.SaveSub2Settings(model.Sub2Settings{URL: "https://sub2.example.com", Email: "admin@example.com", GroupID: 44, GroupName: "Legacy", AccountConcurrency: 10}, "secret"); err != nil {
		t.Fatal(err)
	}
	got, _, err := dataStore.Sub2Settings()
	if err != nil || !slices.Equal(got.GroupIDs, []int64{44}) || !slices.Equal(got.GroupNames, []string{"Legacy"}) {
		t.Fatalf("legacy group was not normalized: %+v err=%v", got, err)
	}
}
