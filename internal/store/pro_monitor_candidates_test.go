package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProMonitorCandidatesPreserveActiveWorkWithoutReadingHistorySecrets(t *testing.T) {
	s := transferStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	yes := true
	started := now.Add(-time.Hour)
	cases := []struct {
		name                        string
		edit                        func(*model.MailAccountProfile)
		quota, force, siwc, quality bool
	}{
		{"quota", func(p *model.MailAccountProfile) {}, true, false, false, true},
		{"due", func(p *model.MailAccountProfile) {
			p.ProPushStartedAt = &started
			p.ProPushTimeoutEnabled = &yes
			p.ProPushTimeoutMinutes = 60
		}, true, true, false, true},
		{"future", func(p *model.MailAccountProfile) {
			p.ProPushStartedAt = &now
			p.ProPushTimeoutEnabled = &yes
			p.ProPushTimeoutMinutes = 60
		}, true, false, false, true},
		{"daily", func(p *model.MailAccountProfile) { p.ProDailyMerge = &model.ProDailyMergeRequest{} }, true, true, false, true},
		{"auto", func(p *model.MailAccountProfile) {
			p.ProAuto.ID = "ongoing"
			p.ProPushStartedAt = &started
			p.ProPushTimeoutEnabled = &yes
			p.ProPushTimeoutMinutes = 30
		}, false, true, false, true},
		{"remove", func(p *model.MailAccountProfile) { p.SpaceMergedOnce = true; p.RemoveStatus = "failed" }, true, false, false, true},
		{"vmcleanup", func(p *model.MailAccountProfile) {
			p.SpaceMergedOnce = true
			p.RemoveStatus = "completed"
			p.VM2 = &model.ProVM2Binding{}
		}, true, false, false, false},
		{"stopped", func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true }, false, false, false, true},
		{"mergev2", func(p *model.MailAccountProfile) { p.ProMergeV2 = &model.ProMergeV2{} }, false, false, false, false},
		{"siwc", func(p *model.MailAccountProfile) {
			p.ProSIWC = &model.ProSIWCState{Status: "warming"}
			p.PushStatus = ""
			p.Sub2AccountID = 0
		}, false, false, true, true},
	}
	for _, c := range cases {
		p := model.MailAccountProfile{Email: c.name + "@example.test", ManagementScope: "pro", PushStatus: "completed", PushProvider: "sub2", Sub2AccountID: 1}
		c.edit(&p)
		if _, e := s.SaveMailAccount(p, model.MailAccountCredentials{AccessToken: "fixture"}); e != nil {
			t.Fatal(e)
		}
	}
	// Bulk history deliberately has unusable credentials and large JSON. None of
	// these rows may be decoded by the active candidate queries.
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 1000; i++ {
		p := model.MailAccountProfile{Email: fmt.Sprintf("history-%04d@example.test", i), ManagementScope: "pro", SpaceMergedOnce: true, RemoveStatus: "completed", TransferStatus: "completed", PushStatus: "completed", Label: strings.Repeat("old", 1000)}
		if _, e = tx.Exec("INSERT INTO mail_accounts(email,profile,encrypted_credentials,updated_at) VALUES(?,?,?,?)", p.Email, mustJSON(p), "unreadable", formatTime(now)); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("UPDATE mail_accounts SET encrypted_credentials='unreadable'"); e != nil {
		t.Fatal(e)
	}
	readers := []struct {
		name    string
		read    func() ([]model.MailAccountProfile, error)
		include func(int) bool
	}{
		{"quota", func() ([]model.MailAccountProfile, error) { return s.ProQuotaCandidates(t.Context()) }, func(i int) bool { return cases[i].quota }},
		{"force", func() ([]model.MailAccountProfile, error) { return s.ProForceMergeCandidates(t.Context(), now) }, func(i int) bool { return cases[i].force }},
		{"siwc", func() ([]model.MailAccountProfile, error) { return s.ProSIWCCandidates(t.Context(), true) }, func(i int) bool { return cases[i].siwc }},
		{"quality", func() ([]model.MailAccountProfile, error) { return s.ProQualityCandidates(t.Context()) }, func(i int) bool { return cases[i].quality }},
	}
	for _, r := range readers {
		t.Run(r.name, func(t *testing.T) {
			s.mu.Lock()
			done := make(chan error, 1)
			go func() {
				rows, err := r.read()
				if err != nil {
					done <- err
					return
				}
				got := map[string]bool{}
				for _, p := range rows {
					got[p.Email] = true
				}
				for i, c := range cases {
					if got[c.name+"@example.test"] != r.include(i) {
						done <- fmt.Errorf("%s membership changed", c.name)
						return
					}
					delete(got, c.name+"@example.test")
				}
				if len(got) > 0 {
					done <- fmt.Errorf("read %d historical rows", len(got))
					return
				}
				done <- nil
			}()
			select {
			case e = <-done:
				s.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(3 * time.Second):
				s.mu.Unlock()
				t.Fatal("read waited for business mutex")
			}
		})
	}
	for name, predicate := range map[string]string{"quota": proQuotaCandidates, "force": proForceCandidates, "siwc": proSIWCCandidates, "quality": proQualityCandidates} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN SELECT profile FROM mail_accounts WHERE " + predicate)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail
		}
		rows.Close()
		if !strings.Contains(plan, "USING INDEX mail_pro_") {
			t.Fatal("candidate index unused", name, plan)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = s.ProQuotaCandidates(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestProAccountOptionsUsesSavedPresenceFlagsAndNoCredentials(t *testing.T) {
	s := transferStore(t)
	transferSource(t, s, "picker@example.test")
	if _, err := s.db.Exec("UPDATE mail_accounts SET encrypted_credentials='unreadable'"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	done := make(chan error, 1)
	go func() {
		rows, err := s.ProAccountOptions(t.Context())
		if err == nil && (len(rows) != 1 || !rows[0].AccessTokenPresent || rows[0].Email != "picker@example.test") {
			err = fmt.Errorf("incorrect options: %+v", rows)
		}
		done <- err
	}()
	select {
	case err := <-done:
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		s.mu.Unlock()
		t.Fatal("picker waited for main mutex")
	}
}

func TestProSupplyCandidatesKeepDeadAccountsAndExcludeCompletedHistory(t *testing.T) {
	s := transferStore(t)
	for _, name := range []string{"active", "removed", "dead", "other"} {
		p := model.MailAccountProfile{Email: name + "@example.test", ManagementScope: "pro", PushProvider: "sub2", PushStatus: "completed", Sub2AccountID: 1}
		if name == "removed" || name == "dead" {
			p.SpaceMergedOnce = true
			p.RemoveStatus = "completed"
		}
		if name == "dead" {
			p.ProDead = &model.ProDeadState{At: time.Now()}
		}
		if name == "other" {
			p.PushProvider = "cpa"
		}
		if _, err := s.SaveMailAccount(p, model.MailAccountCredentials{}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ProSupplyProfiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range rows {
		got[p.Email] = true
	}
	if len(got) != 2 || !got["active@example.test"] || !got["dead@example.test"] {
		t.Fatal(got)
	}
}

func TestLegacyMailReadsDoNotHoldBusinessLock(t *testing.T) {
	s := transferStore(t)
	transferSource(t, s, "pro@example.test")
	if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: "mail@example.test"}, model.MailAccountCredentials{AccessToken: "fixture-at"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	done := make(chan error, 1)
	go func() {
		pro := s.MailAccountsByManagementScope("pro")
		mail := s.MailAccountsByManagementScope("mail")
		if len(pro) != 1 || len(mail) != 1 || !pro[0].AccessTokenPresent || !mail[0].AccessTokenPresent {
			done <- fmt.Errorf("scope/presence changed")
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		s.mu.Unlock()
		t.Fatal("legacy list blocked on business lock")
	}
}
