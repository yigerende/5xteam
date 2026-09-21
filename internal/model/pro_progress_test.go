package model

import (
	"testing"
	"time"
)

func TestProQuotaDisplayKeepsAutomationMilestone(t *testing.T) {
	for _, tc := range []struct {
		name, autoStep, manualStatus, manualError, wantStep, wantError string
	}{
		{"waiting", "waiting", "", "", "waiting", ""},
		{"query_running", "waiting", "running", "", "running", ""},
		{"query_succeeded_below_threshold", "waiting", "completed", "", "waiting", ""},
		{"query_failed", "waiting", "failed", "quota unavailable", "failed", "quota unavailable"},
		{"threshold_already_reached", "completed", "completed", "", "completed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			p := MailAccountProfile{ProAuto: ProAutoState{ID: "auto", Status: "waiting_quota", Stage: "quota", StartedAt: now,
				Steps: map[string]string{"quota": tc.autoStep}, Error: "额度尚未达到阈值，等待下次检测"}}
			if tc.manualStatus != "" {
				p.ProManualStages = map[string]ProStageProgress{"quota": {StartedAt: now.Add(time.Second), Status: tc.manualStatus, Error: tc.manualError}}
			}
			got := p.ProStages()
			if got.Steps["quota"] != tc.wantStep || got.Errors["quota"] != tc.wantError {
				t.Fatalf("display = %+v; want %s / %s", got, tc.wantStep, tc.wantError)
			}
			if p.ProAuto.Steps["quota"] != tc.autoStep {
				t.Fatal("display changed the automation checkpoint")
			}
		})
	}
}

func TestProQuotaDisplayPreservesRealFailure(t *testing.T) {
	p := MailAccountProfile{ProAuto: ProAutoState{ID: "auto", Status: "waiting_quota", Stage: "quota", Error: "额度检测暂未成功，等待下次重试：HTTP 503", Steps: map[string]string{"quota": "waiting"}}}
	if p.ProStages().Errors["quota"] != p.ProAuto.Error {
		t.Fatal("real quota failure was hidden")
	}
	p.ProAuto = ProAutoState{}
	p.QuotaStatus = "completed"
	if p.ProStages().Steps["quota"] != "completed" {
		t.Fatal("manual-only quota query must still show completion")
	}
}
