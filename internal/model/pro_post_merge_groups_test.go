package model

import (
	"testing"
	"time"
)

func TestProPostMergeGroupsDisplayUsesLatestResult(t *testing.T) {
	for _, status := range []string{"pending", "running", "failed", "completed"} {
		t.Run(status, func(t *testing.T) {
			p := MailAccountProfile{
				InviteStatus: "completed", AcceptStatus: "completed", TransferStatus: "completed", RemoveStatus: "completed",
				ProAuto:            ProAutoState{ID: "auto", Steps: map[string]string{"merge": "completed"}},
				ProManualStages:    map[string]ProStageProgress{"merge": {Status: "failed", Error: "old error", StartedAt: time.Now()}},
				ProPostMergeGroups: &ProPostMergeGroups{Status: status},
			}
			if status == "failed" {
				p.ProPostMergeGroups.Error = "new grouping error"
			}
			display := p.ProStages()
			if display.Steps["merge"] != status || display.Errors["merge"] != p.ProPostMergeGroups.Error {
				t.Fatalf("stale manual state hid latest grouping result: %+v", display)
			}
		})
	}
}
