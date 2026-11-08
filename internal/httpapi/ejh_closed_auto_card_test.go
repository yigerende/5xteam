package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestEJHAutoOpeningAfterClosedHistoricalCard(t *testing.T) {
	for _, tc := range []struct {
		name, payment, status                              string
		released, locked, missing, countLimit, budgetLimit bool
		legacyRejected                                     bool
		wantOpen                                           bool
	}{
		{name: "unused_closed", status: "2", wantOpen: true},
		{name: "closed_legacy_rejected_increase", status: "2", legacyRejected: true, wantOpen: true},
		{name: "unused_closing", status: "8", wantOpen: true},
		{name: "closing_locked", status: "8", locked: true, wantOpen: true},
		{name: "closing_unknown_payment", payment: "submission_unknown", status: "8", wantOpen: true},
		{name: "failed_payment_closed", payment: "failed", status: "2", wantOpen: true},
		{name: "used_released_closed", payment: "success", released: true, status: "2", wantOpen: true},
		{name: "closed_unknown_payment", payment: "submission_unknown", status: "2", wantOpen: true},
		{name: "closed_review_payment", payment: "manual_review", status: "2", wantOpen: true},
		{name: "closed_active_account", payment: "success", status: "2", wantOpen: true},
		{name: "unused_unconfirmed", status: "0", wantOpen: true},
		{name: "unused_locked", status: "1", locked: true, wantOpen: true},
		{name: "closed_locked", status: "2", locked: true, wantOpen: true},
		{name: "missing_card", status: "2", missing: true, wantOpen: true},
		{name: "closed_still_counts_daily_limit", status: "2", countLimit: true},
		{name: "closed_still_counts_daily_budget", status: "2", budgetLimit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, issued, paid, _ := allocationFixture(t)
			remote := ejh.Card{OrderNo: "historical-auto-card", Number: "5555555555559974", CVV: "123", Validity: "1299", Status: "1", CardCurrency: "USD"}
			old, err := s.store.ImportEJHCard(remote, true)
			if err != nil {
				t.Fatal(err)
			}
			if tc.payment != "" {
				email := "historical-card@example.com"
				allocationAccount(t, s, email)
				_, secret, err := s.store.GPTPayCard(old.ID)
				if err != nil {
					t.Fatal(err)
				}
				err = s.store.CreateGPTPayOrder(gptpay.Order{ID: "historical-payment", Email: email, CardID: old.ID, Status: tc.payment, CreatedAt: time.Now()}, gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: secret}})
				if err != nil {
					t.Fatal(err)
				}
				if tc.released {
					if _, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" }); err != nil {
						t.Fatal(err)
					}
					ejhReceiveOrderAPI(t, s, old.ID, "historical-payment")
				}
			}
			remote.Status = tc.status
			if tc.locked {
				remote.Locked = "1"
			}
			old, err = s.store.ImportEJHCard(remote, false)
			if err != nil {
				t.Fatal(err)
			}
			if old.Available {
				t.Fatal("historical fixture unexpectedly usable")
			}
			if tc.legacyRejected {
				legacy := ejh.Operation{ID: "old-increase", Method: "increase", CardID: old.ID, OrderNo: remote.OrderNo, Status: "unknown", Error: "EJH 800006: legacy rejection", CreatedAt: time.Now()}
				if err = s.store.EJHBeginOperation(legacy); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			op := ejh.Operation{ID: "historical-auto-open", Method: "open", CardID: old.ID, OrderNo: remote.OrderNo, Status: "completed", Automatic: true, Amount: "20", Currency: "USD", CreatedAt: now, UpdatedAt: now}
			if err = s.store.EJHPut("operation", op.ID, op); err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				if err = s.store.DeleteGPTPayCard(old.ID); err != nil {
					t.Fatal(err)
				}
			}
			pro, _, _, err := s.store.ProSettings()
			if err != nil {
				t.Fatal(err)
			}
			if tc.countLimit {
				pro.EJHDailyOpenLimit = 1
			}
			if tc.budgetLimit {
				pro.EJHDailyAmountLimit = "39.99"
			}
			if _, err = s.store.SaveProSettings(pro, "", ""); err != nil {
				t.Fatal(err)
			}
			next, err := s.ensureEJHCard(context.Background())
			if tc.wantOpen {
				if err != nil || !next.Available || next.ID == old.ID || issued.Load() != 1 {
					t.Fatalf("closed history blocked replenishment: card=%s issued=%d error=%v", next.ID, issued.Load(), err)
				}
				// Repeated evaluation reuses the new card instead of issuing another.
				again, err := s.ensureEJHCard(context.Background())
				if err != nil || again.ID != next.ID || issued.Load() != 1 {
					t.Fatalf("duplicate issuance: %s %d %v", again.ID, issued.Load(), err)
				}
			} else {
				if err == nil || issued.Load() != 0 {
					t.Fatalf("guard bypass: issued=%d error=%v", issued.Load(), err)
				}
				if (tc.countLimit || tc.budgetLimit) && !strings.Contains(err.Error(), "每日数量或刷卡额度预算上限") {
					t.Fatalf("closed history did not reach budget guard: %v", err)
				}
			}
			if paid.Load() != 0 {
				t.Fatal("unexpected Pro payment")
			}
		})
	}
}

