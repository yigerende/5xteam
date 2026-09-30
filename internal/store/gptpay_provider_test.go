package store

import (
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"encoding/json"
	"testing"
)

func TestGPTPayCMSNavTransferKeepsProviderAndSession(t *testing.T) {
	src, dst := transferStore(t), transferStore(t)
	email := "cmsnav-transfer@example.com"
	transferSource(t, src, email)
	o := transferOrder(email, "cmsnav-transfer", "processing")
	o.Order.Provider, o.Order.PlanCode = gptpay.CMSNav, "pro50"
	o.Snapshot.Provider, o.Snapshot.Country, o.Snapshot.URL, o.Snapshot.Input.PlanCode = gptpay.CMSNav, "PH", gptpay.CMSNavURL, "pro50"
	o.Snapshot.Input.Session.Raw = json.RawMessage(`{"user":{"email":"cmsnav-transfer@example.com","name":"Fixture"},"account":{"id":"personal"},"accessToken":"purchase-at","expires":"2099-01-01T00:00:00Z"}`)
	if err := src.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	file, err := src.ExportProAccounts([]string{email}, model.ProDownstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	file = transferWire(t, file)
	if err = ValidateProTransfer(file); err != nil {
		t.Fatal(err)
	}
	if _, err = dst.ImportProAccount(file.Accounts[0], file.ExportedAt, false, model.ProDownstreamRef{}); err != nil {
		t.Fatal(err)
	}
	saved, snapshot, err := dst.GPTPayOrder(o.Order.ID)
	if err != nil || saved.Provider != gptpay.CMSNav || snapshot.Provider != gptpay.CMSNav || snapshot.Country != "PH" || snapshot.Input.PlanCode != "pro50" {
		t.Fatal("import lost supplier", err)
	}
	before, _ := json.Marshal(o.Snapshot)
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("import altered payment snapshot")
	}
	file.Accounts[0].Orders[0].Order.Provider = gptpay.Tokenseek
	if err = ValidateProTransfer(file); err == nil {
		t.Fatal("mismatched supplier accepted")
	}
}

func TestGPTPayProviderIsolationMigrationAndRollback(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	// Existing installations have only a singleton profile without provider.
	sealed, err := s.encrypt("old-provider-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO gptpay_settings(id,profile,encrypted_key) VALUES(1,?,?)`, `{"url":"https://gptpay.tokenseek.app/api/v1","plan_code":"pro20"}`, sealed); err != nil {
		t.Fatal(err)
	}
	cfg, key, err := s.GPTPayProviderSettings(gptpay.CMSNav)
	if err != nil || key != "" || cfg.KeyPresent || cfg.URL != gptpay.CMSNavURL {
		t.Fatal("new provider inherited credentials", err)
	}
	cfg.PlanCode = "pro50"
	if _, _, err = s.SaveProAutomationSettings(model.DefaultProSettings(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	_, key, err = s.GPTPaySettings()
	if err != nil || key != "" {
		t.Fatal("save inherited wrong key")
	}
	if _, err = s.SaveGPTPaySettings(cfg, "new-provider-key"); err != nil {
		t.Fatal(err)
	}
	old, key, err := s.GPTPayProviderSettings(gptpay.Tokenseek)
	if err != nil || key != "old-provider-key" || old.PlanCode != "pro20" {
		t.Fatal("legacy supplier lost", err)
	}
	if _, err = s.SaveGPTPaySettings(old, ""); err != nil {
		t.Fatal(err)
	}
	_, key, err = s.GPTPaySettings()
	if err != nil || key != "old-provider-key" {
		t.Fatal("switch back lost key")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_pay_save BEFORE UPDATE ON gptpay_settings BEGIN SELECT RAISE(ABORT,'fixture rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SaveProAutomationSettings(model.DefaultProSettings(), cfg, "replacement-key"); err == nil {
		t.Fatal("expected transaction failure")
	}
	_, key, err = s.GPTPayProviderSettings(gptpay.CMSNav)
	if err != nil || key != "new-provider-key" {
		t.Fatal("failed transaction changed provider profile")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	active, key, err := s.GPTPaySettings()
	if err != nil || active.Provider != gptpay.Tokenseek || key != "old-provider-key" {
		t.Fatal("active configuration lost on restart")
	}
	cfg, key, err = s.GPTPayProviderSettings(gptpay.CMSNav)
	if err != nil || key != "new-provider-key" || cfg.PlanCode != "pro50" {
		t.Fatal("inactive profile lost on restart")
	}
}
