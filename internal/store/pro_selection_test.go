package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestSelectUnloggedProAccountsAcrossPages(t *testing.T) {
	s := transferStore(t)
	want := []string{}
	for i := 0; i < 27; i++ {
		email := fmt.Sprintf("new-%02d@example.com", i)
		saveProSortFixture(t, s, model.MailAccountProfile{Email: email})
		want = append(want, email)
	}
	now := time.Now()
	for _, p := range []model.MailAccountProfile{
		{Email: "at@example.com", AccessTokenPresent: true},
		{Email: "rt@example.com", RefreshTokenPresent: true},
		{Email: "id@example.com", IDTokenPresent: true},
		{Email: "session@example.com", ChatGPTSessionPresent: true},
		{Email: "siwc@example.com", SIWCPresent: true},
		{Email: "expired@example.com", ATCheckedAt: &now, ATValid: false},
		{Email: "oauth@example.com", OAuthStatus: "completed"},
		{Email: "login@example.com", ProManualStages: map[string]model.ProStageProgress{"login": {Status: "completed"}}},
		{Email: "auto-login@example.com", ProAuto: model.ProAutoState{Steps: map[string]string{"login": "completed"}}},
		{Email: "running@example.com", ProWorkflowRunning: true},
		{Email: "merged@example.com", SpaceMergedOnce: true},
		{Email: "pending-merge@example.com", InviteStatus: "completed"},
	} {
		saveProSortFixture(t, s, p)
	}
	if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: "mail-only@example.com"}, model.MailAccountCredentials{Email: "mail-only@example.com"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SelectUnloggedProAccounts(t.Context(), "")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %v, error = %v; want %v", got, err, want)
	}
	got, err = s.SelectUnloggedProAccounts(t.Context(), " NEW-26@EXAMPLE.COM ")
	if err != nil || !reflect.DeepEqual(got, want[26:]) {
		t.Fatalf("search selection = %v, error = %v", got, err)
	}
	got, err = s.SelectUnloggedProAccounts(t.Context(), "no-match")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty selection = %v, error = %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SelectUnloggedProAccounts(ctx, ""); err == nil {
		t.Fatal("cancelled selection must stop")
	}
}
