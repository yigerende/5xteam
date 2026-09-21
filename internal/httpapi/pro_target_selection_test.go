package httpapi

import (
	"context"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProMergeUnusedAccountUsesCurrentMother(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "manual"
		if automatic {
			name = "automatic"
		}
		t.Run(name, func(t *testing.T) {
			f := newProExecutionFixture(t)
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				// The former invitation failed and was reset before changing mother.
				p.TargetAdminID, p.TargetTeamID, p.TargetSeatType = "deleted-mother", "old-team", "prolite"
				p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "not_started", "pending", "pending", "pending"
				if automatic {
					p.ProAuto = model.ProAutoState{ID: "auto", Status: "running", StartedAt: time.Now(), QuotaUsedThreshold: 100,
						Steps: map[string]string{"oauth": "completed", "push": "completed", "quota": "completed"}}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if automatic {
				err = f.s.runProAutomationTail(context.Background(), f.email)
			} else {
				out := f.mergeAndWait(t)
				if out.Code != 200 {
					t.Fatal(out.Body.String())
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			p, _, err := f.s.store.MailAccountCredential(f.email)
			if err != nil || p.TargetAdminID != f.admin.ID || p.TargetTeamID != f.admin.TeamAccountID || p.TargetSeatType != "default" || p.RemoveStatus != "completed" {
				t.Fatalf("current target not persisted: target=%s team=%s seat=%s remove=%s err=%v", p.TargetAdminID, p.TargetTeamID, p.TargetSeatType, p.RemoveStatus, err)
			}
			for _, step := range []string{"invite", "accept", "transfer", "remove"} {
				if f.calls[step] != 1 {
					t.Fatalf("%s calls = %d", step, f.calls[step])
				}
			}
		})
	}
}

func TestProMergeStartedAccountCannotSwitchMother(t *testing.T) {
	for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
		t.Run(stage, func(t *testing.T) {
			f := newProExecutionFixture(t)
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.TargetAdminID, p.TargetTeamID = "deleted-mother", "old-team"
				setProStage(p, stage, "completed")
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.performProMerge(context.Background(), f.email); err == nil {
				t.Fatal("silently redirected a started workflow to the new mother")
			}
			if len(f.calls) != 0 {
				t.Fatal("sent requests using the wrong mother")
			}
		})
	}
}
