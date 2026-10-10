package model

import (
	"testing"
	"time"
)

func TestProManualRecoveryRequiresCurrentPersistedResults(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		change func(*MailAccountProfile)
		want   string
	}{
		{"oauth_and_push", func(p *MailAccountProfile) {}, "waiting_quota"},
		{"oauth_only", func(p *MailAccountProfile) { delete(p.ProManualStages, "push") }, "awaiting_push"},
		{"push_failed", func(p *MailAccountProfile) { p.ProManualStages["push"] = ProStageProgress{Status: "failed"} }, "awaiting_push"},
		{"old_push", func(p *MailAccountProfile) {
			v := p.ProManualStages["push"]
			v.UpdatedAt = now.Add(-4 * time.Minute)
			p.ProManualStages["push"] = v
		}, "awaiting_push"},
		{"cpa_push", func(p *MailAccountProfile) { p.PushProvider = "cpa" }, "awaiting_push"},
		{"failed_push_after_auto_oauth", func(p *MailAccountProfile) {
			p.ProAuto.Steps["oauth"] = "completed"
			p.ProAuto.Stage = "push"
			delete(p.ProManualStages, "oauth")
		}, "waiting_quota"},
		{"manual_only", func(p *MailAccountProfile) { p.ProAuto.ID = "" }, "failed"},
		{"missing_rt", func(p *MailAccountProfile) { p.RefreshTokenPresent = false }, "failed"},
		{"missing_at", func(p *MailAccountProfile) { p.AccessTokenPresent = false }, "failed"},
		{"oauth_failed", func(p *MailAccountProfile) { p.ProManualStages["oauth"] = ProStageProgress{Status: "failed"} }, "failed"},
		{"old_oauth", func(p *MailAccountProfile) { old := now.Add(-time.Hour); p.OAuthAuthorizedAt = &old }, "failed"},
		{"old_manual_attempt", func(p *MailAccountProfile) {
			v := p.ProManualStages["oauth"]
			v.StartedAt = now.Add(-time.Hour)
			p.ProManualStages["oauth"] = v
		}, "failed"},
		{"unconfirmed_payment", func(p *MailAccountProfile) { p.ProAuto.Steps["recharge"] = "unknown" }, "failed"},
		{"stopped", func(p *MailAccountProfile) { p.ProAuto.Status = "stopped" }, "stopped"},
		{"running", func(p *MailAccountProfile) { p.ProAuto.Status = "running" }, "running"},
		{"completed", func(p *MailAccountProfile) { p.ProAuto.Status = "completed" }, "completed"},
		{"paused_import", func(p *MailAccountProfile) { p.ProMigration = &ProMigrationState{Paused: true} }, "failed"},
		{"already_merged", func(p *MailAccountProfile) { p.SpaceMergedOnce = true }, "failed"},
		{"dead", func(p *MailAccountProfile) { p.ChatGPTStatus = "dead" }, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authorized := now.Add(-3 * time.Minute)
			p := MailAccountProfile{ManagementScope: "pro", OAuthStatus: "completed", AccessTokenPresent: true, RefreshTokenPresent: true, OAuthAuthorizedAt: &authorized, PushStatus: "completed", PushProvider: "sub2", Sub2AccountID: 42,
				ProAuto:         ProAutoState{ID: "auto", OrderID: "keep-order", Status: "failed", Stage: "oauth", Error: "old failure", StartedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute), QuotaUsedThreshold: 85, QuotaIntervalSeconds: 45, Steps: map[string]string{"login": "completed", "recharge": "completed", "oauth": "failed"}},
				ProManualStages: map[string]ProStageProgress{"oauth": {Status: "completed", StartedAt: now.Add(-4 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute)}, "push": {Status: "completed", StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-30 * time.Second)}}}
			tc.change(&p)
			changed := p.ReconcileProAutoManual(now)
			if p.ProAuto.Status != tc.want {
				t.Fatalf("got %+v, want %s", p.ProAuto, tc.want)
			}
			if changed {
				if p.ProAuto.Error != "" || p.ProAuto.Steps["oauth"] != "completed" || p.ProAuto.OrderID != "keep-order" || p.ProAuto.QuotaUsedThreshold != 85 || p.ProAuto.QuotaIntervalSeconds != 45 {
					t.Fatal("lost checkpoint/config")
				}
				if (p.ProAuto.NextCheckAt != nil) != (tc.want == "waiting_quota") {
					t.Fatal("wrong scheduling")
				}
				if p.ReconcileProAutoManual(now.Add(time.Minute)) {
					t.Fatal("repair repeated")
				}
			}
		})
	}
}
