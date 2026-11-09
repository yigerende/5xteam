package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProRestockCandidatesSkipHistoryPreservePaymentAndManualWork(t *testing.T) {
	s := transferStore(t)
	cases := []struct {
		name, status, payment                                    string
		auto, explicit, active, manual, recharged, tracked, want bool
		retry                                                    int
	}{
		{name: "completed", auto: true, status: "completed", payment: "success", explicit: true},
		{name: "historical_without_order", auto: true, status: "completed"},
		{name: "missing_link", auto: true, status: "completed", explicit: true, want: true},
		{name: "implicit_unknown", auto: true, status: "completed", payment: "submission_unknown", want: true},
		{name: "implicit_success", auto: true, status: "completed", payment: "success"},
		{name: "retry_success", auto: true, status: "completed", payment: "success", retry: 2},
		{name: "retry_unknown", auto: true, status: "completed", payment: "submission_unknown", retry: 2, want: true},
		{name: "review", auto: true, status: "completed", payment: "failed", explicit: true, want: true},
		{name: "active_payment", auto: true, status: "completed", payment: "success", active: true, want: true},
		{name: "running", auto: true, status: "running", want: true},
		{name: "idle"},
		{name: "manual_running", manual: true, want: true},
		{name: "manual_recharged", recharged: true, want: true},
		{name: "manual_payment", active: true, want: true},
		{name: "tracked", auto: true, status: "running", tracked: true},
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, tc := range cases {
		p := model.MailAccountProfile{Email: tc.name + "@example.test", ManagementScope: "pro"}
		if tc.auto {
			p.ProAuto = model.ProAutoState{ID: tc.name, Status: tc.status, RetryAttempts: tc.retry}
			if tc.explicit {
				p.ProAuto.OrderID = "explicit-" + tc.name
			}
		}
		if tc.manual {
			p.ProManualStages = map[string]model.ProStageProgress{"login": {Status: "running"}}
		}
		if tc.recharged {
			p.ProManualStages = map[string]model.ProStageProgress{"recharge": {Status: "completed"}}
		}
		if tc.tracked {
			p.ProRestockTaskID = "tracked-task"
		}
		if _, err = tx.Exec("INSERT INTO mail_accounts(email,profile,encrypted_credentials,updated_at) VALUES(?,?,?,?)", p.Email, mustJSON(p), "unreadable", formatTime(time.Now())); err != nil {
			t.Fatal(err)
		}
		if tc.payment != "" {
			id := p.ProAuto.OrderID
			if id == "" {
				id = p.ProAuto.PaymentOrderID()
			}
			order := gptpay.Order{ID: id, Email: p.Email, Status: tc.payment}
			if _, err = tx.Exec("INSERT INTO gptpay_orders(id,email,profile,encrypted_snapshot,active,created_at) VALUES(?,?,?,'unreadable',?,?)", id, p.Email, mustJSON(order), order.Active(), formatTime(time.Now())); err != nil {
				t.Fatal(err)
			}
		}
		if tc.active {
			order := gptpay.Order{ID: "active-" + tc.name, Email: p.Email, Status: "submission_unknown"}
			if _, err = tx.Exec("INSERT INTO gptpay_orders(id,email,profile,encrypted_snapshot,active,created_at) VALUES(?,?,?,'unreadable',1,?)", order.ID, p.Email, mustJSON(order), formatTime(time.Now())); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Large finished history must not enter the candidate list at all.
	for i := 0; i < 1000; i++ {
		p := model.MailAccountProfile{Email: fmt.Sprintf("history-%d@example.test", i), ManagementScope: "pro", ProAuto: model.ProAutoState{ID: fmt.Sprint(i), Status: "completed"}}
		if _, err = tx.Exec("INSERT INTO mail_accounts(email,profile,encrypted_credentials,updated_at) VALUES(?,?,?,?)", p.Email, mustJSON(p), "unreadable", formatTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	reader, err := s.ProListReader(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	type result struct {
		profiles []model.MailAccountProfile
		err      error
	}
	s.mu.Lock()
	done := make(chan result, 1)
	go func() { profiles, err := reader.ProRestockExternalProfiles(t.Context()); done <- result{profiles, err} }()
	var got result
	select {
	case got = <-done:
		s.mu.Unlock()
	case <-time.After(3 * time.Second):
		s.mu.Unlock()
		t.Fatal("history reconciliation waited for business write lock")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	byEmail := map[string]bool{}
	for _, p := range got.profiles {
		byEmail[p.Email] = true
	}
	wantCount := 0
	for _, tc := range cases {
		if tc.want {
			wantCount++
		}
		if byEmail[tc.name+"@example.test"] != tc.want {
			t.Errorf("%s included=%v want=%v", tc.name, byEmail[tc.name+"@example.test"], tc.want)
		}
	}
	if len(got.profiles) != wantCount {
		t.Fatalf("unexpected candidates: got %d want %d", len(got.profiles), wantCount)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = reader.ProRestockExternalProfiles(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read continued", err)
	}
}
