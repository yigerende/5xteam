package httpapi

import (
	"net/http"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProActivationDeadlineWaitsForPush(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream request") })
	s.proAutoCancel()
	cfg := model.DefaultProSettings()
	cfg.ForceMergeEnabled, cfg.ForceMergeMinutes = true, 60
	if _, err := st.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	paid := time.Now().Add(-70 * time.Minute)
	order := gptpay.Order{ID: "activation-deadline", Email: email, Status: "success", SucceededAt: &paid, CreatedAt: paid}
	if err := st.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"", "running", "failed", "completed"} {
		p, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.PushStatus = status })
		if err != nil {
			t.Fatal(err)
		}
		deadline := p.ProPushDeadline()
		if deadline == nil || !deadline.Equal(paid.Add(time.Hour)) {
			t.Fatal("deadline did not start at activation")
		}
		if got := s.forcedMergeReason(p, cfg, time.Now()); (got == "push_timeout") != (status == "completed") {
			t.Fatalf("push=%q reason=%q", status, got)
		}
		rows, err := s.proAccountListWithCards([]model.MailAccountProfile{p})
		if err != nil || len(rows) != 1 || rows[0].PushElapsedMinutes < 70 || rows[0].ProPushStartedSource != "activation" {
			t.Fatal("list clock missing before push", err)
		}
	}
}
