package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRestockExternalInflightTracksPaymentOutcome(t *testing.T) {
	for _, tc := range []struct {
		status, payment      string
		active, linked, want bool
	}{
		{"failed", "", false, false, false},
		{"interrupted", "", false, false, false},
		{"failed", "failed", false, true, false},
		{"failed", "historical_risk", false, true, false},
		{"failed", "risk_review", false, true, true},
		{"failed", "submission_unknown", false, true, true},
		{"stopped", "submission_unknown", false, true, true},
		{"stopped", "success", false, true, true},
		{"failed", "submission_unknown", false, false, true},
		{"running", "", true, false, true},
	} {
		t.Run(tc.status+"/"+tc.payment, func(t *testing.T) {
			s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier call") })
			s.proAutoCancel()
			s.proAutoWG.Wait()
			configureProAutoFixture(t, s)
			p, _, _, err := st.ProSettings()
			if err != nil {
				t.Fatal(err)
			}
			if tc.payment != "" {
				_, secret, err := st.GPTPayCard("test-card")
				if err != nil {
					t.Fatal(err)
				}
				order := gptpay.Order{ID: "pro-auto-external", Email: email, CardID: "test-card", Status: tc.payment, CreatedAt: time.Now()}
				if tc.payment == "historical_risk" || tc.payment == "risk_review" {
					order.Status, order.Error = "failed", "OpenAI 支付风控拦截"
				}
				if err = st.CreateGPTPayOrder(order, gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: secret}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = st.UpdateProAccount(email, func(v *model.MailAccountProfile) {
				v.ProAuto = model.ProAutoState{ID: "external", Status: tc.status, StartedAt: time.Now(), RestockBinding: restockBinding(p)}
				v.ProAuto.PaymentReviewRequired = tc.payment == "risk_review"
				if tc.linked {
					v.ProAuto.OrderID = "pro-auto-external"
				}
			}); err != nil {
				t.Fatal(err)
			}
			if tc.active {
				s.proAutoJobs = map[string]context.CancelFunc{email: func() {}}
				defer delete(s.proAutoJobs, email)
			}
			tasks, _, err := s.restockTasks(restock.Snapshot{Binding: restockBinding(p), GroupID: 1}, p, restock.Config{GroupID: 1, LeadMinutes: 5, LeadSamples: 2})
			if err != nil {
				t.Fatal(err)
			}
			if (len(tasks) == 1) != tc.want {
				t.Fatalf("active inflight=%d want=%v", len(tasks), tc.want)
			}
			if tc.payment == "submission_unknown" && len(tasks) > 0 && tasks[0].Predictable() {
				t.Fatal("unknown payment promised future capacity")
			}
		})
	}
}

func TestRestockSettledRiskFailureReleasesOnlyFinishedWork(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		for _, settled := range []bool{false, true} {
			t.Run(fmt.Sprintf("tracked=%v/settled=%v", tracked, settled), func(t *testing.T) {
				s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("no supplier mutation allowed") })
				s.proAutoCancel()
				s.proAutoWG.Wait()
				configureProAutoFixture(t, s)
				cfg, _, _, _ := st.ProSettings()
				var task restock.Task
				if tracked {
					c := restock.DefaultConfig()
					c.Enabled, c.Mode, c.GroupID = true, "auto", 1
					c, err := st.SaveProRestockConfig(c)
					if err != nil {
						t.Fatal(err)
					}
					reserved, err := st.ReserveProRestock(restock.Decision{ID: "risk-batch", At: time.Now(), Version: c.Version, Suggested: 1}, "", 0)
					if err != nil || len(reserved) != 1 {
						t.Fatal(reserved, err)
					}
					task = reserved[0]
				}
				o := gptpay.Order{ID: "pro-auto-risk", Email: email, CardID: "test-card", Status: "failed", Error: "支付风控拦截", CreatedAt: time.Now(), Remote: gptpay.RemoteOrder{ID: "remote-risk", Status: "failed"}}
				if settled {
					o.Remote.SettlementStatus = "settled"
				}
				if err := st.CreateGPTPayOrder(o, gptpay.Snapshot{}); err != nil {
					t.Fatal(err)
				}
				if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.ProAuto = model.ProAutoState{ID: "risk", Status: "failed", Stage: "recharge", OrderID: o.ID, CardID: "test-card", PaymentReviewRequired: true, RestockBinding: restockBinding(cfg)}
					if tracked {
						p.ProRestockTaskID = task.ID
					}
				}); err != nil {
					t.Fatal(err)
				}
				if tracked {
					task.Status, task.OrderID, task.Binding = "attention", o.ID, restockBinding(cfg)
					if err := st.SaveProRestockTask(task); err != nil {
						t.Fatal(err)
					}
				}
				for n := 0; n < 2; n++ {
					tasks, _, err := s.restockTasks(restock.Snapshot{Binding: restockBinding(cfg), GroupID: 1}, cfg, restock.Config{GroupID: 1, LeadMinutes: 5, LeadSamples: 2})
					if err != nil {
						t.Fatal(err)
					}
					active := 0
					for _, task := range tasks {
						if task.Active() {
							active++
						}
						if settled && task.Predictable() {
							t.Fatal("failed order counted as supply")
						}
					}
					if active != 0 {
						t.Fatalf("tasks=%+v settled=%v", tasks, settled)
					}
					if !settled && (len(tasks) != 1 || !tasks[0].AutoIsolated) {
						t.Fatal("uncertain order did not isolate durably", tasks)
					}
					if settled && (len(tasks) != 1 || !tasks[0].PaymentRiskPending) {
						t.Fatal("failed workflow lost its durable one-time replacement", tasks)
					}
				}
				p, _, _ := st.MailAccountCredential(email)
				if !p.ProAuto.PaymentReviewRequired || p.ProAuto.OrderID != o.ID || p.IsProDead() {
					t.Fatal("review or original order lost")
				}
			})
		}
	}
}
