package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProSessionRecoveryClaimSurvivesRestart(t *testing.T) {
	s := transferStore(t)
	email := "session@example.com"
	transferSource(t, s, email)
	now := time.Now()
	a := model.ProAutoState{ID: "original-flow", Status: "running", Stage: "recharge", Steps: map[string]string{"login": "completed", "recharge": "running"}, PlanCode: "pro20", CardID: "same-card", RetryAttempts: 2, ProxyRetryAttempts: 1, StartedAt: now}
	a.OrderID = a.PaymentOrderID()
	o := transferOrder(email, a.OrderID, "failed")
	o.Order.Provider, o.Order.CardID, o.Order.LastStatusCheckAt = gptpay.XUPay, a.CardID, &now
	r := &o.Order.Remote
	r.TargetEmail, r.PlanCode, r.FailureCategory = email, a.PlanCode, "unpaid_session_expired"
	r.PaymentState, r.SettlementStatus = "failed", "released"
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto = a }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var wins atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PrepareProSessionRecovery(email, a, "session expired"); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims succeeded", wins.Load())
	}
	if err := s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	p, _, err := s.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	got := p.ProAuto
	if got.Status != "interrupted" || got.SessionRecoveryOrderID != a.OrderID || got.OrderID != "" || got.PaymentOrderID() != a.OrderID+"-session-1" || got.RetryAttempts != 2 || got.ProxyRetryAttempts != 1 || !got.StartedAt.Equal(now) {
		t.Fatal("restart changed recovered flow", got)
	}
	if _, err = s.PrepareProSessionRecovery(email, got, "duplicate"); err == nil {
		t.Fatal("restart granted another claim")
	}
}

func TestProAutoRetryPaymentAndStateGuards(t *testing.T) {
	for _, scenario := range []string{"no_order", "failed", "success", "processing", "submission_unknown", "manual_review", "missing_order", "unlinked_success", "once_successful", "provider_blocked", "payment_risk", "stopped", "stale_flow", "stale_attempt", "exhausted", "paid_stage"} {
		t.Run(scenario, func(t *testing.T) {
			s := transferStore(t)
			email := "retry@example.com"
			transferSource(t, s, email)
			state := model.ProAutoState{ID: "flow", Status: "running", Stage: "login", CardID: "same-card", StopVersion: 7, Steps: map[string]string{"login": "running"}, StartedAt: time.Now().Add(-time.Minute)}
			if scenario == "stopped" {
				state.StopRequested = true
			}
			if scenario == "exhausted" {
				state.RetryAttempts = 2
			}
			if scenario == "paid_stage" {
				state.Steps["recharge"] = "completed"
			}
			if scenario == "missing_order" {
				state.OrderID = "missing"
			}
			switch scenario {
			case "failed", "success", "processing", "submission_unknown", "manual_review", "unlinked_success", "once_successful", "provider_blocked", "payment_risk":
				status := scenario
				if scenario == "unlinked_success" {
					status = "success"
				}
				if scenario == "once_successful" || scenario == "provider_blocked" || scenario == "payment_risk" {
					status = "failed"
				}
				o := transferOrder(email, state.PaymentOrderID(), status)
				if scenario == "payment_risk" {
					o.Order.Error = "OpenAI 支付风控拦截"
				}
				if scenario == "provider_blocked" {
					no := false
					o.Order.Provider, o.Order.Remote.Retryable = gptpay.CMSNav, &no
				}
				if scenario == "once_successful" {
					o.Order.Status = "success"
				}
				if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
					t.Fatal(err)
				}
				if scenario == "once_successful" {
					o.Order.Status = "failed"
					if err := s.UpdateGPTPayOrder(o.Order); err != nil {
						t.Fatal(err)
					}
				}
				if scenario != "unlinked_success" {
					state.OrderID = o.Order.ID
				}
			}
			p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = state
				p.ProRestockTaskID, p.ProRestockTargetID = "original-batch", "original-target"
			})
			if err != nil {
				t.Fatal(err)
			}
			expected := p.ProAuto
			if scenario == "stale_flow" {
				expected.ID = "stale"
			}
			if scenario == "stale_attempt" {
				expected.RetryAttempts++
			}
			at := time.Now().Add(time.Second)
			retried, err := s.PrepareProAutoRetry(email, expected, 2, at, "retry pending")
			if scenario != "no_order" && scenario != "failed" {
				if err == nil {
					t.Fatal("unsafe retry accepted", scenario)
				}
				saved, _, _ := s.MailAccountCredential(email)
				if saved.ProAuto.RetryAttempts != p.ProAuto.RetryAttempts || saved.ProAuto.OrderID != p.ProAuto.OrderID {
					t.Fatal("rejected retry changed progress")
				}
				return
			}
			if err != nil || retried.ProAuto.ID != state.ID || retried.ProAuto.RetryAttempts != 1 || retried.ProAuto.PaymentOrderID() != "pro-auto-flow-retry-1" || retried.ProRestockTaskID != "original-batch" || retried.ProRestockTargetID != "original-target" || retried.ProAuto.CardID != "same-card" || !retried.ProAuto.StartedAt.Equal(state.StartedAt) || !retried.ProAuto.RetryAt.Equal(at) {
				t.Fatal("retry did not preserve original task", retried.ProAuto, err)
			}
			if _, err = s.PrepareProAutoRetry(email, expected, 2, at, "duplicate"); err == nil {
				t.Fatal("duplicate retry incremented attempts")
			}
			if err = s.RecoverProAutomations(); err != nil {
				t.Fatal(err)
			}
			recovered, _, _ := s.MailAccountCredential(email)
			if recovered.ProAuto.RetryAttempts != 1 || recovered.ProAuto.PaymentOrderID() != retried.ProAuto.PaymentOrderID() {
				t.Fatal("restart reset attempts or payment key")
			}
		})
	}
}
