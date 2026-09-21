package store

import (
	"testing"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProAutomationSettingsAtomicAndPreservePush(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pro := model.DefaultProSettings()
	pro.Sub2.URL, pro.Sub2.Email, pro.Sub2.GroupIDs = "https://sub.invalid", "test@example.com", []int64{7, 8}
	if _, err = s.SaveProSettings(pro, "sub-secret", "cpa-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGPTPaySettings(gptpay.Settings{PlanCode: "pro5"}, "pay-secret"); err != nil {
		t.Fatal(err)
	}
	input := model.DefaultProSettings()
	input.QuotaEnabled, input.AutoMergeEnabled, input.TargetAdminID = true, true, "mother-8"
	input.QuotaUsedThreshold, input.QuotaCheckIntervalSeconds = 86, 45
	input.Concurrency, input.RetryCount = 4, 3
	_, pay, err := s.SaveProAutomationSettings(input, gptpay.Settings{PlanCode: "pro20"}, "")
	if err != nil || !pay.KeyPresent {
		t.Fatalf("save failed: %v", err)
	}
	saved, password, cpa, err := s.ProSettings()
	if err != nil || password != "sub-secret" || cpa != "cpa-secret" || saved.Sub2.URL != pro.Sub2.URL || len(saved.Sub2.GroupIDs) != 2 {
		t.Fatal("push config was overwritten")
	}
	if saved.QuotaUsedThreshold != 86 || saved.QuotaCheckIntervalSeconds != 45 || saved.TargetAdminID != "mother-8" || saved.NextQuotaSweepAt == nil {
		t.Fatal("automation config not saved")
	}
	_, key, err := s.GPTPaySettings()
	if err != nil || key != "pay-secret" {
		t.Fatal("blank key cleared credential")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_pay_update BEFORE UPDATE ON gptpay_settings BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	input.QuotaUsedThreshold = 60
	if _, _, err = s.SaveProAutomationSettings(input, gptpay.Settings{PlanCode: "pro5"}, "changed-key"); err == nil {
		t.Fatal("expected save failure")
	}
	saved, _, _, _ = s.ProSettings()
	pay, key, _ = s.GPTPaySettings()
	if saved.QuotaUsedThreshold != 86 || pay.PlanCode != "pro20" || key != "pay-secret" {
		t.Fatal("partially saved configuration after failed transaction")
	}
}

func TestProAutomationQuotaMigration(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "full_workflow_schedule", true: "existing_active_schedule"}[active], func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			pro := model.DefaultProSettings()
			pro.QuotaEnabled, pro.QuotaCheckIntervalSeconds, pro.TargetAdminID = active, 75, "keep-target"
			if _, err = s.SaveProSettings(pro, "keep-password", ""); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE pro_settings SET profile=json_remove(profile,'$.quota_used_threshold')`); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO gptpay_settings(id,profile,encrypted_key) VALUES(1,?,'')`, `{"plan_code":"pro20","quota_used_threshold":88,"quota_interval_seconds":35}`); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got, password, _, err := s.ProSettings()
			expectedInterval := 35
			if active {
				expectedInterval = 75
			}
			if err != nil || got.QuotaUsedThreshold != 88 || got.QuotaCheckIntervalSeconds != expectedInterval || got.TargetAdminID != "keep-target" || password != "keep-password" {
				t.Fatal("legacy settings lost")
			}
			var count int
			if err = s.db.QueryRow(`SELECT count(*) FROM gptpay_settings WHERE json_type(profile,'$.quota_interval_seconds') IS NOT NULL OR json_type(profile,'$.quota_used_threshold') IS NOT NULL`).Scan(&count); err != nil || count != 0 {
				t.Fatal("duplicate settings retained")
			}
			got.QuotaUsedThreshold, got.QuotaCheckIntervalSeconds = 93, 60
			if _, err = s.SaveProSettings(got, "", ""); err != nil {
				t.Fatal(err)
			}
			if err = s.migrateProQuotaSettings(); err != nil {
				t.Fatal(err)
			}
			got, _, _, _ = s.ProSettings()
			if got.QuotaUsedThreshold != 93 || got.QuotaCheckIntervalSeconds != 60 {
				t.Fatal("migration overwrote new values")
			}
		})
	}
}
