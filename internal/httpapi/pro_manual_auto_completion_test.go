package httpapi

import (
	"context"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProManualMergeCompletesWaitingAutomation(t *testing.T) {
	for _, mode := range []string{"execute", "edit_status", "remove_failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newProExecutionFixture(t)
			next := time.Now().Add(-time.Second)
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.ProAuto = model.ProAutoState{ID: "waiting-auto", Status: "waiting_quota", Stage: "quota", NextCheckAt: &next,
					StartedAt: time.Now(), Steps: map[string]string{"oauth": "completed", "push": "completed", "quota": "waiting"}}
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "edit_status" {
				for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
					out := f.request(f.s.updateProAccountStage, "PUT", "/stage", `{"stage":"`+stage+`","status":"completed"}`)
					if out.Code != 200 {
						t.Fatal(out.Body.String())
					}
				}
			} else {
				if mode == "remove_failed" {
					f.failStage, f.failCount = "remove", 99
				}
				out := f.mergeAndWait(t)
				if mode != "remove_failed" && out.Code != 200 {
					t.Fatal(out.Body.String())
				}
				if mode == "remove_failed" && out.Code == 200 {
					t.Fatal("expected removal failure")
				}
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if p.ProAuto.Steps["quota"] != "skipped" || p.ProAuto.NextCheckAt != nil || p.ProAuto.Status == "waiting_quota" {
				t.Fatalf("still waiting quota after manual merge: %+v", p.ProAuto)
			}
			if mode == "remove_failed" {
				if p.ProAuto.Status == "completed" || p.ProStages().Steps["merge"] != "failed" {
					t.Fatal("unfinished removal marked complete")
				}
				return
			}
			if p.ProAuto.Status != "completed" || p.ProAuto.Steps["merge"] != "completed" || p.ProStages().Steps["quota"] != "skipped" {
				t.Fatalf("manual completion did not end automatic task: %+v", p.ProAuto)
			}
			due, err := f.s.store.DueProAutomations(time.Now().Add(time.Hour))
			if err != nil || len(due) != 0 {
				t.Fatal("completed account still scheduled", err)
			}
			f.s.proAutoHooks = &proAutomationHooks{
				Push: func(context.Context, string) (model.MailAccountProfile, error) {
					t.Error("pushed completed workflow")
					return p, nil
				},
				Quota: func(context.Context, string, bool) (model.MailAccountProfile, error) {
					t.Error("queried completed workflow")
					return p, nil
				},
				Merge: func(context.Context, string) (model.MailAccountProfile, error) {
					t.Error("repeated completed merge")
					return p, nil
				},
			}
			if err := f.s.runProAutomationTail(context.Background(), f.email); err != nil {
				t.Fatal(err)
			}
		})
	}
}
