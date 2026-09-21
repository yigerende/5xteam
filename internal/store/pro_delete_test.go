package store

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProDeleteKeepsPaymentHistoryAndCardUsage(t *testing.T) {
	s := transferStore(t)
	const email = "delete@example.com"
	transferSource(t, s, email)
	capacityCard(t, s, "card")
	order := transferOrder(email, "paid-delete", "success")
	if err := s.CreateGPTPayOrder(order.Order, order.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProOAuthSession(model.ProOAuthSession{ID: "old-authorization", TargetEmail: email, ExpiresAt: time.Now().Add(time.Hour)}, "verifier"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProAccount(email); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MailAccountCredential(email); err == nil {
		t.Fatal("credentials retained")
	}
	if _, _, err := s.ProOAuthSession("old-authorization"); err == nil {
		t.Fatal("deleted account can still be authorized by old callback")
	}
	if _, _, err := s.GPTPayOrder(order.Order.ID); err != nil {
		t.Fatal("payment history removed", err)
	}
	card, _, err := s.GPTPayCard("card")
	if err != nil || card.OpenedAccounts != 1 {
		t.Fatal("deleting account reset card usage", card.OpenedAccounts, err)
	}
}

func TestProDeleteBlocksPendingPayment(t *testing.T) {
	for _, status := range []string{"submitting", "submission_unknown", "processing"} {
		t.Run(status, func(t *testing.T) {
			s := transferStore(t)
			const email = "pending@example.com"
			transferSource(t, s, email)
			order := transferOrder(email, "pending-delete", status)
			if err := s.CreateGPTPayOrder(order.Order, order.Snapshot); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteProAccount(email); err == nil {
				t.Fatal("deleted unresolved purchase")
			}
			if _, _, err := s.MailAccountCredential(email); err != nil {
				t.Fatal(err)
			}
		})
	}
}
