package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCMSNavPurchaseConsentAndRetryAdmission(t *testing.T) {
	var calls atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"taskId":"new-task","status":"reserved"}}`)
	})
	cfg, key, _ := st.GPTPaySettings()
	cfg.Provider = gptpay.CMSNav
	if _, err := st.SaveGPTPaySettings(cfg, key); err != nil {
		t.Fatal(err)
	}
	input := payInput("consent-check-001")
	input["provider"] = gptpay.CMSNav
	if rec := payCall(t, s.createGPTPayOrder, "email", email, input); rec.Code != 409 || !strings.Contains(rec.Body.String(), "条款") || calls.Load() != 0 {
		t.Fatalf("missing consent not blocked: %s", rec.Body.String())
	}
	cfg.TermsAccepted = true
	if _, err := st.SaveGPTPaySettings(cfg, key); err != nil {
		t.Fatal(err)
	}
	old := seedGPTPayPollOrder(t, s, "rejected-old", email, "old-task")
	no := false
	old.Provider = gptpay.CMSNav
	old.Status = "failed"
	old.Remote.Retryable = &no
	old.Remote.RetryHint = "contact support"
	if err := st.UpdateGPTPayOrder(old); err != nil {
		t.Fatal(err)
	}
	if rec := payCall(t, s.createGPTPayOrder, "email", email, input); rec.Code != 409 || !strings.Contains(rec.Body.String(), "不允许重新付款") || calls.Load() != 0 {
		t.Fatalf("denied retry created purchase: %s", rec.Body.String())
	}
	yes := true
	old.Remote.Retryable = &yes
	if err := st.UpdateGPTPayOrder(old); err != nil {
		t.Fatal(err)
	}
	if err := s.validateGPTPayNewPurchase(email, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestCMSNavManualReviewDoesNotPollOrReleaseOccupancy(t *testing.T) {
	var calls atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	o := seedGPTPayPollOrder(t, s, "review-order", email, "review-task")
	o.Provider = gptpay.CMSNav
	o.Status = "manual_review"
	o.Remote.Status = "manual_review"
	o.Remote.CancellationStatus = "pending"
	if err := st.UpdateGPTPayOrder(o); err != nil {
		t.Fatal(err)
	}
	if !o.Active() || o.NeedsStatusPoll() {
		t.Fatal("review must retain payment occupancy without polling")
	}
	due, err := st.GPTPayOrdersDue(time.Now().Add(time.Hour), 20)
	if err != nil || len(due) != 0 {
		t.Fatalf("review remains in poll queue: %+v %v", due, err)
	}
	s.pollGPTPayOrders(context.Background(), time.Now().Add(time.Hour))
	if calls.Load() != 0 {
		t.Fatal("review contacted upstream automatically")
	}
	active, err := st.ActiveGPTPayOrder(email)
	if err != nil || active.ID != o.ID {
		t.Fatal("review reservation released")
	}
}

func TestCMSNavMissingConsentBlocksAutomationBeforeCardAdmission(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected purchase") })
	configureProAutoFixture(t, s)
	cfg, key, _ := st.GPTPaySettings()
	cfg.Provider = gptpay.CMSNav
	if _, err := st.SaveGPTPaySettings(cfg, key); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"provider": gptpay.CMSNav, "plan_code": cfg.PlanCode, "card_id": "test-card"}
	if rec := payCall(t, s.startProAutomation, "email", email, input); rec.Code != 409 || !strings.Contains(rec.Body.String(), "条款") {
		t.Fatalf("unconsented automation admitted: %s", rec.Body.String())
	}
	cards, _, err := st.GPTPayCards(10, 0, true)
	if err != nil || len(cards) != 1 || cards[0].PendingAccounts != 0 {
		t.Fatal("card occupied before consent", err)
	}
}

func TestCMSNavMissingConsentBlocksRestockReservation(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected purchase") })
	configureProAutoFixture(t, s)
	pay, key, _ := st.GPTPaySettings()
	pay.Provider = gptpay.CMSNav
	if _, err := st.SaveGPTPaySettings(pay, key); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID, c.EmptyGroupCount = true, "auto", 1, 1
	if _, err := st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	p, _, _, _ := st.ProSettings()
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		return restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}, nil
	}
	if err := s.tickProRestock(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ProRestockTasks()
	if err != nil || len(tasks) != 0 || !strings.Contains(s.proRestock.decision.Reason, "条款") {
		t.Fatal("restock claimed an account without consent", tasks, err, s.proRestock.decision.Reason)
	}
}
