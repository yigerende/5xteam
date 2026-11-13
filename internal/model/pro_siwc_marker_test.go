package model

import (
	"testing"
	"time"
)

func TestSIWCMarkerFinalStepsMatchDisplay(t *testing.T) {
	now := time.Now().UTC()
	for _, stage := range []string{"merge", "merge_v2", "deactivate", "merchant"} {
		for _, status := range []string{"completed", "waiting", "running", "failed", "skipped", "unknown", "pending", "interrupted"} {
			t.Run(stage+"/"+status, func(t *testing.T) {
				p := MailAccountProfile{ID: "account", Email: "test@example.com", ProAuto: ProAutoState{ID: "flow", OrderID: "order", Stage: stage, Steps: map[string]string{stage: status}}}
				m := p.SIWCWorkflowMarker("source", 3, now, "merge")
				if m.Completed != (status == "completed") || m.Stage != stage || m.FlowID != "flow" || m.OrderID != "order" {
					t.Fatalf("wrong marker: %+v", m)
				}
				// OAI confirmation, refunds, payment and cleanup are deliberately absent.
				p.ProStageOverrides = map[string]ProStageOverride{stage: {Status: "completed", UpdatedAt: now}}
				m = p.SIWCWorkflowMarker("source", 4, now, "merge")
				if !m.Completed || !m.Manual {
					t.Fatalf("manual completion not projected: %+v", m)
				}
				p.ProAuto.StartedAt = now.Add(time.Second)
				if p.SIWCWorkflowMarker("source", 5, now, "merge").Manual {
					t.Fatal("old override leaked into new flow")
				}
			})
		}
	}
}

func TestSIWCMarkerPersistedCompletionAndMode(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		p     MailAccountProfile
		stage string
		done  bool
	}{
		{"merge", MailAccountProfile{InviteStatus: "completed", AcceptStatus: "completed", TransferStatus: "completed", RemoveStatus: "completed"}, "merge", true},
		{"merge still removing", MailAccountProfile{TransferStatus: "completed", RemoveStatus: "running"}, "merge", false},
		{"merchant complete without retirement", MailAccountProfile{ProMerchant: &ProMerchant{RequestID: "id", CompletedAt: &now}}, "merchant", true},
		{"merchant accepted only", MailAccountProfile{ProMerchant: &ProMerchant{RequestID: "id", AcceptedAt: &now}}, "merchant", false},
		{"delete confirmed", MailAccountProfile{ProDeactivation: &ProDeactivation{ConfirmedAt: &now}}, "deactivate", true},
		{"delete awaiting confirmation", MailAccountProfile{ProDeactivation: &ProDeactivation{Status: "completed", SubmittedAt: &now}}, "deactivate", false},
		{"merge2", MailAccountProfile{ProMergeV2: &ProMergeV2{Status: "completed"}}, "merge_v2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.p.SIWCWorkflowMarker("source", 1, now, "deactivate")
			if m.Stage != tc.stage || m.Completed != tc.done {
				t.Fatalf("got %+v", m)
			}
		})
	}
}
