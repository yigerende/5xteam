package store

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestDeleteMailAccountRemovesTeamProjectionButKeepsWorkspaceHistory(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "cascade@example.com", UserID: "cascade-user"}, "source-at")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := s.UpdateFreeAccount(p.ID, func(item *model.FreeAccountProfile) {
		item.TeamAccountID = "team-cascade"
		item.AcceptStatus = "completed"
		item.InviteStatus = "completed"
		item.JoinedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: "cascade@example.com"}, model.MailAccountCredentials{Email: "cascade@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMailAccount("CASCADE@EXAMPLE.COM"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MailAccountCredential("cascade@example.com"); err == nil {
		t.Fatal("mail account was not deleted")
	}
	if _, _, err := s.FreeAccountCredential(p.ID); err == nil {
		t.Fatal("Team rotation projection was not deleted")
	}
	visits, err := s.TeamVisits("cascade@example.com", "cascade-user")
	if err != nil || len(visits) != 1 || visits[0].TeamAccountID != "team-cascade" {
		t.Fatalf("workspace history was not retained: visits=%+v err=%v", visits, err)
	}
}
