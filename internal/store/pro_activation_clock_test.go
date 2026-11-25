package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProActivationClockPaymentAndConcurrentRepush(t *testing.T) {
	s := transferStore(t)
	email := "activation@example.com"
	_, err := s.SaveMailAccountToPro(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email})
	if err != nil {
		t.Fatal(err)
	}
	order := gptpay.Order{ID: "paid", Email: email, Status: "processing", CreatedAt: time.Now().Add(-time.Minute)}
	if err = s.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"processing", "unknown", "failed"} {
		order.Status = status
		if err = s.UpdateGPTPayOrder(order); err != nil {
			t.Fatal(err)
		}
		p, _, _ := s.MailAccountCredential(email)
		if p.ProPushStartedAt != nil {
			t.Fatalf("%s started clock", status)
		}
	}
	order.Status = "success"
	if err = s.UpdateGPTPayOrder(order); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := s.GPTPayOrder(order.ID)
	p, _, _ := s.MailAccountCredential(email)
	if p.PushStatus == "completed" || p.ProPushStartedAt == nil || saved.SucceededAt == nil || !p.ProPushStartedAt.Equal(*saved.SucceededAt) || p.ProPushStartedSource != "activation" {
		t.Fatal("payment did not immediately start the clock before push")
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if err := s.UpdateGPTPayOrder(order); err != nil {
					t.Error(err)
				}
			} else {
				if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = []string{"running", "failed", "completed"}[i%3] }); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	if _, err = s.SaveMailAccountToPro(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.MailAccountCredential(email)
	if !p.ProPushStartedAt.Equal(*saved.SucceededAt) || p.ProPushStartedSource != "activation" {
		t.Fatal("repush/poll/import reset clock")
	}
}

func TestProActivationClockScopeAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, scope, flow, orderID string
		old                        bool
		want                       bool
	}{
		{"manual", "pro", "", "paid", false, true},
		{"automatic", "pro", "flow", "pro-auto-flow", false, true},
		{"unrelated_order", "pro", "flow", "old-order", false, false},
		{"mail_only", "mail", "", "paid", false, false},
		{"historic_clock", "pro", "", "paid", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := transferStore(t)
			email := "scope@example.com"
			old := time.Now().Add(-time.Hour)
			p := model.MailAccountProfile{Email: email, ManagementScope: tc.scope, ProAuto: model.ProAutoState{ID: tc.flow}}
			if tc.old {
				p.ProPushStartedAt = &old
			}
			if _, err := s.SaveMailAccount(p, model.MailAccountCredentials{Email: email}); err != nil {
				t.Fatal(err)
			}
			paid := time.Now().Add(-time.Minute)
			order := gptpay.Order{ID: tc.orderID, Email: email, Status: "success", SucceededAt: &paid, CreatedAt: paid}
			if err := s.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
				t.Fatal(err)
			}
			p, _, _ = s.MailAccountCredential(email)
			if tc.want {
				if p.ProPushStartedAt == nil || !p.ProPushStartedAt.Equal(paid) {
					t.Fatal("missing activation clock")
				}
				// Simulate a pre-upgrade paid account with no clock, then resume it.
				if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_remove(profile,'$.pro_push_started_at','$.pro_push_started_source') WHERE email=?`, email); err != nil {
					t.Fatal(err)
				}
				p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Steps = map[string]string{"recharge": "completed"} })
				if err != nil || p.ProPushStartedAt == nil || !p.ProPushStartedAt.Equal(paid) {
					t.Fatal("paid recovery changed activation time", err)
				}
			} else if tc.old {
				if p.ProPushStartedAt == nil || !p.ProPushStartedAt.Equal(old) {
					t.Fatal("historical clock moved")
				}
			} else if p.ProPushStartedAt != nil {
				t.Fatal("unrelated payment started clock")
			}
		})
	}
}

func TestProActivationClockRollbackAndImportedFallback(t *testing.T) {
	s := transferStore(t)
	email := "rollback@example.com"
	if _, err := s.SaveMailAccountToPro(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	order := gptpay.Order{ID: "rollback", Email: email, Status: "processing", CreatedAt: time.Now()}
	if err := s.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_clock BEFORE UPDATE ON mail_accounts WHEN json_extract(NEW.profile,'$.pro_push_started_at') IS NOT NULL BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	order.Status = "success"
	if err := s.UpdateGPTPayOrder(order); err == nil {
		t.Fatal("expected transaction failure")
	}
	saved, _, _ := s.GPTPayOrder(order.ID)
	if saved.Status != "processing" || saved.SucceededAt != nil {
		t.Fatal("partial payment commit")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_clock`); err != nil {
		t.Fatal(err)
	}
	p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
	if err != nil || p.ProPushStartedAt == nil || p.ProPushStartedSource != "push" {
		t.Fatal(fmt.Sprint("external account fallback: ", err))
	}
}
