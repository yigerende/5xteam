package httpapi

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestMailManagementDeleteDoesNotWaitForOAIOrFinalStep(t *testing.T) {
	for _, state := range []string{"waiting", "running"} {
		t.Run(state, func(t *testing.T) {
			f := mailActionFixture(t, "merge_v2")
			if _, err := f.s.store.EnableMailProActions(f.email, "merge_v2"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				next := time.Now().Add(time.Hour)
				p.ProWorkflowRunning = state == "running"
				p.ProOAIConfirmations = map[string]*model.ProOAIConfirmation{
					"merge_v2": {Status: state, NextAttemptAt: &next},
				}
			}); err != nil {
				t.Fatal(err)
			}
			// Single and batch mailbox deletion use this same HTTP handler.
			w := f.request(f.s.deleteMailAccount, "DELETE", "/mail-account", "")
			if w.Code != 200 {
				t.Fatalf("delete returned %d: %s", w.Code, w.Body.String())
			}
			if _, _, err := f.s.store.MailAccountCredential(f.email); err == nil {
				t.Fatal("mailbox credentials remain")
			}
			work, err := f.s.store.ProOAIWork(time.Now().Add(2*time.Hour), false)
			if err != nil || len(work) != 0 {
				t.Fatal("deleted mailbox still scheduled for OAI", work, err)
			}
			if _, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.ProWorkflowRunning = false
			}); err == nil {
				t.Fatal("late workflow update recreated the deleted account")
			}
			if w := f.request(f.s.deleteMailAccount, "DELETE", "/mail-account", ""); w.Code != 404 {
				t.Fatal("missing mailbox should return 404", w.Code)
			}
		})
	}
}
