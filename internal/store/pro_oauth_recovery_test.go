package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestOAuthRecoveryConcurrentDurableBudget(t *testing.T) {
	s := transferStore(t)
	var wg sync.WaitGroup
	for account := range 4 {
		email := fmt.Sprintf("identity%d@example.com", account)
		transferSource(t, s, email)
		a := model.ProAutoState{ID: email, OrderID: "paid-" + email, Status: "running", Stage: "oauth", Steps: map[string]string{"recharge": "completed"}, RetryAttempts: 3, StartedAt: time.Now()}
		o := transferOrder(email, a.OrderID, "success")
		o.Snapshot.Input.CardSecret.Number = fmt.Sprintf("42424242424242%02d", account)
		if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
			t.Fatal(err)
		}
		clock := time.Now().Add(-time.Hour)
		if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProAuto = a
			p.ProPushStartedAt = &clock
			p.ProRestockTaskID = "original-task"
		}); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var callers sync.WaitGroup
			var claims atomic.Int32
			for range 24 {
				callers.Add(1)
				go func() {
					defer callers.Done()
					if _, err := s.PrepareProOAuthRecovery(email, a, "siwc_local", model.ProDownstreamRef{Provider: "sub2", URL: "http://fixture.invalid"}, 2, time.Now().Add(10*time.Second)); err == nil {
						claims.Add(1)
					}
				}()
			}
			callers.Wait()
			if claims.Load() != 1 {
				t.Errorf("%s: claims=%d", email, claims.Load())
			}
			p, _, err := s.MailAccountCredential(email)
			if err != nil || p.ProAuto.OAuthRecovery == nil || p.ProAuto.OAuthRecovery.Attempts != 1 {
				t.Errorf("claim not persisted: %s %v", email, err)
				return
			}
			if p.ProAuto.OrderID != a.OrderID || p.ProAuto.RetryAttempts != 3 || !p.ProPushStartedAt.Equal(clock) || p.ProRestockTaskID != "original-task" {
				t.Error("payment, clock or restock identity changed")
			}
		}()
	}
	wg.Wait()
	if err := s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	for account := range 4 {
		email := fmt.Sprintf("identity%d@example.com", account)
		p, _, _ := s.MailAccountCredential(email)
		if p.ProAuto.Status != "interrupted" || p.ProAuto.OAuthRecovery.Attempts != 1 || p.ProAuto.OAuthRecovery.NextAt == nil {
			t.Fatal("restart lost recovery", p.ProAuto)
		}
		p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "running" })
		if err != nil {
			t.Fatal(err)
		}
		p, err = s.PrepareProOAuthRecovery(email, p.ProAuto, "siwc_local", model.ProDownstreamRef{Provider: "sub2", URL: "http://fixture.invalid"}, 2, time.Now())
		if err != nil || p.ProAuto.OAuthRecovery.Attempts != 2 || p.ProAuto.OAuthRecovery.Exhausted {
			t.Fatal("second recovery", err)
		}
		p, err = s.PrepareProOAuthRecovery(email, p.ProAuto, "siwc_local", model.ProDownstreamRef{Provider: "sub2", URL: "http://fixture.invalid"}, 2, time.Now())
		if err != nil || !p.ProAuto.OAuthRecovery.Exhausted || p.ProAuto.OAuthRecovery.Attempts != 2 || p.ProAuto.SIWCConsentFinal() {
			t.Fatal("budget or final disposition", err)
		}
	}
}

func TestOAuthRecoveryRejectsChangedOrUnpaidWorkflow(t *testing.T) {
	for _, kind := range []string{"stopped", "flow", "generation", "unpaid", "imported", "disposition"} {
		t.Run(kind, func(t *testing.T) {
			s := transferStore(t)
			email := "identity@example.com"
			transferSource(t, s, email)
			a := model.ProAutoState{ID: "flow", OrderID: "paid", Status: "running", Stage: "oauth", Steps: map[string]string{"recharge": "completed"}}
			o := transferOrder(email, a.OrderID, "success")
			if kind == "unpaid" {
				o.Order.Status = "processing"
			}
			if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
				t.Fatal(err)
			}
			_, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = a
				switch kind {
				case "stopped":
					p.ProAuto.StopRequested = true
				case "imported":
					p.Sub2AccountID = 99
				case "disposition":
					p.SpaceMergedOnce = true
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "flow" {
				a.ID = "stale"
			}
			if kind == "generation" {
				a.StopVersion++
			}
			if _, err = s.PrepareProOAuthRecovery(email, a, "siwc_local", model.ProDownstreamRef{Provider: "sub2", URL: "http://fixture.invalid"}, 2, time.Now()); err == nil {
				t.Fatal("guard bypassed")
			}
		})
	}
}
