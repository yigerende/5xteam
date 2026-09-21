package store

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func transferStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func transferSource(t *testing.T, s *Store, email string) {
	t.Helper()
	_, err := s.SaveMailAccount(model.MailAccountProfile{Email: email, Label: "source", Group: "pro"}, model.MailAccountCredentials{Email: email, MailPassword: "mail-pw", GptPassword: "gpt-pw", TotpSecret: "totp", AccessToken: "saved-at", RefreshToken: "saved-rt", ChatGPTSession: `{"accessToken":"web-at","session_token":"web-cookie"}`})
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateMailAccountManagementScope(email, "pro")
}
func transferOrder(email, id, status string) ProTransferOrder {
	now := time.Now().UTC()
	snap := gptpay.Snapshot{URL: gptpay.DefaultURL, APIKey: "supplier-key", Input: gptpay.CreateInput{PlanCode: "pro20", CardSecret: gptpay.CardSecret{Number: "4242424242424242", Year: 2099, Month: 12, CVV: "123"}}}
	snap.Input.Session.User.Email = email
	snap.Input.Session.Account.ID = "personal"
	snap.Input.Session.AccessToken = "purchase-at"
	return ProTransferOrder{Order: gptpay.Order{ID: id, Email: email, PlanCode: "pro20", Status: status, CardName: "source card", CardLast4: "4242", CreatedAt: now, UpdatedAt: now, Remote: gptpay.RemoteOrder{ID: "remote-" + id, Status: status}}, Snapshot: snap}
}
func transferWire(t *testing.T, f ProTransferFile) ProTransferFile {
	t.Helper()
	raw, e := json.Marshal(f)
	if e != nil {
		t.Fatal(e)
	}
	var decoded ProTransferFile
	if e = json.Unmarshal(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	return decoded
}

func TestProTransferEveryStageAcrossIndependentStores(t *testing.T) {
	for _, stage := range []string{"empty", "login", "recharge", "oauth", "push", "quota", "invite", "accept", "transfer", "remove", "done", "manual", "pending_payment"} {
		t.Run(stage, func(t *testing.T) {
			src, dst := transferStore(t), transferStore(t)
			email := "transfer@example.com"
			transferSource(t, src, email)
			ref := model.ProDownstreamRef{Provider: "sub2", URL: "https://sub2.test", LoginEmail: "admin@test.com"}
			original, _, _ := src.MailAccountCredential(email)
			now := time.Now().UTC()
			o := transferOrder(email, "order-"+stage, "success")
			if stage == "pending_payment" {
				o.Order.Status = "processing"
			}
			if stage != "empty" && stage != "login" {
				if e := src.CreateGPTPayOrder(o.Order, o.Snapshot); e != nil {
					t.Fatal(e)
				}
			}
			_, err := src.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = model.ProAutoState{ID: "source-task", Stage: stage, Status: "running", StartedAt: now.Add(-time.Hour), Steps: map[string]string{"login": "completed", "recharge": "completed", "oauth": "completed"}, QuotaUsedThreshold: 85, QuotaIntervalSeconds: 45}
				if stage != "empty" && stage != "login" {
					p.ProAuto.OrderID = o.Order.ID
				}
				p.ProAuto.Steps[stage] = "running"
				p.ProWorkflowRunning = stage == "transfer" || stage == "remove"
				p.PushStatus = "completed"
				p.PushProvider = "sub2"
				p.Sub2AccountID = 42
				p.OAuthStatus = "completed"
				p.OAuthAuthorizedAt = &now
				p.TargetAdminID = "source-mother-id"
				p.TargetTeamID = "source-team"
				p.InviteStatus = "completed"
				p.AcceptStatus = "completed"
				if stage == "transfer" {
					p.TransferStatus = "running"
				}
				if stage == "remove" || stage == "done" {
					p.SpaceMergedOnce = true
					p.TransferStatus = "completed"
					p.RemoveStatus = "running"
				}
				if stage == "done" {
					p.RemoveStatus = "completed"
					p.ProAuto.Status = "completed"
					p.ProAuto.Steps["merge"] = "completed"
				}
				if stage == "manual" {
					p.ProAuto = model.ProAutoState{}
					p.ProManualStages = map[string]model.ProStageProgress{"login": {ID: "manual-login", Status: "completed"}, "oauth": {ID: "manual-oauth", Status: "running"}}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			admin := model.AdminAccountProfile{ID: "destination-mother", Label: "dest", TeamAccountID: "source-team"}
			raw, _ := json.Marshal(admin)
			if _, err = dst.db.Exec("INSERT INTO admin_accounts(id,label,profile,encrypted_access_token,encrypted_refresh_token) VALUES(?,?,?,?,?)", admin.ID, admin.Label, string(raw), "", ""); err != nil {
				t.Fatal(err)
			}
			file, err := src.ExportProAccounts([]string{email}, ref)
			if err != nil {
				t.Fatal(err)
			}
			file = transferWire(t, file)
			if err = ValidateProTransfer(file); err != nil {
				t.Fatal(err)
			}
			result, err := dst.ImportProAccount(file.Accounts[0], file.ExportedAt, false, ref)
			if err != nil || result.Status != "imported" {
				t.Fatal("import failed", err, result.Status)
			}
			p, c, err := dst.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			if p.ID == original.ID || p.TargetAdminID != admin.ID || p.TargetTeamID != "source-team" {
				t.Fatal("local IDs were not remapped")
			}
			if c.AccessToken != "saved-at" || c.RefreshToken != "saved-rt" || !strings.Contains(c.ChatGPTSession, "web-cookie") || c.GptPassword != "gpt-pw" || c.TotpSecret != "totp" {
				t.Fatal("credential loss")
			}
			if p.ProWorkflowRunning || p.ProAuto.Status == "running" || p.ProMigration == nil || !p.ProMigration.Paused {
				t.Fatal("import launched or retained a running task")
			}
			if stage == "transfer" && p.TransferStatus != "unknown" {
				t.Fatal("uncertain merge would be replayed")
			}
			if stage == "done" && (p.RemoveStatus != "completed" || !p.SpaceMergedOnce) {
				t.Fatal("completed progress lost")
			}
			if stage == "manual" && (p.ProManualStages["login"].Status != "completed" || p.ProManualStages["oauth"].Status != "interrupted") {
				t.Fatal("manual progress lost")
			}
			if p.Sub2AccountID != 42 || p.PushStatus != "completed" {
				t.Fatal("same-service linkage lost")
			}
			if stage != "empty" && stage != "login" {
				order, snapshot, e := dst.GPTPayOrder(o.Order.ID)
				if e != nil || order.Remote.ID != o.Order.Remote.ID || snapshot.APIKey != "supplier-key" || snapshot.Input.CVV != "123" {
					t.Fatal("order cannot be resumed", e)
				}
			}
			var encrypted string
			if err = dst.db.QueryRow("SELECT encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&encrypted); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encrypted, "saved-at") || strings.Contains(encrypted, "gpt-pw") {
				t.Fatal("destination credentials not encrypted")
			}
			// A replay after local advancement is a no-op even with overwrite.
			dst.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" })
			result, err = dst.ImportProAccount(file.Accounts[0], file.ExportedAt, true, ref)
			if err != nil || result.Status != "skipped" {
				t.Fatal("replay not idempotent", err, result.Status)
			}
			p, _, _ = dst.MailAccountCredential(email)
			if p.RemoveStatus != "completed" {
				t.Fatal("replay rewound progress")
			}
			due, err := dst.DueProAutomations(time.Now().Add(time.Hour))
			if err != nil || len(due) != 0 {
				t.Fatal("import scheduled background actions")
			}
			before, _, _ := src.MailAccountCredential(email)
			if stage != "manual" && stage != "done" && before.ProAuto.Status != "running" {
				t.Fatal("export mutated source task")
			}
		})
	}
}

func TestProTransferConflictsAndAtomicRollback(t *testing.T) {
	src, dst := transferStore(t), transferStore(t)
	email := "conflict@example.com"
	transferSource(t, src, email)
	o := transferOrder(email, "original-order", "processing")
	if err := src.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	f, err := src.ExportProAccounts([]string{email}, model.ProDownstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dst.db.Exec(`CREATE TRIGGER reject_import_order BEFORE INSERT ON gptpay_orders BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = dst.ImportProAccount(f.Accounts[0], f.ExportedAt, false, model.ProDownstreamRef{}); err == nil {
		t.Fatal("expected failure")
	}
	if _, _, err = dst.MailAccountCredential(email); err == nil {
		t.Fatal("account written despite failed order")
	}
	dst.db.Exec("DROP TRIGGER reject_import_order")
	other := o
	other.Order.Email = "someoneelse@example.com"
	if err = dst.CreateGPTPayOrder(other.Order, other.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = dst.ImportProAccount(f.Accounts[0], f.ExportedAt, false, model.ProDownstreamRef{}); err == nil {
		t.Fatal("conflicting order overwritten")
	}
	if _, _, err = dst.MailAccountCredential(email); err == nil {
		t.Fatal("conflict left partial account")
	}
}

func TestProTransferDuplicateConcurrentImportAndSelection(t *testing.T) {
	src, dst := transferStore(t), transferStore(t)
	for _, email := range []string{"one@example.com", "two@example.com"} {
		transferSource(t, src, email)
	}
	src.UpdateProAccount("two@example.com", func(p *model.MailAccountProfile) { p.SpaceMergedOnce = true })
	emails, e := src.ProExportEmails("", "unmerged")
	if e != nil || len(emails) != 1 || emails[0] != "one@example.com" {
		t.Fatal("wrong export filter")
	}
	f, e := src.ExportProAccounts(emails, model.ProDownstreamRef{})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := dst.ImportProAccount(f.Accounts[0], f.ExportedAt, true, model.ProDownstreamRef{})
			if err != nil {
				t.Error(err)
			}
			results <- r.Status
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	for status := range results {
		if status == "imported" {
			created++
		}
	}
	if created != 1 {
		t.Fatal("concurrent import duplicated account")
	}
	p, _, _ := dst.MailAccountCredential("one@example.com")
	dst.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { p.ProWorkflowRunning = true })
	changed := transferWire(t, f)
	changed.Accounts[0].Profile.Label = "new"
	if _, err := dst.ImportProAccount(changed.Accounts[0], changed.ExportedAt, true, model.ProDownstreamRef{}); err == nil {
		t.Fatal("overwrote running task")
	}
}

func TestProTransferRejectsInvalidFile(t *testing.T) {
	src := transferStore(t)
	transferSource(t, src, "valid@example.com")
	f, e := src.ExportProAccounts([]string{"valid@example.com"}, model.ProDownstreamRef{})
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"version", "duplicate", "missing_order", "email", "session"} {
		bad := transferWire(t, f)
		switch kind {
		case "version":
			bad.Version = 99
		case "duplicate":
			bad.Accounts = append(bad.Accounts, bad.Accounts[0])
		case "missing_order":
			bad.Accounts[0].Profile.ProAuto.OrderID = "missing"
		case "email":
			bad.Accounts[0].Credentials.Email = "wrong@example.com"
		case "session":
			bad.Accounts[0].Credentials.ChatGPTSession = "invalid"
		}
		if ValidateProTransfer(bad) == nil {
			t.Fatal("invalid file accepted", kind)
		}
	}
}
