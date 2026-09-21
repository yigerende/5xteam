package store

import (
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

func TestProActivationCardsSuccessfulSnapshot(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	snapshot := gptpay.Snapshot{}
	orders := []gptpay.Order{
		{ID: "old", Email: "pro@example.com", Status: "success", CardName: "旧卡", CardLast4: "1111", CreatedAt: now.Add(-3 * time.Hour)},
		{ID: "paid", Email: "pro@example.com", Status: "success", CardName: "开通时卡名", CardLast4: "4242", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "failed", Email: "pro@example.com", Status: "failed", CardName: "失败卡", CardLast4: "3333", CreatedAt: now.Add(-time.Hour)},
		{ID: "pending", Email: "pro@example.com", Status: "processing", CardName: "待处理卡", CardLast4: "5555", CreatedAt: now},
		{ID: "unrelated", Email: "other@example.com", Status: "success", CardName: "其他账号卡", CardLast4: "6666", CreatedAt: now},
	}
	for _, order := range orders {
		if err = s.CreateGPTPayOrder(order, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	emails := []string{"PRO@example.com", "unknown@example.com"}
	for i := 0; i < 405; i++ {
		emails = append(emails, fmt.Sprintf("empty-%d@example.com", i))
	}
	cards, err := s.ProActivationCards(emails)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards["pro@example.com"].CardName != "开通时卡名" || cards["pro@example.com"].CardLast4 != "4242" {
		t.Fatalf("wrong card snapshot: %+v", cards)
	}
	// No card rows exist: deleting/editing the card cannot erase the order snapshot.
	orders[3].Status = "success"
	if err = s.UpdateGPTPayOrder(orders[3]); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ProActivationCards([]string{"pro@example.com"})
	if err != nil || cards["pro@example.com"].CardLast4 != "5555" {
		t.Fatal("new successful order not reflected")
	}
	cards, err = s.ProActivationCards(nil)
	if err != nil || len(cards) != 0 {
		t.Fatal("empty page lookup failed")
	}
}
