package store

import (
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeleteEJHUnopenedCasesPreservesFinancialHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	card := ejhFixtureCard(t, s)
	settings, secret, err := s.EJHSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.SingleActiveAccount = false
	settings.DefaultMaxAccounts = 100
	if _, err = s.SaveEJHSettings(settings, secret); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, status string }{{"returned", "success"}, {"failed", "failed"}, {"pending", "processing"}, {"paid", "success"}, {"waiting", "success"}, {"other-card", "failed"}} {
		o := transferOrder(item.id+"@example.com", item.id, item.status)
		o.Order.CardID = card.ID
		if item.id == "other-card" {
			o.Snapshot.Input.Number = "5555555555554444"
			o.Order.CardID = "other-card"
		}
		if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
			t.Fatal(err)
		}
		if item.id == "returned" {
			ejhReceiveForOrder(t, s, card.ID, "returned")
		}
	}
	if err = s.EJHMarkRequested("waiting@example.com", "manual", time.Now()); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.GPTPayCard(card.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"paid", "returned", "waiting", "other-card"} {
		if _, err = s.DeleteEJHUnopenedCases(id); err == nil {
			t.Fatal("deleted protected record", id)
		}
	}
	if _, err = s.DeleteEJHUnopenedCases("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	ids, err := s.DeleteEJHUnopenedCases("failed")
	if err != nil || !reflect.DeepEqual(ids, []string{"failed"}) {
		t.Fatal(ids, err)
	}
	if ids, err = s.DeleteEJHUnopenedCases("failed"); err != nil || len(ids) != 0 {
		t.Fatal("not idempotent", ids, err)
	}
	if ids, err = s.DeleteEJHUnopenedCases(""); err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	if ids, err = s.DeleteEJHUnopenedCases(""); err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	after, _, err := s.GPTPayCard(card.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("deletion changed card occupancy or capacity", before, after, err)
	}
	for _, id := range []string{"failed", "pending", "paid", "returned", "waiting", "other-card"} {
		if _, _, err = s.GPTPayOrder(id); err != nil {
			t.Fatal("original payment order deleted", id, err)
		}
	}
	if err = s.EJHBackfillCases(); err != nil {
		t.Fatal(err)
	}
	if err = s.EJHReconcile(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := s.EJHCases()
	if err != nil || len(cases) != 6 {
		t.Fatal("reconciliation history removed", len(cases), err)
	}
	for _, c := range cases {
		wantRemoved := c.ID == "failed"
		if (c.RemovedAt != nil) != wantRemoved {
			t.Fatal("restart lost removal marker", c.ID, c.RemovedAt)
		}
		if c.ID == "returned" && (c.State != "returned" || len(c.Returns) == 0) {
			t.Fatal("refund evidence lost", c)
		}
	}
	order, _, err := s.GPTPayOrder("pending")
	if err != nil || !order.Active() {
		t.Fatal("deletion stopped payment tracking", order, err)
	}
	order.Status = "success"
	if err = s.UpdateGPTPayOrder(order); err != nil {
		t.Fatal(err)
	}
	cases, err = s.EJHCases()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.ID == "pending" && (c.State != "opened" || c.RemovedAt != nil) {
			t.Fatal("later payment not tracked", c)
		}
	}
}

func TestDeleteEJHUnopenedCasesConcurrentAndManualState(t *testing.T) {
	s := transferStore(t)
	card := ejhFixtureCard(t, s)
	o := transferOrder("manual@example.com", "manual-delete", "success")
	o.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEJHCaseState(o.Order.ID, caseStateInput(t, s, o.Order.ID, "unopened")); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.GPTPayCard(card.ID)
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := s.DeleteEJHUnopenedCases(o.Order.ID)
			if err != nil {
				t.Error(err)
				return
			}
			count.Add(int32(len(ids)))
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("concurrent deletion duplicated", count.Load())
	}
	after, _, _ := s.GPTPayCard(card.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("deletion changed manual state or cumulative usage")
	}
	cases, err := s.EJHCases()
	if err != nil || cases[0].StateOverride == nil || cases[0].StateOverride.State != "unopened" {
		t.Fatal(cases, err)
	}
	if err = s.UpdateGPTPayOrder(o.Order); err != nil {
		t.Fatal(err)
	}
	cases, err = s.EJHCases()
	if err != nil || cases[0].RemovedAt == nil {
		t.Fatal("unchanged success refresh restored manually unopened record", cases, err)
	}
	if _, err = s.SetEJHCaseState(o.Order.ID, caseStateInput(t, s, o.Order.ID, "auto")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteEJHUnopenedCases(o.Order.ID); err == nil {
		t.Fatal("stale deletion accepted after state changed")
	}
	if row, err := s.SetEJHCaseState(o.Order.ID, caseStateInput(t, s, o.Order.ID, "unopened")); err != nil || row.RemovedAt != nil {
		t.Fatal("explicit state change did not restore record", row, err)
	}
	audits, err := EJHRecords[map[string]any](s, "match_audit")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range audits {
		if a["action"] == "delete_unopened_case" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("missing or duplicate deletion audit", n)
	}
}
