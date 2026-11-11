package httpapi

import (
	"net/http"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestRestockDeliveryReconcilesRealReplacementChain(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream request") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	p, _, _, _ := st.ProSettings()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID, c.UnitCost = true, "auto", 1, 25
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	snap := restock.Snapshot{ID: "delivered", At: now, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p)}
	tasks, err := st.ReserveProRestock(restock.Decision{ID: "delivery", At: now, Version: c.Version, Snapshot: snap, Suggested: 1}, "target", 0)
	if err != nil || len(tasks) != 1 || tasks[0].Email != email {
		t.Fatal(tasks, err)
	}
	if err = st.CreateGPTPayOrder(gptpay.Order{ID: "pro-auto-original", Email: email, CardID: "original-card", Status: "manual_review", SubmissionStarted: true, CreatedAt: now}, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateProAccount(email, func(v *model.MailAccountProfile) {
		v.ProAuto = model.ProAutoState{ID: "original", OrderID: "pro-auto-original", Status: "failed", Stage: "recharge", Replacement: &model.ProRechargeReplacement{RootEmail: email, Limit: 1}}
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := tasks[0]
	parent.Status, parent.OrderID, parent.CardID = "unknown", "pro-auto-original", "original-card"
	if err = st.SaveProRestockTask(parent); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: "replacement@example.com", ManagementScope: "pro"}, model.MailAccountCredentials{Email: "replacement@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err = st.QueueProRechargeReplacement(email, "original"); err != nil {
		t.Fatal(err)
	}
	childProfile, err := st.ClaimProRechargeReplacement(email)
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.ProRestockTask(childProfile.ProRestockTaskID)
	if err != nil {
		t.Fatal(err)
	}
	ready := time.Now()
	child.Status, child.OrderID, child.AccountID, child.ReadyAt = "ready", "pro-auto-child", 3965, &ready
	if err = st.SaveProRestockTask(child); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateGPTPayOrder(gptpay.Order{ID: child.OrderID, Email: child.Email, Status: "success", CreatedAt: now}, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateProAccount(child.Email, func(v *model.MailAccountProfile) {
		v.ProAuto.ID, v.ProAuto.OrderID, v.ProAuto.Status, v.ProAuto.Stage = "child", child.OrderID, "waiting_quota", "quota"
		v.PushStatus, v.PushProvider, v.Sub2AccountID = "completed", "sub2", child.AccountID
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, _, err := s.restockTasks(snap, p, c)
		if err != nil || len(got) != 2 {
			t.Fatal(got, err)
		}
		if got[0].Active() || !got[0].ReplacementDelivered() || got[0].Status != "unknown" || got[0].ReadyAt != nil || got[0].FulfilledByEmail != child.Email {
			t.Fatal(got[0])
		}
		order, _, err := st.GPTPayOrder(parent.OrderID)
		if err != nil || !order.Active() || order.Status != "manual_review" || order.CardID != "original-card" {
			t.Fatal("original financial hold changed", order, err)
		}
	}
}
