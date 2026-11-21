package store

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func autoReissueMerchantFixture(t *testing.T) (*Store, model.MailAccountProfile, refundmerchant.Job) {
	t.Helper()
	s := transferStore(t)
	email := "auto-reissue@example.com"
	transferSource(t, s, email)
	cfg := model.DefaultProSettings()
	cfg.MerchantProvider = "starbridge"
	if _, _, err := s.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, "", "", "bridge-key"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PrepareProAutomaticMerchant(email); err != nil {
		t.Fatal(err)
	}
	p, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "flow", OrderID: "paid-order", RetryAttempts: 2}
		p.ProWorkflowRunning = true
		p.ProMerchant.JobID, p.ProMerchant.Status = "first-job", "waiting"
		now := time.Now()
		p.ProMerchant.AcceptedAt, p.ProMerchant.RetiredAt = &now, &now
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, refundmerchant.Job{ID: "first-job", Email: email, Status: "failed"}
}

func TestStarBridgeMerchantAutoReissueConcurrentAndRestart(t *testing.T) {
	s, original, failed := autoReissueMerchantFixture(t)
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AutoReissueProMerchant(original.Email, original.ProMerchant.RequestID, 0, failed); err == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("rotated %d times", won.Load())
	}
	p, _, err := s.MailAccountCredential(original.Email)
	if err != nil || p.ProMerchant.AutoReissues != 1 || p.ProAuto.RetryAttempts != 2 || p.ProMerchant.RequestID == original.ProMerchant.RequestID {
		t.Fatal("incorrect reissue state", err)
	}
	var raw string
	if err := s.db.QueryRow("SELECT payload FROM auto_rotation_events WHERE id=?", "merchant-reissue:"+p.ID+":"+original.ProMerchant.RequestID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Details struct {
			Previous  model.ProMerchant `json:"previous_merchant"`
			Automatic bool              `json:"automatic"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil || !event.Details.Automatic || event.Details.Previous.Status != "failed" || event.Details.Previous.RemoteStatus != "failed" || event.Details.Previous.JobID != "first-job" {
		t.Fatal("did not atomically archive failure")
	}
	path := filepath.Dir(s.path)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.RecoverProWorkflows(); err != nil {
		t.Fatal(err)
	}
	due, err := reopened.DueProMerchants(time.Now().Add(time.Minute))
	if err != nil || len(due) != 1 || due[0].ProMerchant.RequestID != p.ProMerchant.RequestID || due[0].ProMerchant.AutoReissues != 1 {
		t.Fatal("restart lost queued reissue", err)
	}
	_, err = reopened.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		p.ProMerchant.JobID, p.ProMerchant.Status, p.ProMerchant.RemoteStatus = "second-job", "failed", "failed"
	})
	if err != nil {
		t.Fatal(err)
	}
	failed.ID = "second-job"
	if _, err := reopened.AutoReissueProMerchant(p.Email, p.ProMerchant.RequestID, 0, failed); err == nil {
		t.Fatal("restart restored consumed allowance")
	}
	manual, err := reopened.ReissueProMerchant(p.Email, p.ProMerchant.RequestID, 0)
	if err != nil || manual.ProMerchant.AutoReissueEnabled || manual.ProMerchant.AutoReissues != 1 {
		t.Fatal("manual reissue replenished automatic allowance", err)
	}
}

func TestStarBridgeMerchantAutoReissueGuards(t *testing.T) {
	s, original, failed := autoReissueMerchantFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*model.MailAccountProfile)
	}{
		{"stopped", func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true }},
		{"stop version changed", func(p *model.MailAccountProfile) { p.ProAuto.StopVersion++ }},
		{"migration paused", func(p *model.MailAccountProfile) { p.ProMigration = &model.ProMigrationState{Paused: true} }},
		{"manual or legacy request", func(p *model.MailAccountProfile) { p.ProMerchant.AutoReissueEnabled = false }},
		{"original merchant", func(p *model.MailAccountProfile) { p.ProMerchant.Provider = refundmerchant.ProviderOriginal }},
		{"mail account", func(p *model.MailAccountProfile) { p.ManagementScope = "mail" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p model.MailAccountProfile
			if err := json.Unmarshal([]byte(mustJSON(original)), &p); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&p)
			if _, err := s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", mustJSON(p), original.Email); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AutoReissueProMerchant(original.Email, original.ProMerchant.RequestID, 0, failed); err == nil {
				t.Fatal("unexpected reissue")
			}
		})
	}
	if _, err := s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", mustJSON(original), original.Email); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProAutoDeletePaused(true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AutoReissueProMerchant(original.Email, original.ProMerchant.RequestID, 0, failed); err == nil {
		t.Fatal("ignored global pause")
	}
	if err := s.SetProAutoDeletePaused(false); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "processing", "verifying", "attention", "succeeded", "banned", "cancelled", ""} {
		job := failed
		job.Status = status
		if _, err := s.AutoReissueProMerchant(original.Email, original.ProMerchant.RequestID, 0, job); err == nil {
			t.Fatal("reissued", status)
		}
	}
	job := failed
	job.ID = "other-job"
	if _, err := s.AutoReissueProMerchant(original.Email, original.ProMerchant.RequestID, 0, job); err == nil {
		t.Fatal("accepted wrong job")
	}
	job = failed
	job.Email = "other@example.com"
	if _, err := s.AutoReissueProMerchant(original.Email, original.ProMerchant.RequestID, 0, job); err == nil {
		t.Fatal("accepted wrong account")
	}
	var requestID string
	if err := s.db.QueryRow("SELECT request_id FROM pro_merchant_requests WHERE email=?", original.Email).Scan(&requestID); err != nil || requestID != original.ProMerchant.RequestID {
		t.Fatal("changed rejected request", err)
	}
}

func TestStarBridgeMerchantAutoReissueManualOrigin(t *testing.T) {
	s := transferStore(t)
	email := "manual-merchant@example.com"
	transferSource(t, s, email)
	cfg := model.DefaultProSettings()
	cfg.MerchantProvider = "starbridge"
	if _, _, err := s.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, "", "", "bridge-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "existing-flow", Status: "waiting_quota"}
	}); err != nil {
		t.Fatal(err)
	}
	manual, _, err := s.PrepareProMerchant(email)
	if err != nil || manual.ProMerchant.AutoReissueEnabled {
		t.Fatal("manual request received automatic allowance", err)
	}
	resumed, _, err := s.PrepareProAutomaticMerchant(email)
	if err != nil || resumed.ProMerchant.RequestID != manual.ProMerchant.RequestID || resumed.ProMerchant.AutoReissueEnabled {
		t.Fatal("automatic continuation changed manual origin", err)
	}
}
