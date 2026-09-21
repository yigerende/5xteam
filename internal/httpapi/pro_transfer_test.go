package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProTransferOAuthBeforePurchaseCannotSkipRecharge(t *testing.T) {
	f := newProExecutionFixture(t)
	f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.OAuthStatus = "completed"
		p.ProMigration = &model.ProMigrationState{Paused: true}
	})
	r := payCall(t, f.s.resumeImportedPro, "email", f.email, map[string]any{})
	if r.Code != 409 || !strings.Contains(r.Body.String(), "尚未确认开通") {
		t.Fatal("unpaid migrated account skipped recharge", r.Code, r.Body.String())
	}
	if len(f.calls) != 0 {
		t.Fatal("unpaid migration called upstream")
	}
}

func TestProTransferHTTPAndResumeWithoutPurchase(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("migration must not pay") })
	configureProAutoFixture(t, s)
	now := time.Now()
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.OAuthStatus = "completed"
		p.OAuthAuthorizedAt = &now
		p.CurrentPlanType = "pro"
	})
	exported := payCall(t, s.exportProAccounts, "", "", map[string]any{"emails": []string{email}})
	if exported.Code != 200 || exported.Header().Get("Cache-Control") != "no-store" || !strings.Contains(exported.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("bad download")
	}
	var file store.ProTransferFile
	if err := json.Unmarshal(exported.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Accounts) != 1 || file.Accounts[0].Credentials.RefreshToken != "do-not-use-rt" {
		t.Fatal("credentials not in export")
	}
	// The download endpoint must still be protected by the admin middleware.
	r := httptest.NewRequest("POST", "/api/pro-accounts/export", strings.NewReader(`{"all":true}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("unauthenticated export allowed")
	}
	// Import to an independently encrypted destination server.
	dst, ds, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("migration paid at destination") })
	configureProAutoFixture(t, dst)
	im := payCall(t, dst.importProAccounts, "", "", map[string]any{"file": file, "overwrite": true})
	if im.Code != 200 || strings.Contains(im.Body.String(), "do-not-use-rt") || !strings.Contains(im.Body.String(), `"updated":1`) {
		t.Fatal("invalid import reply", im.Body.String())
	}
	p, _, _ := ds.MailAccountCredential(email)
	if p.ProMigration == nil || !p.ProMigration.Paused {
		t.Fatal("not paused after import")
	}
	var pushes, quotas atomic.Int32
	dst.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
		t.Error("migrated RT-ready account logged in again")
		return nil
	}, Push: func(ctx context.Context, e string) (model.MailAccountProfile, error) {
		pushes.Add(1)
		return ds.UpdateProAccount(e, func(p *model.MailAccountProfile) {
			p.PushStatus = "completed"
			p.PushProvider = "sub2"
			p.Sub2AccountID = 9
		})
	}, Quota: func(ctx context.Context, e string, _ bool) (model.MailAccountProfile, error) {
		quotas.Add(1)
		return ds.UpdateProAccount(e, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 30} })
	}}
	res := payCall(t, dst.resumeImportedPro, "email", email, map[string]any{})
	if res.Code != 202 {
		t.Fatal("resume failed", res.Code, res.Body.String())
	}
	p = waitProAutoFinished(t, dst, email)
	if p.ProAuto.Status != "waiting_quota" || p.ProMigration.Paused || pushes.Load() != 1 || quotas.Load() != 1 {
		t.Fatal("saved RT did not resume tail", p.ProAuto.Status)
	}
	// A source snapshot replay must not rewind the now-progressed destination.
	im = payCall(t, dst.importProAccounts, "", "", map[string]any{"file": file, "overwrite": true})
	if !strings.Contains(im.Body.String(), `"skipped":1`) {
		t.Fatal("same file rewound destination")
	}
}

func TestProTransferResumeRemainingSpaceStepsWithMappedMother(t *testing.T) {
	f := newProExecutionFixture(t)
	s := f.s
	_, err := s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.InviteStatus, p.AcceptStatus, p.TransferStatus = "completed", "completed", "completed"
		p.SpaceMergedOnce = true
		p.TargetAdminID = "source-local-id"
		p.TargetTeamID = "original-team"
		p.ProMigration = &model.ProMigrationState{Paused: true, TargetTeamID: "original-team"}
	})
	if err != nil {
		t.Fatal(err)
	}
	r := payCall(t, s.resumeImportedPro, "email", f.email, map[string]any{})
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _, _ := s.store.MailAccountCredential(f.email)
		if !p.ProWorkflowRunning && !p.ProMigration.Paused {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p, _, _ := s.store.MailAccountCredential(f.email)
	if p.RemoveStatus != "completed" || p.TargetAdminID != f.admin.ID || p.ProMigration.Paused {
		t.Fatal("remaining removal did not finish", p.RemoveStatus)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 || f.calls["remove"] != 1 {
		t.Fatal("completed steps repeated", f.calls)
	}
}

func TestProTransferDifferentDownstreamAndUnknownMergeBlocked(t *testing.T) {
	f := newProExecutionFixture(t)
	s := f.s
	p, err := s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMigration = &model.ProMigrationState{Paused: true, Downstream: model.ProDownstreamRef{Provider: "sub2", URL: "https://old.invalid", LoginEmail: "old@example.com"}}
		p.PushStatus = "completed"
		p.PushProvider = "sub2"
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _, _ := s.store.ProSettings()
	if proImportedDownstreamOK(p, cfg) == nil {
		t.Fatal("foreign downstream account ID accepted")
	}
	s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.TransferStatus = "unknown" })
	r := payCall(t, s.resumeImportedPro, "email", f.email, map[string]any{})
	if r.Code != 409 {
		t.Fatal("uncertain merge replayed")
	}
	if len(f.calls) != 0 {
		t.Fatal("unexpected upstream call")
	}
}
