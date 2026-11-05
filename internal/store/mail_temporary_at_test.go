package store

import (
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestTemporaryATDeletionKeepsHistoryAndIsIdempotent(t *testing.T) {
	s := transferStore(t)
	const email = "temporary-at-delete@example.com"
	transferSource(t, s, email)
	order := transferOrder(email, "historical-payment", "failed")
	if err := s.CreateGPTPayOrder(order.Order, order.Snapshot); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.GPTPayOrder(order.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.DeleteMailAccountAfterTemporaryATFailure(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.MailAccountCredential(email); err == nil {
		t.Fatal("account remains")
	}
	after, _, err := s.GPTPayOrder(order.Order.ID)
	if err != nil || mustJSON(before) != mustJSON(after) {
		t.Fatal("payment history changed", err)
	}
}

func TestTemporaryATDeletionRejectsChangedOrReimportedAccount(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		s := transferStore(t)
		const email = "temporary-at-new@example.com"
		p, err := s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email})
		if err != nil {
			t.Fatal(err)
		}
		if recreate {
			if err = s.DeleteMailAccountFromManagement(email); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "new-at"}); err != nil {
			t.Fatal(err)
		}
		if err = s.DeleteMailAccountAfterTemporaryATFailure(p); err == nil {
			t.Fatal("stale delete accepted")
		}
		_, c, err := s.MailAccountCredential(email)
		if err != nil || c.AccessToken != "new-at" {
			t.Fatal("new credentials removed", err)
		}
	}
}
