package store

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

func TestGPTPayOrderStateWithoutSnapshot(t *testing.T) {
	s := transferStore(t)
	for i, state := range []string{"success", "submission_unknown", "failed"} {
		order := gptpay.Order{ID: state, Email: state + "@example.test", Status: state, CreatedAt: time.Now()}
		snapshot := gptpay.Snapshot{}
		snapshot.Input.Number = fmt.Sprintf("42424242424%05d", i)
		if err := s.CreateGPTPayOrder(order, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE gptpay_orders SET encrypted_snapshot='unreadable'"); err != nil {
		t.Fatal(err)
	}
	reader, err := s.ProListReader(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// A busy main store cannot delay history reads or require sensitive snapshots.
	s.mu.Lock()
	done := make(chan error, 1)
	go func() {
		order, e := reader.GPTPayOrderState("success")
		if e == nil && order.Status != "success" {
			e = errors.New("wrong payment status")
		}
		done <- e
	}()
	select {
	case err = <-done:
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		s.mu.Unlock()
		t.Fatal("history lookup waited for main store")
	}
	order, err := reader.ActiveGPTPayOrderState("submission_unknown@example.test")
	if err != nil || order.Status != "submission_unknown" {
		t.Fatal("unknown payment hold lost", order.Status, err)
	}
	if _, err = reader.ActiveGPTPayOrderState("success@example.test"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err = reader.GPTPayOrderState("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	reader.Close()
	if _, err = s.db.Exec("UPDATE gptpay_orders SET profile=json_set(profile,'$.status',42) WHERE id='success'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GPTPayOrderState("success"); err == nil {
		t.Fatal("invalid order data treated as settled")
	}
}
