package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptregister"
	"chatgpt-space-merge/internal/model"
)

func gprStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func gprConfigure(t *testing.T, s *Store, pro bool) {
	t.Helper()
	_, e := s.SaveGPTRegisterSettings(gptregister.Submission{Settings: gptregister.Settings{URL: "https://register.example.com/open/v1/", EnterPro: pro}, APIKey: "key-fixture"})
	if e != nil {
		t.Fatal(e)
	}
}
func TestGPTRegisterSettingsIntentAndLease(t *testing.T) {
	s := gprStore(t)
	gprConfigure(t, s, true)
	cfg, e := s.GPTRegisterSettings()
	if e != nil || cfg.URL != "https://register.example.com" || !cfg.KeyPresent {
		t.Fatal(cfg.Settings, e)
	}
	if _, e = s.ResolveGPTRegisterSettings(gptregister.Submission{Settings: gptregister.Settings{URL: "https://other.example.com"}}); e == nil {
		t.Fatal("key forwarded to different host")
	}
	if _, e = s.SaveGPTRegisterSettings(gptregister.Submission{Settings: cfg.Settings}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := s.CreateGPTRegisterJob("browser-intent", 2, true)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- j.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id != "" && v != id {
			t.Fatal("duplicate jobs")
		}
		id = v
	}
	if _, e = s.CreateGPTRegisterJob("browser-intent", 3, true); e == nil {
		t.Fatal("changed payload accepted")
	}
	var sealed string
	if e = s.db.QueryRow(`SELECT payload FROM gptregister_settings`).Scan(&sealed); e != nil || strings.Contains(sealed, "key-fixture") {
		t.Fatal("unencrypted settings", e)
	}
	j, snapshot, lease, e := s.ClaimGPTRegisterJob(time.Now())
	if e != nil || lease == "" || !snapshot.EnterPro || j.ID != id {
		t.Fatal(e)
	}
	if _, _, _, e = s.ClaimGPTRegisterJob(time.Now()); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("double claimed", e)
	}
	gprConfigure(t, s, false)
	if e = s.RetryGPTRegisterJob(j.ID); e == nil {
		t.Fatal("active lease stolen")
	}
	if e = s.SaveGPTRegisterJob(j, "wrong", true); e == nil {
		t.Fatal("invalid lease accepted")
	}
	if e = s.SaveGPTRegisterJob(j, lease, true); e != nil {
		t.Fatal(e)
	}
	_, snapshot, _, e = s.ClaimGPTRegisterJob(time.Now())
	if e != nil || !snapshot.EnterPro {
		t.Fatal("job changed with config", e)
	}
}

