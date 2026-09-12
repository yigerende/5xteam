package store

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func historyFixture(t *testing.T, s *Store, email string) model.FreeAccountProfile {
	t.Helper()
	p, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "source-at")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTeamHistoryEntryIsPermanentIdempotentAndCycleIsolated(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.SaveAdminAccount(model.AdminAccountProfile{Email: "mother@example.com", TeamAccountID: "team-one"}, "mother-token")
	if err != nil {
		t.Fatal(err)
	}
	p := historyFixture(t, s, "child@example.com")
	originalImported := p.ImportedAt
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.UpdateFreeAccount(p.ID, func(a *model.FreeAccountProfile) {
				now := time.Now()
				a.AdminAccountID = admin.ID
				a.AdminEmail = admin.Email
				a.TeamAccountID = admin.TeamAccountID
				a.AcceptStatus = "completed"
				a.JoinedAt = &now
				a.Status = "joined"
			})
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	got, _, err := s.FreeAccountCredential(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	visits, err := s.TeamVisits(p.Email, p.UserID)
	if err != nil || len(visits) != 1 || got.VisitedTeamCount != 1 {
		t.Fatalf("visits=%+v count=%d err=%v", visits, got.VisitedTeamCount, err)
	}
	if s.AdminAccounts()[0].TeamRotationChildCount != 1 {
		t.Fatal("duplicate callbacks inflated mother counter")
	}
	firstEntered := *visits[0].EnteredAt
	if _, err = s.SaveFreeAccountOAuth(p.ID, "old-at", "old-rt", "team-one"); err != nil {
		t.Fatal(err)
	}
	got, err = s.UpdateFreeAccount(p.ID, func(a *model.FreeAccountProfile) {
		now := time.Now()
		a.RemoveStatus = "completed"
		a.Status = "removed"
		a.RemovedAt = &now
		a.RemoteRemovedAt = &now
		a.DownstreamCleaned = true
		a.TotalCostUSD = 12
		a.CostByAdmin = map[string]float64{admin.ID: 12}
		a.ReloginFailureCount = 2
		a.Sub2AccountID = 15
		a.RemovalReason = "oauth_failed"
	})
	if err != nil {
		t.Fatal(err)
	}
	oldCycle := got.CycleID
	var started atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.BeginFreeAccountCycle(got.ID, oldCycle, "new-source")
			if e == nil {
				started.Add(1)
			} else if !errors.Is(e, ErrStaleCycle) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if started.Load() != 1 {
		t.Fatalf("started %d cycles", started.Load())
	}
	next, creds, err := s.FreeAccountCredential(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.CycleID == oldCycle || next.TeamAccountID != "" || next.OAuthStatus != "pending" || next.Sub2AccountID != 0 || next.ReloginFailureCount != 0 || next.TotalCostUSD != 12 || next.CostByAdmin[admin.ID] != 12 || !next.ImportedAt.Equal(originalImported) || !next.ReusePending || creds.OAuthAccessToken != "" || creds.OAuthRefreshToken != "" || creds.SourceAccessToken != "new-source" {
		t.Fatalf("reset lost or retained wrong state: %+v", next)
	}
	if _, err = s.UpdateFreeAccountCycle(p.ID, oldCycle, func(a *model.FreeAccountProfile) { a.LastError = "stale" }); !errors.Is(err, ErrStaleCycle) {
		t.Fatalf("stale update accepted: %v", err)
	}
	if _, err = s.SaveFreeAccountOAuth(p.ID, "stale-at", "stale-rt", "team-one", oldCycle); !errors.Is(err, ErrStaleCycle) {
		t.Fatalf("stale OAuth accepted: %v", err)
	}
	if used, err := s.HasVisitedTeam(next, "team-one"); err != nil || !used {
		t.Fatal("used team forgotten")
	}
	if used, err := s.HasVisitedTeam(next, "team-two"); err != nil || used {
		t.Fatal("new mother incorrectly blocked")
	}
	if err = s.PurgeAutoRotationHistory(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteFreeAccount(p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, created, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: p.Email, UserID: p.UserID}, "reimport-at")
	if err != nil || !created || restored.VisitedTeamCount != 1 || restored.CycleID != next.CycleID {
		t.Fatalf("reimport lost ledger: %+v err=%v", restored, err)
	}
	visits, _ = s.TeamVisits(p.Email, "")
	if len(visits) != 1 || !visits[0].EnteredAt.Equal(firstEntered) || visits[0].RemovalReason != "oauth_failed" {
		t.Fatalf("history changed: %+v", visits)
	}
}

func TestTeamHistoryFourFiltersAndDeadStillConsumesSeat(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	states := []string{"outside", "inside", "removed", "dead"}
	for _, state := range states {
		for i := 0; i < 12; i++ {
			p := historyFixture(t, s, fmt.Sprintf("%s-%02d@example.com", state, i))
			if state != "outside" {
				_, err = s.UpdateFreeAccount(p.ID, func(a *model.FreeAccountProfile) {
					now := time.Now()
					a.TeamAccountID = "team"
					a.AcceptStatus = "completed"
					a.JoinedAt = &now
					a.SeatType = "prolite"
					a.Dead = state == "dead"
					if state == "removed" {
						a.RemoveStatus = "completed"
						a.RemovedAt = &now
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, state := range states {
		items, total, summary, err := s.FreeAccountsPage(state, 10, 10)
		if err != nil || total != 12 || len(items) != 2 || summary.All != 48 || summary.InsidePremium != 24 {
			t.Fatalf("free %s: total=%d items=%d summary=%+v err=%v", state, total, len(items), summary, err)
		}
		mail, err := s.MailAccountsPage("mail", "", state, 10, 10)
		if err != nil || mail.Total != 12 || len(mail.Items) != 2 || mail.SpaceCounts[state] != 12 {
			t.Fatalf("mail %s: %+v err=%v", state, mail, err)
		}
		for _, item := range mail.Items {
			if state != "outside" && item.VisitedTeamCount != 1 {
				t.Fatal("mail count projection missing")
			}
		}
	}
}

func TestTeamHistoryMigrationAndReusePreconditions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := historyFixture(t, s, "legacy@example.com")
	// Simulate a pre-feature row and a lost legacy history table entry.
	_, err = s.db.Exec(`UPDATE free_accounts SET profile=json_set(profile,'$.cycle_id','','$.team_account_id','legacy-team','$.accept_status','completed','$.remove_status','completed') WHERE id=?`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _, _ = s.FreeAccountCredential(p.ID)
	if !p.HistoryUncertain || p.VisitedTeamCount != 1 {
		t.Fatalf("legacy silently trusted: %+v", p)
	}
	if _, err = s.BeginFreeAccountCycle(p.ID, p.CycleID, "at"); err == nil {
		t.Fatal("unreviewed history reused")
	}
	if err = s.ReviewTeamHistory(p, []string{"another-historical-team"}); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.FreeAccountCredential(p.ID)
	if p.HistoryUncertain || p.VisitedTeamCount != 2 {
		t.Fatal("history review not saved")
	}
	_, _ = s.UpdateFreeAccount(p.ID, func(a *model.FreeAccountProfile) { a.Dead = true })
	if _, err = s.BeginFreeAccountCycle(p.ID, p.CycleID, "at"); err == nil {
		t.Fatal("dead account reused")
	}
	_, _ = s.UpdateFreeAccount(p.ID, func(a *model.FreeAccountProfile) { a.Dead = false; a.DownstreamCleaned = false })
	if _, err = s.BeginFreeAccountCycle(p.ID, p.CycleID, "at"); err == nil {
		t.Fatal("uncleaned cycle reused")
	}
}
