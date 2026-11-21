package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/nexovcc"
	"chatgpt-space-merge/internal/restock"
)

func TestProRestockBatchDrainsBeforeNewDemand(t *testing.T) {
	for _, tc := range []struct {
		size, concurrency int
		nexovcc           bool
		global            bool
	}{{1, 1, false, false}, {2, 1, false, false}, {3, 2, false, false}, {2, 1, true, false}, {3, 2, true, false}, {1, 1, true, true}, {2, 1, true, true}, {3, 2, true, true}} {
		t.Run(fmt.Sprintf("batch_%d_workers_%d_nexovcc_%v_global_%v", tc.size, tc.concurrency, tc.nexovcc, tc.global), func(t *testing.T) {
			var purchases, pushes atomic.Int32
			s, st, firstEmail := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"code":0,"data":{"id":"batch-order-%d","status":"success"}}`, purchases.Add(1))
			})
			s.proAutoCancel()
			s.proAutoWG.Wait()
			configureProAutoFixture(t, s)
			p, password, _, _ := st.ProSettings()
			p.Concurrency = tc.concurrency
			if tc.global {
				p.CardProvider = "nexovcc"
			}
			if _, err := st.SaveProSettings(p, password, ""); err != nil {
				t.Fatal(err)
			}
			if tc.nexovcc {
				enableNexovccPaymentFixture(t, st)
				s.nexovcc.transport = func(context.Context, nexovcc.Request) (nexovcc.Response, error) {
					return nexovcc.Response{}, errors.New("no issuer network expected in batch fixture")
				}
				for i := 0; i < tc.size; i++ {
					_, err := st.SaveNexovccCard(nexovcc.Card{Serial: fmt.Sprintf("batch-card-%d", i), Status: "1", Balance: "10.00", CreatedAt: time.Now().Add(time.Duration(i-10) * time.Hour)}, &nexovcc.PrivateCard{Number: []string{"5555555555554444", "4000000000000002", "4242424242424242"}[i], CVV: "123", Month: 12, Year: 2099})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			permits := map[string]chan struct{}{firstEmail: make(chan struct{})}
			for i := 0; i < 2*tc.size; i++ {
				email := fmt.Sprintf("batch-%d@example.com", i)
				permits[email] = make(chan struct{})
				if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email}); err != nil {
					t.Fatal(err)
				}
			}
			started := make(chan string, tc.size)
			s.proAutoHooks = &proAutomationHooks{
				Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
					started <- email
					select {
					case <-permits[email]:
					case <-ctx.Done():
						return ctx.Err()
					}
					st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = "198.51.100.8" })
					if _, err := rpc("pro_recharge", map[string]any{"exit_ip": "198.51.100.8", "session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}}); err != nil {
						return err
					}
					_, err := rpc("pro_tokens", map[string]any{"exit_ip": "198.51.100.8", "tokens": map[string]any{"access_token": proAutoToken(email, "new"), "refresh_token": "new-rt"}})
					return err
				},
				Push: func(_ context.Context, email string) (model.MailAccountProfile, error) {
					id := int64(100 + pushes.Add(1))
					return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
						p.PushStatus, p.PushProvider, p.Sub2AccountID = "completed", "sub2", id
					})
				},
				Quota: func(context.Context, string, bool) (model.MailAccountProfile, error) {
					return model.MailAccountProfile{}, errors.New("test quota sampling pending")
				},
			}
			c := restock.DefaultConfig()
			c.Enabled, c.Mode, c.GroupID = true, "auto", 1
			c.CapacityPercent, c.Confirmations = 100, 3
			c.MinBatch, c.MaxBatch, c.MaxInflight, c.AllowSmall = tc.size, tc.size, 10, false
			c.CooldownSeconds, c.EmergencyCooldownSeconds = 1, 1
			if tc.nexovcc && !tc.global {
				now := time.Now().In(time.FixedZone("CST", 8*3600))
				c.NoRestockEnabled = true
				c.NoRestockWindows = []restock.DailyWindow{{Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), AllowedProvider: "nexovcc"}}
			}
			if _, err := st.SaveProRestockConfig(c); err != nil {
				t.Fatal(err)
			}
			pay, _, _ := st.GPTPaySettings()
			load := 9
			var frozen, collected time.Time
			var monitorErr error
			s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
				at := time.Now()
				if !frozen.IsZero() {
					at = frozen
				}
				collected = at
				snap := restock.Snapshot{At: at, CollectionStartedAt: at, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: load}}}
				for _, a := range st.MailAccountsByManagementScope("pro") {
					if a.PushStatus == "completed" {
						snap.Accounts = append(snap.Accounts, restock.Account{ID: a.Sub2AccountID, Ready: true, Capacity: p.Sub2.AccountConcurrency})
					}
				}
				return snap, monitorErr // Money samples deliberately remain missing.
			}
			tick := func() {
				t.Helper()
				if err := s.tickProRestock(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				// Production schedules the next tick independently. This test
				// advances one completed preparation at a time, without waiting
				// for the deliberately blocked account workflow.
				deadline := time.Now().Add(5 * time.Second)
				for {
					s.proAutoMu.Lock()
					preparing := len(s.proRestockStartJobs) > 0
					s.proAutoMu.Unlock()
					if !preparing {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("preparation did not settle")
					}
					time.Sleep(time.Millisecond)
				}
			}
			busy := func(n int) {
				s.proMergeMu.Lock()
				s.proAutoRunning = n
				s.proMergeMu.Unlock()
			}
			busy(tc.concurrency)
			for range c.Confirmations {
				tick()
			}
			original, err := st.ProRestockTasks()
			if err != nil || len(original) != tc.size {
				t.Fatalf("initial batch: %+v %v", original, err)
			}
			if tc.nexovcc {
				for _, task := range original {
					if task.CardProvider != "nexovcc" {
						t.Fatal("batch pinned wrong issuer", task)
					}
				}
			}
			busy(0)
			for i := 0; i < tc.size; i++ {
				tick()
				var email string
				select {
				case email = <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("ordinary batch left a queued member stranded")
				}
				for range 3 {
					tick()
					if s.proRestock.decision.Started != 0 || s.proRestock.decision.BatchWait == nil || s.proRestock.decision.Confirmations != 0 {
						t.Fatal("active batch allowed a new batch or accumulated pressure", s.proRestock.decision)
					}
				}
				if i == tc.size-1 {
					frozen = collected // Final push must not use a pre-push sample.
				}
				close(permits[email])
				profile := waitProAutoFinished(t, s, email)
				if profile.PushStatus != "completed" || profile.ProAuto.Status != "waiting_quota" {
					t.Fatalf("full workflow did not reach push: %+v", profile.ProAuto)
				}
				load = 0 // Same batch must finish despite demand falling to zero.
			}
			tick()
			if s.proRestock.decision.BatchWait == nil || s.proRestock.decision.BatchWait.Remaining == 0 {
				t.Fatal("push completion accepted an old monitoring sample")
			}
			frozen = time.Time{}
			tick() // All members become individually ready.
			wait := s.proRestock.decision.BatchWait
			if wait == nil || !wait.Refresh || s.proRestock.decision.Started != 0 {
				t.Fatal("last readiness observation bypassed final refresh", s.proRestock.decision)
			}
			frozen = collected
			load = 100
			tick()
			if s.proRestock.decision.BatchWait == nil || s.proRestock.decision.Confirmations != 0 {
				t.Fatal("repeated final snapshot released the batch")
			}
			monitorErr = errors.New("monitor unavailable")
			tick()
			if s.proRestock.decision.Started != 0 || s.proRestock.decision.BatchWait == nil {
				t.Fatal("failed final refresh admitted a new batch")
			}
			monitorErr, frozen = nil, time.Time{}
			load = (10 + tc.size*p.Sub2.AccountConcurrency) * 9 / 10 // Soft pressure after the completed batch.
			busy(tc.concurrency)                                     // Inspect next reservation without starting payment.
			time.Sleep(1100 * time.Millisecond)                      // Expire the independently enforced cooldown.
			for i := 1; i <= c.Confirmations; i++ {
				tick()
				if s.proRestock.decision.Confirmations != i || (i < c.Confirmations && s.proRestock.decision.Started != 0) {
					t.Fatal("next batch reused old confirmations", s.proRestock.decision)
				}
			}
			if s.proRestock.decision.Started != tc.size || int(purchases.Load()) != tc.size || int(pushes.Load()) != tc.size {
				t.Fatalf("next batch or payment counts wrong: decision=%+v purchases=%d pushes=%d", s.proRestock.decision, purchases.Load(), pushes.Load())
			}
		})
	}
}