func TestGPTRegisterImportAtomicDedupAndDelete(t *testing.T) {
	s := gprStore(t)
	gprConfigure(t, s, true)
	original, e := s.SaveMailAccount(model.MailAccountProfile{Email: "existing@example.com"}, model.MailAccountCredentials{Email: "existing@example.com", GptPassword: "keep-password"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.CreateGPTRegisterJob("import-first", 3, true)
	if e != nil {
		t.Fatal(e)
	}
	j, _, lease, e := s.ClaimGPTRegisterJob(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	j.RemoteUserID = "u1"
	j.RemoteOrderID = "o1"
	c := gptregister.Credentials{AssetID: "engine-id", Email: "fresh@example.com", Password: "new-password", TOTP: "JBSWY3DPEHPK3PXP", OAuth: map[string]json.RawMessage{"access_token": json.RawMessage(`"at-fixture"`), "refresh_token": json.RawMessage(`"rt-fixture"`), "extra_metadata": json.RawMessage(`{"preserved":true}`)}}
	a := gptregister.Account{ID: "delivery1", OrderID: "o1", Email: c.Email, NeedOAuth: true}
	if e = s.ImportGPTRegisterDelivery(j, "wrong", a, c); e == nil {
		t.Fatal("stale lease imported")
	}
	if _, _, e = s.MailAccountCredential(c.Email); e == nil {
		t.Fatal("failed import leaked mailbox")
	}
	for i := 0; i < 2; i++ {
		if e = s.ImportGPTRegisterDelivery(j, lease, a, c); e != nil {
			t.Fatal(e)
		}
	}
	p, credentials, e := s.MailAccountCredential(c.Email)
	if e != nil || p.ManagementScope != "pro" || p.ProAuto.ID != "" || p.ProWorkflowRunning || p.OAuthStatus != "completed" || credentials.RefreshToken != "rt-fixture" || len(s.FreeAccounts()) != 0 {
		t.Fatal("bad ownership/credentials/automation", e)
	}
	var raw string
	if e = s.db.QueryRow(`SELECT credentials FROM gptregister_deliveries`).Scan(&raw); e != nil || strings.Contains(raw, "at-fixture") {
		t.Fatal("unencrypted delivery", e)
	}
	plain, e := s.decrypt(raw)
	if e != nil || !strings.Contains(plain, "extra_metadata") {
		t.Fatal("complete OAuth not retained", e)
	}
	a.ID = "delivery2"
	a.Email = original.Email
	c.Email = original.Email
	if e = s.ImportGPTRegisterDelivery(j, lease, a, c); e != nil {
		t.Fatal(e)
	}
	after, kept, e := s.MailAccountCredential(original.Email)
	if e != nil || after.ID != original.ID || after.ManagementScope == "pro" || kept.GptPassword != "keep-password" {
		t.Fatal("overwrote existing account", e)
	}
	if _, e = s.db.Exec(`DELETE FROM mail_accounts WHERE email='fresh@example.com'`); e != nil {
		t.Fatal(e)
	}
	a.ID = "delivery1"
	a.Email = "fresh@example.com"
	c.Email = a.Email
	if e = s.ImportGPTRegisterDelivery(j, lease, a, c); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.MailAccountCredential(c.Email); e == nil {
		t.Fatal("deleted delivery reimported")
	}
	im, sk, e := s.GPTRegisterCounts(j.ID)
	if e != nil || im != 1 || sk != 1 {
		t.Fatal(im, sk, e)
	}
	// A new local task with the same remote delivery is also deduplicated.
	if e = s.SaveGPTRegisterJob(j, lease, true); e != nil {
		t.Fatal(e)
	}
	j.State = "completed"
	_, _, lease, e = s.ClaimGPTRegisterJob(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SaveGPTRegisterJob(j, lease, true); e != nil {
		t.Fatal(e)
	}
	_, e = s.CreateGPTRegisterJob("second-job", 1, true)
	if e != nil {
		t.Fatal(e)
	}
	j, _, lease, e = s.ClaimGPTRegisterJob(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	j.RemoteUserID = "u1"
	j.RemoteOrderID = "o1"
	if e = s.ImportGPTRegisterDelivery(j, lease, a, c); e != nil {
		t.Fatal(e)
	}
	im, sk, e = s.GPTRegisterCounts(j.ID)
	if e != nil || im != 0 || sk != 1 {
		t.Fatal(im, sk, e)
	}
	if _, _, e = s.MailAccountCredential(c.Email); e == nil {
		t.Fatal("new task recreated old delivery")
	}
}

func TestGPTRegisterReceiptFailureRollsBackMailbox(t *testing.T) {
	s := gprStore(t)
	gprConfigure(t, s, false)
	s.CreateGPTRegisterJob("rollback-intent", 1, false)
	j, _, lease, e := s.ClaimGPTRegisterJob(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	j.RemoteUserID = "u1"
	j.RemoteOrderID = "o1"
	_, e = s.db.Exec(`CREATE TRIGGER fail_gpr_receipt BEFORE INSERT ON gptregister_receipts BEGIN SELECT RAISE(ABORT,'test failure'); END`)
	if e != nil {
		t.Fatal(e)
	}
	a := gptregister.Account{ID: "a1", OrderID: "o1", Email: "atomic@example.com"}
	c := gptregister.Credentials{Email: a.Email, Password: "test", TOTP: "test"}
	if e = s.ImportGPTRegisterDelivery(j, lease, a, c); e == nil {
		t.Fatal("expected receipt failure")
	}
	if _, _, e = s.MailAccountCredential(a.Email); e == nil {
		t.Fatal("mailbox survived rolled-back receipt")
	}
	var n int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM gptregister_deliveries`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