func settledEJHHistory(t *testing.T, s *Server) {
	t.Helper()
	card, err := s.store.ImportEJHCard(ejh.Card{OrderNo: "settled-history", Number: "5555555555559974", CVV: "123", Validity: "1299", Status: "2", CardCurrency: "USD"}, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	op := ejh.Operation{ID: "settled-auto-open", Method: "open", CardID: card.ID, Status: "completed", Automatic: true, Amount: "20", Currency: "USD", CreatedAt: now, UpdatedAt: now}
	if err = s.store.EJHPut("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
}

// A settled historical card is only one condition: all remaining admission
// checks still run before issuing a new card or reserving an existing one.
func TestEJHAutoOpeningClosedHistoryPreservesAdmissionGuards(t *testing.T) {
	for _, tc := range []struct {
		guard, wantError string
		wantIssue        bool
	}{
		{guard: "existing_card"},
		{guard: "auto_disabled", wantError: "未启用"},
		{guard: "template_missing", wantError: "模板"},
		{guard: "submitting", wantError: "未核实开卡"},
		{guard: "unknown", wantError: "未核实开卡"},
		{guard: "import_pending", wantError: "未核实开卡"},
		{guard: "failed_recent", wantError: "冷却"},
		{guard: "failed_old", wantIssue: true},
		{guard: "currency_changed", wantError: "币种"},
		{guard: "budget_exact", wantIssue: true},
		{guard: "schedule_maximum", wantError: "最大未合并数"},
		{guard: "stopped", wantError: "用户已停止"},
		{guard: "restock_owned", wantError: "自动补货领取"},
		{guard: "cancelled", wantError: "context canceled"},
	} {
		t.Run(tc.guard, func(t *testing.T) {
			s, issued, paid, email := allocationFixture(t)
			settledEJHHistory(t, s)
			pro, _, _, err := s.store.ProSettings()
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			enforceMaximum := false
			switch tc.guard {
			case "existing_card":
				// Even exhausted issuance limits must not prevent reusing a free card.
				pro.EJHDailyOpenLimit = 1
				pro.EJHDailyAmountLimit = "20"
				_, err = s.store.UpdateProAccount("resident@example.com", func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" })
				ejhReceiveOrderAPI(t, s, "test-card", "resident-order")
			case "auto_disabled":
				pro.EJHAutoOpen = false
			case "template_missing":
				pro.EJHTemplateID = "missing"
			case "submitting", "unknown", "import_pending", "failed_recent", "failed_old":
				op := ejh.Operation{ID: "other-opening", Method: "open", Status: tc.guard, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if strings.HasPrefix(tc.guard, "failed_") {
					op.Status, op.Automatic = "failed", true
					if tc.guard == "failed_old" {
						op.UpdatedAt = time.Now().Add(-6 * time.Minute)
					}
				}
				err = s.store.EJHPut("operation", op.ID, op)
			case "currency_changed":
				var op ejh.Operation
				if err = s.store.EJHGet("operation", "settled-auto-open", &op); err == nil {
					op.Currency = "EUR"
					err = s.store.EJHPut("operation", op.ID, op)
				}
			case "budget_exact":
				pro.EJHDailyAmountLimit = "40"
			case "schedule_maximum":
				pro.MaxUnmerged = 1
				enforceMaximum = true
			case "stopped":
				enforceMaximum = true
				_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
			case "restock_owned":
				_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProRestockTaskID = "other-task" })
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.store.SaveProSettings(pro, "", ""); err != nil {
				t.Fatal(err)
			}
			card, _, err := s.reserveProStartCard(ctx, email, "", true, enforceMaximum)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("wrong admission result: %v, want %q", err, tc.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantIssued := int32(0)
			if tc.wantIssue {
				wantIssued = 1
			}
			if issued.Load() != wantIssued || paid.Load() != 0 {
				t.Fatalf("issued=%d want=%d paid=%d", issued.Load(), wantIssued, paid.Load())
			}
			if tc.guard == "existing_card" && card.ID != "test-card" {
				t.Fatalf("did not reuse released card: %s", card.ID)
			}
		})
	}
}

func TestEJHAutoOpeningClosedHistoryThroughEntrypoints(t *testing.T) {
	for _, mode := range []string{"manual", "auto", "timer", "restock"} {
		t.Run(mode, func(t *testing.T) {
			s, issued, paid, email := allocationFixture(t)
			settledEJHHistory(t, s)
			if mode == "manual" {
				input := payInput("closed-history-request")
				input["card_id"] = ""
				first := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
				again := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
				if first.Status != "success" || first.ID != again.ID || issued.Load() != 1 || paid.Load() != 1 {
					t.Fatal("manual issuance or payment was blocked or duplicated")
				}
				return
			}
			// Keep login in flight, before payment/push: another tick/start must
			// respect the reserved card and task instead of buying a second card.
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			started := make(chan struct{})
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				close(started)
				<-ctx.Done()
				return errors.New("fixture stops before payment")
			}}
			switch mode {
			case "auto":
				for range 2 {
					rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"plan_code": "pro20"})
					if rec.Code != http.StatusAccepted {
						t.Fatal(rec.Body.String())
					}
				}
			case "timer":
				s.tickProSchedule(true)
				s.tickProSchedule(true)
			case "restock":
				c := restock.DefaultConfig()
				c.Enabled, c.Mode, c.GroupID, c.EmptyGroupCount = true, "auto", 1, 1
				if _, err := s.store.SaveProRestockConfig(c); err != nil {
					t.Fatal(err)
				}
				p, _, _, _ := s.store.ProSettings()
				pay, _, _ := s.store.GPTPaySettings()
				s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
					return restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}, nil
				}
				for range 2 {
					if err := s.tickProRestock(context.Background(), true); err != nil {
						t.Fatal(err)
					}
				}
				tasks, err := s.store.ProRestockTasks()
				if err != nil || len(tasks) != 1 || !tasks[0].Active() {
					t.Fatalf("inflight task lost or duplicated: %+v %v", tasks, err)
				}
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				tasks, _ := s.store.ProRestockTasks()
				t.Fatalf("background card preparation did not reach login: %+v", tasks)
			}
			profile, _, err := s.store.MailAccountCredential(email)
			if err != nil || !s.proAutomationRunning(email) || issued.Load() != 1 || paid.Load() != 0 {
				t.Fatalf("startup failed or duplicated: issued=%d paid=%d err=%v", issued.Load(), paid.Load(), err)
			}
			card, _, err := s.store.GPTPayCard(profile.ProAuto.CardID)
			if err != nil || card.Source != "ejh" || card.PendingAccounts != 1 || card.ActiveAccounts != 1 || card.Available {
				t.Fatalf("card was not reserved during login: %+v %v", card, err)
			}
			stop()
			waitProAutoFinished(t, s, email)
		})
	}
}
