package store

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
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

func TestMailManagementDeleteIgnoresBusinessStateAndPreservesHistory(t *testing.T) {
	s := transferStore(t)
	const email = "operator-delete@example.com"
	transferSource(t, s, email)
	card := ejhFixtureCard(t, s)
	order := transferOrder(email, "operator-delete-payment", "manual_review")
	order.Order.CardID = card.ID
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err := s.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ReserveProRestock(restock.Decision{ID: "operator-delete-restock", At: time.Now(), Version: c.Version, Suggested: 1}, "target", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("reserve: %v %v", tasks, err)
	}
	if err := s.CreateGPTPayOrder(order.Order, order.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,
 '$.pro_workflow_running',json('true'),'$.vm2api',json('{"connection_id":"existing-runtime"}')) WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	beforeOrder, _, err := s.GPTPayOrder(order.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMailAccountFromManagement(" OPERATOR-DELETE@EXAMPLE.COM "); err != nil {
		t.Fatal("operator deletion blocked by business state", err)
	}
	if _, _, err := s.MailAccountCredential(email); err == nil {
		t.Fatal("mailbox credentials remain")
	}
	afterOrder, _, err := s.GPTPayOrder(order.Order.ID)
	if err != nil || mustJSON(beforeOrder) != mustJSON(afterOrder) {
		t.Fatal("deletion changed payment evidence", err)
	}
	if linked, err := s.ProRestockDecisionTasks(tasks[0].DecisionID); err != nil || len(linked) != 1 {
		t.Fatal("deletion removed restock history", err)
	}
	got, _, err := s.GPTPayCard(card.ID)
	if err != nil || len(got.Occupants) != 1 || !got.Occupants[0].Deleted || got.Occupants[0].OrderID != order.Order.ID {
		t.Fatal("deletion removed unresolved payment/card history", err)
	}
}
