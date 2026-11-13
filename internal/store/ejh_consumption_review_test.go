package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
)

func consumptionReviewFixture(t *testing.T) (*Store, ejh.Case, string) {
	t.Helper()
	s := transferStore(t)
	card := ejhFixtureCard(t, s)
	email := "interrupted@example.com"
	transferSource(t, s, email)
	o := transferOrder(email, "interrupted-order", "success")
	o.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_auto',json('{"id":"flow","order_id":"interrupted-order","status":"completed","stage":"merchant","steps":{"push":"interrupted"},"stage_times":{"push:running":"2026-11-12T23:02:14+08:00"}}')) WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	rows, err := s.EJHCases()
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	return s, rows[0], email
}

func reviewInput(row ejh.Case, state string) EJHConsumptionReviewInput {
	revision := int64(0)
	if row.ConsumptionReview != nil {
		revision = row.ConsumptionReview.Revision
	}
	return EJHConsumptionReviewInput{State: state, Reason: "原下游及历史使用记录均已核实，没有创建或消费", ExpectedKey: row.ConsumptionReviewKey, ExpectedRevision: &revision, Confirm: true}
}

func TestEJHConsumptionReviewPreservesHistoryAndConcurrentUpdates(t *testing.T) {
	s, row, email := consumptionReviewFixture(t)
	if row.ConsumptionState != "" || !row.ConsumptionCanReview {
		t.Fatal(row)
	}
	var beforeProfile, beforeOrder string
	s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&beforeProfile)
	s.db.QueryRow("SELECT profile FROM gptpay_orders WHERE id=?", row.ID).Scan(&beforeOrder)
	version, _ := s.EJHDisplayRevision(context.Background())
	input := reviewInput(row, "not_pushed")
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.SetEJHConsumptionReview(row.ID, input); results <- err }()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("duplicate write accepted: %d", succeeded)
	}
	rows, err := s.EJHCases()
	if err != nil {
		t.Fatal(err)
	}
	row = rows[0]
	if row.ConsumptionState != "not_pushed" || row.ConsumptionTotal != nil || row.ConsumptionReview == nil {
		t.Fatal(row)
	}
	var afterProfile, afterOrder string
	s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&afterProfile)
	s.db.QueryRow("SELECT profile FROM gptpay_orders WHERE id=?", row.ID).Scan(&afterOrder)
	if beforeProfile != afterProfile || beforeOrder != afterOrder {
		t.Fatal("review rewrote workflow/payment history")
	}
	var audits int
	s.db.QueryRow("SELECT count(*) FROM ejh_records WHERE kind='match_audit'").Scan(&audits)
	if audits != 1 {
		t.Fatal(audits)
	}
	next, _ := s.EJHDisplayRevision(context.Background())
	if next == version {
		t.Fatal("review did not invalidate view")
	}
	if _, err = s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_auto.stage_times."quota:waiting"','2026-11-13T12:00:00Z') WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	afterQuota, _ := s.EJHDisplayRevision(context.Background())
	if afterQuota != next {
		t.Fatal("unrelated quota timing invalidated financial view")
	}
	if _, err = s.SetEJHConsumptionReview(row.ID, reviewInput(row, "auto")); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.EJHCases()
	if rows[0].ConsumptionState != "" || rows[0].ConsumptionTotal != nil {
		t.Fatal(rows[0])
	}
	if _, err = s.SetEJHConsumptionReview(row.ID, reviewInput(rows[0], "not_pushed")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_auto.stage_times."push:running"','2026-11-13T12:00:00Z') WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.EJHCases()
	if rows[0].ConsumptionState != "" {
		t.Fatal("review carried into a new push")
	}
	changed, _ := s.EJHDisplayRevision(context.Background())
	if changed == next {
		t.Fatal("context update did not invalidate cache")
	}
}

func TestEJHConsumptionReviewRejectsEvidenceAndPendingCreates(t *testing.T) {
	for _, patch := range []string{`{"push_status":"completed"}`, `{"sub2_account_id":42}`, `{"pro_auto":{"status":"running"}}`, `{"pro_auto":{"order_id":"other"}}`, `{"pro_workflow_running":true}`, `{"user_cost_downstream_identity":"42","user_cost_downstream_snapshot":0}`, `{"pro_pending_push_target_id":"pending"}`} {
		t.Run(patch, func(t *testing.T) {
			s, row, email := consumptionReviewFixture(t)
			s.db.Exec(`UPDATE mail_accounts SET profile=json_patch(profile,?) WHERE email=?`, patch, email)
			rows, _ := s.EJHCases()
			if _, err := s.SetEJHConsumptionReview(row.ID, reviewInput(rows[0], "not_pushed")); err == nil {
				t.Fatal("invalid account accepted")
			}
		})
	}
	for _, prefix := range []string{"pro:", "pro-siwc:"} {
		t.Run(prefix, func(t *testing.T) {
			s, row, email := consumptionReviewFixture(t)
			p, _, err := s.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.SaveDownstreamTarget(model.DownstreamTarget{Provider: "sub2", Sub2: model.Sub2Settings{URL: "https://old.example"}, Secret: "private"})
			if err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("%x", sha256.Sum256([]byte("https://old.example\x00"+prefix+p.ID)))
			if err = s.SaveSub2PushAttempt(id, `{"input":{}}`); err != nil {
				t.Fatal(err)
			}
			if _, err = s.SetEJHConsumptionReview(row.ID, reviewInput(row, "not_pushed")); err == nil {
				t.Fatal("uncertain create hidden")
			}
		})
	}
	for _, value := range []float64{0, 12} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			s, row, email := consumptionReviewFixture(t)
			if err := s.EJHPut("finance_usage", "history", EJHConsumption{ID: "history", Email: email, Total: value}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetEJHConsumptionReview(row.ID, reviewInput(row, "not_pushed")); err == nil {
				t.Fatal("saved consumption hidden")
			}
		})
	}
}

func TestEJHConsumptionReviewSurvivesDeletionButKnownAmountWins(t *testing.T) {
	s, row, email := consumptionReviewFixture(t)
	if _, err := s.SetEJHConsumptionReview(row.ID, reviewInput(row, "not_pushed")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM mail_accounts WHERE email=?", email); err != nil {
		t.Fatal(err)
	}
	rows, err := s.EJHCases()
	if err != nil || rows[0].ConsumptionState != "not_pushed" || !rows[0].Deleted {
		t.Fatal(rows, err)
	}
	transferSource(t, s, email)
	if _, err = s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.user_cost_downstream_identity','42','$.user_cost_downstream_snapshot',0) WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	rows, err = s.EJHCases()
	if err != nil || rows[0].ConsumptionState != "" || rows[0].ConsumptionTotal == nil || *rows[0].ConsumptionTotal != 0 {
		t.Fatal(rows, err)
	}
	var saved string
	s.db.QueryRow("SELECT profile FROM ejh_cases WHERE id=?", row.ID).Scan(&saved)
	var persisted ejh.Case
	json.Unmarshal([]byte(saved), &persisted)
	if persisted.ConsumptionTotal != nil || persisted.ConsumptionReview == nil {
		t.Fatal("stored a fake amount or lost review")
	}
}
