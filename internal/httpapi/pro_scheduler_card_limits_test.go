package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Exercise the actual timer -> card selection -> atomic reservation -> payment
// -> push/quota path. Only external login/payment/Sub calls use local fixtures.
func TestProSchedulerPerCardLimits(t *testing.T) {
	type cardCase struct {
		limit, opened, pending int
		disabled, expired      bool
	}
	for _, tc := range []struct {
		name                         string
		global, maximum, concurrency int
		cards                        []cardCase
		capacity, want               int
		duplicate, lowerDuringLogin  bool
	}{
		{"override_above_global", 3, 20, 10, []cardCase{{limit: 5}}, 5, 5, false, false},
		{"override_below_global", 5, 20, 10, []cardCase{{limit: 2}}, 2, 2, false, false},
		{"reset_uses_global", 3, 20, 10, []cardCase{{opened: 1, pending: 1}}, 1, 1, false, false},
		{"paid_and_pending_deducted", 3, 20, 10, []cardCase{{limit: 5, opened: 2, pending: 1}}, 2, 2, false, false},
		{"full_card_starts_nothing", 5, 20, 10, []cardCase{{limit: 2, opened: 1, pending: 1}}, 0, 0, false, false},
		{"skip_full_card_use_other", 5, 20, 10, []cardCase{{limit: 1, opened: 1}, {limit: 3, opened: 1, pending: 1}}, 1, 1, false, false},
		{"duplicate_card_no_extra_capacity", 5, 20, 10, []cardCase{{limit: 2}}, 2, 2, true, false},
		{"population_maximum_still_applies", 3, 3, 10, []cardCase{{limit: 5, opened: 1, pending: 1}}, 3, 1, false, false},
		{"concurrency_still_applies", 3, 20, 2, []cardCase{{limit: 5}}, 5, 2, false, false},
		{"disabled_and_expired_excluded", 3, 20, 10, []cardCase{{limit: 5, disabled: true}, {limit: 5, expired: true}, {limit: 2}}, 2, 2, false, false},
		{"lower_after_reservation_keeps_admitted_work", 3, 20, 10, []cardCase{{limit: 3}}, 3, 3, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var purchases, logins, pushes, quotas atomic.Int32
			s, st, _ := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/gpt-recharge/orders" {
					t.Errorf("unexpected supplier request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
					return
				}
				var input gptpay.CreateInput
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				purchases.Add(1)
				fmt.Fprintf(w, `{"code":0,"data":{"id":%q,"status":"success"}}`, input.Session.User.Email)
			})
			// Drive timer ticks explicitly below; the wall-clock monitor would race
			// the due-time assertions and can legitimately consume the next tick.
			s.proAutoCancel()
			s.proAutoWG.Wait()
			configureProAutoFixture(t, s)
			cfg, _, _, err := st.ProSettings()
			if err != nil {
				t.Fatal(err)
			}
			cfg.ScheduledEnabled, cfg.MaxUnmerged, cfg.Concurrency = true, tc.maximum, tc.concurrency
			if _, err = st.SaveProSettings(cfg, "", ""); err != nil {
				t.Fatal(err)
			}
			pay, _, err := st.GPTPaySettings()
			if err != nil {
				t.Fatal(err)
			}
			pay.CardAccountLimit = tc.global
			if _, err = st.SaveGPTPaySettings(pay, ""); err != nil {
				t.Fatal(err)
			}
			if err = st.DeleteGPTPayCard("test-card"); err != nil {
				t.Fatal(err)
			}
			for i, spec := range tc.cards {
				id := fmt.Sprintf("card-%d", i)
				secret := gptpay.CardSecret{Number: fmt.Sprintf("424242424242%04d", i), Month: 12, Year: 2099, CVV: "123"}
				// Deterministic ordering: try the first (possibly full) card first.
				card := gptpay.Card{ID: id, Enabled: !spec.disabled, CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour)}
				if _, err = st.SaveGPTPayCard(card, secret); err != nil {
					t.Fatal(err)
				}
				// Use the same API field as the real card editor, including reset to 0.
				if w := payCall(t, s.saveGPTPayCard, "id", id, map[string]any{"max_accounts": spec.limit}); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				// A card may expire after it was validly configured in the UI.
				if spec.expired {
					secret.Year = 2000
					if _, err = st.SaveGPTPayCard(card, secret); err != nil {
						t.Fatal(err)
					}
				}
				for j := 0; j < spec.opened+spec.pending; j++ {
					email := fmt.Sprintf("existing-%d-%d@example.com", i, j)
					if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
						t.Fatal(err)
					}
					status := "submission_unknown"
					if j < spec.opened {
						status = "success"
					}
					order := gptpay.Order{ID: email, Email: email, CardID: id, Status: status, CreatedAt: time.Now()}
					if err = st.CreateGPTPayOrder(order, gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: secret}}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.duplicate {
					card.ID = "duplicate"
					if _, err = st.SaveGPTPayCard(card, secret); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i := 0; i < 12; i++ {
				email := fmt.Sprintf("new-%02d@example.com", i)
				if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
					t.Fatal(err)
				}
			}
			gate := make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(gate) })
			s.proAutoHooks = &proAutomationHooks{
				Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
					logins.Add(1)
					select {
					case <-gate:
					case <-ctx.Done():
						return ctx.Err()
					}
					_, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}})
					if err != nil {
						return err
					}
					_, err = rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(email, "after"), "refresh_token": "fixture-rt"}})
					return err
				},
				Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
					pushes.Add(1)
					return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
						p.PushStatus = "completed"
						p.PushProvider = "sub2"
						p.Sub2AccountID = 88
					})
				},
				Quota: func(ctx context.Context, email string, _ bool) (model.MailAccountProfile, error) {
					quotas.Add(1)
					return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
						p.QuotaStatus = "completed"
						p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 20}
					})
				},
				Merge: func(context.Context, string) (model.MailAccountProfile, error) {
					t.Error("low quota must not merge")
					return model.MailAccountProfile{}, nil
				},
			}
			before, err := s.proScheduleStatus()
			if err != nil || before.CardCapacity != tc.capacity {
				t.Fatalf("capacity=%d want=%d err=%v", before.CardCapacity, tc.capacity, err)
			}
			// false is the real timed entry, not the force/manual shortcut.
			var ticks sync.WaitGroup
			for i := 0; i < 12; i++ {
				ticks.Add(1)
				go func() { defer ticks.Done(); s.tickProSchedule(false) }()
			}
			ticks.Wait()
			runs, total, err := st.ProScheduleRuns(10, 0)
			if err != nil || total != 1 || len(runs) != 1 || runs[0].Trigger != "timer" || len(runs[0].Tasks) != tc.want {
				t.Fatalf("wrong timer batch: %+v total=%d err=%v", runs, total, err)
			}
			for _, task := range runs[0].Tasks {
				if task.Status != "running" {
					t.Fatalf("task rejected: %+v", task)
				}
			}
			during, err := s.proScheduleStatus()
			if err != nil || during.CardCapacity != tc.capacity-tc.want {
				t.Fatalf("reservations not counted: before=%d during=%d err=%v", tc.capacity, during.CardCapacity, err)
			}
			if purchases.Load() != 0 {
				t.Fatal("payment ran before gate opened")
			}
			// Even when due again, the unfinished batch must block another batch.
			if err = st.SetProScheduleNext(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if tc.want > 0 {
				s.tickProSchedule(false)
				_, n, err := st.ProScheduleRuns(10, 0)
				if err != nil || n != 1 {
					t.Fatal("overlapping timer batch", n, err)
				}
			}
			if tc.lowerDuringLogin {
				if w := payCall(t, s.saveGPTPayCard, "id", "card-0", map[string]any{"max_accounts": 1}); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			release.Do(func() { close(gate) })
			for _, task := range runs[0].Tasks {
				p := waitProAutoFinished(t, s, task.Email)
				if p.ProAuto.Status != "waiting_quota" || p.ProAuto.ProvisionedAt == nil {
					t.Fatalf("provisioning did not finish: %+v", p.ProAuto)
				}
			}
			if int(logins.Load()) != tc.want || int(purchases.Load()) != tc.want || int(pushes.Load()) != tc.want || int(quotas.Load()) != tc.want {
				t.Fatalf("execution counts: login=%d pay=%d push=%d quota=%d want=%d", logins.Load(), purchases.Load(), pushes.Load(), quotas.Load(), tc.want)
			}
			for i, spec := range tc.cards {
				card, _, err := st.GPTPayCard(fmt.Sprintf("card-%d", i))
				if err != nil || card.PendingAccounts != spec.pending {
					t.Fatal("reservation leaked or existing order lost", card, err)
				}
				if !tc.lowerDuringLogin && card.OpenedAccounts+card.PendingAccounts > card.AccountLimit {
					t.Fatal("card overbooked", card)
				}
			}
			if tc.capacity == tc.want {
				// Capacity stays exhausted after successful purchases; no fresh account
				// may be admitted simply because login reservations were released.
				s.tickProSchedule(false)
				runs, _, err = st.ProScheduleRuns(10, 0)
				if err != nil || runs[0].Status != "skipped" || len(runs[0].Tasks) != 0 || int(purchases.Load()) != tc.want {
					t.Fatal("full cards reused by next timer", runs, err)
				}
			}
			t.Logf("timer: capacity=%d admitted=%d; login/payment/push/quota=%d each; no overbooking", tc.capacity, tc.want, tc.want)
		})
	}
}
