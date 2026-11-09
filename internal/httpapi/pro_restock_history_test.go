package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestRestockHistoryCandidatesPreserveUncertainAndManualWork(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier request") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	cases := []struct {
		name, payment                             string
		exiting, auto, manual, linked, seen, want bool
	}{
		{name: "settled", payment: "success", exiting: true, auto: true},
		{name: "unknown", payment: "submission_unknown", exiting: true, auto: true, want: true},
		{name: "review", payment: "manual_review", exiting: true, auto: true, want: true},
		{name: "missing", exiting: true, auto: true, want: true},
		{name: "idle"},
		{name: "manual_running", manual: true, want: true},
		{name: "manual_payment", payment: "submission_unknown", want: true},
		{name: "new_automation", auto: true, want: true},
		{name: "known_task", auto: true, linked: true},
		{name: "seen_task", auto: true, seen: true},
	}
	seen := map[string]bool{}
	for i, tc := range cases {
		email := tc.name + "@example.test"
		_, err := st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{})
		if err != nil {
			t.Fatal(err)
		}
		if tc.payment != "" {
			snapshot := gptpay.Snapshot{}
			snapshot.Input.Number = fmt.Sprintf("42424242424%05d", i)
			if err = st.CreateGPTPayOrder(gptpay.Order{ID: tc.name, Email: email, Status: tc.payment, CreatedAt: time.Now()}, snapshot); err != nil {
				t.Fatal(err)
			}
		}
		_, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if tc.auto {
				p.ProAuto = model.ProAutoState{ID: tc.name, OrderID: tc.name, Status: "running", Stage: "login"}
			}
			if tc.exiting {
				p.PushProvider, p.PushStatus, p.Sub2AccountID = "sub2", "completed", 88
				p.ProAuto.Stage = "merge"
				p.ProAuto.Steps = map[string]string{"push": "completed"}
			}
			if tc.manual {
				p.ProManualStages = map[string]model.ProStageProgress{"login": {Status: "running"}}
			}
			if tc.linked {
				p.ProRestockTaskID = "tracked"
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		seen[email] = tc.seen
	}
	profiles, err := s.restockExternalCandidates(context.Background(), seen)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range profiles {
		got[p.Email] = true
	}
	for _, tc := range cases {
		if got[tc.name+"@example.test"] != tc.want {
			t.Errorf("%s candidate=%v want=%v", tc.name, got[tc.name+"@example.test"], tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.restockExternalCandidates(ctx, seen); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled scan continued", err)
	}
}
