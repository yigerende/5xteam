package store

import (
	"context"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestEJHConsumptionUsesLatestUserSnapshot(t *testing.T) {
	s := transferStore(t)
	card := ejhFixtureCard(t, s)
	const email = "consumption@example.com"
	transferSource(t, s, email)
	o := transferOrder(email, "consumption-order", "success")
	o.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	check := func(want *float64) {
		t.Helper()
		cases, err := s.EJHCases()
		if err != nil || len(cases) != 1 {
			t.Fatalf("read cases: %v, %v", cases, err)
		}
		got := cases[0].ConsumptionTotal
		if (got == nil) != (want == nil) || want != nil && *got != *want {
			t.Fatalf("consumption: got %v want %v", got, want)
		}
	}
	check(nil)
	for _, value := range []float64{67.0766, 72.5, 0} {
		if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.CostDownstreamSnapshot = 335.3832
			p.UserCostDownstreamIdentity = "42"
			p.UserCostDownstreamSnapshot = value
		}); err != nil {
			t.Fatal(err)
		}
		check(&value)
	}
	if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ClearProCostSnapshot()
	}); err != nil {
		t.Fatal(err)
	}
	check(nil)
	if _, err := s.db.Exec("DELETE FROM mail_accounts WHERE email=?", email); err != nil {
		t.Fatal(err)
	}
	check(nil)
}

func TestEJHConsumptionNotPushedRequiresCompletedMatchingFlow(t *testing.T) {
	s := transferStore(t)
	card := ejhFixtureCard(t, s)
	const email = "never-pushed@example.com"
	transferSource(t, s, email)
	o := transferOrder(email, "never-pushed-order", "success")
	o.Order.CardID = card.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_auto',json('{"id":"flow","order_id":"never-pushed-order","status":"completed","stage":"merge_v2","steps":{"login":"completed","recharge":"failed","merge_v2":"completed"}}')) WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	var base, saved string
	if err := s.db.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, email).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT profile FROM ejh_cases WHERE id=?`, o.Order.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, patch string
		excluded    bool
	}{
		{"completed without push", `{}`, true},
		{"running", `{"pro_auto":{"status":"running"}}`, false},
		{"different order", `{"pro_auto":{"order_id":"other"}}`, false},
		{"missing order", `{"pro_auto":{"order_id":""}}`, false},
		{"workflow running", `{"pro_workflow_running":true}`, false},
		{"push failed", `{"push_status":"failed"}`, false},
		{"provider retained", `{"push_provider":"sub2api"}`, false},
		{"downstream id", `{"sub2_account_id":42}`, false},
		{"vm binding", `{"vm2api":{"id":42}}`, false},
		{"siwc binding", `{"pro_siwc":{}}`, false},
		{"push target", `{"pro_push_downstream":{}}`, false},
		{"push start", `{"pro_push_started_at":"2026-11-09T00:00:00Z"}`, false},
		{"push ended", `{"pro_push_ended_at":"2026-11-09T00:00:00Z"}`, false},
		{"snapshot identity", `{"cost_downstream_identity":"42"}`, false},
		{"connection retained", `{"cost_connection_identity":"source"}`, false},
		{"snapshot amount", `{"user_cost_downstream_snapshot":3}`, false},
		{"queried zero", `{"user_cost_downstream_identity":"42","user_cost_downstream_snapshot":0}`, false},
		{"previous query", `{"cost_checked_at":"2026-11-09T00:00:00Z"}`, false},
		{"push step", `{"pro_auto":{"steps":{"push":"failed"}}}`, false},
		{"old push time", `{"pro_auto":{"stage_times":{"push:completed":"2026-11-09T00:00:00Z"}}}`, false},
		{"manual push", `{"pro_manual_stages":{"push":{"status":"failed"}}}`, false},
		{"override push", `{"pro_stage_overrides":{"push":{}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_patch(?,?) WHERE email=?`, base, tc.patch, email); err != nil {
				t.Fatal(err)
			}
			cases, err := s.EJHCases()
			if err != nil || len(cases) != 1 {
				t.Fatalf("cases: %v %v", cases, err)
			}
			if (cases[0].ConsumptionState == "not_pushed") != tc.excluded {
				t.Fatalf("unexpected state: %q", cases[0].ConsumptionState)
			}
			if tc.excluded && cases[0].ConsumptionTotal != nil {
				t.Fatal("fabricated a queried zero")
			}
		})
	}
	if _, err := s.db.Exec(`DELETE FROM mail_accounts WHERE email=?`, email); err != nil {
		t.Fatal(err)
	}
	cases, err := s.EJHCases()
	if err != nil {
		t.Fatal(err)
	}
	if cases[0].ConsumptionState != "" || cases[0].ConsumptionTotal != nil {
		t.Fatal("deleted account lost its unknown state")
	}
	var after string
	if err := s.db.QueryRow(`SELECT profile FROM ejh_cases WHERE id=?`, o.Order.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if saved != after {
		t.Fatal("read projection changed saved case")
	}
}

func TestEJHConsumptionNotPushedRevisionUpgradesExistingTrigger(t *testing.T) {
	s := transferStore(t)
	transferSource(t, s, "revision-consumption@example.com")
	if _, err := s.db.Exec(`DROP TRIGGER ejh_display_mail_consumption_update`); err != nil {
		t.Fatal(err)
	}
	if err := s.initializeEJHDisplayRevision(); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		v, err := s.EJHDisplayRevision(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return v[:strings.LastIndex(v, ":")]
	}
	change := func(patch string, want bool) {
		t.Helper()
		before := read()
		if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_patch(profile,?)`, patch); err != nil {
			t.Fatal(err)
		}
		if (read() != before) != want {
			t.Fatalf("revision mismatch for %s", patch)
		}
	}
	change(`{"pro_auto":{"status":"completed"}}`, true)
	change(`{"quota_status":"checked"}`, false)
	change(`{"pro_auto":{"updated_at":"2026-11-11T01:00:00Z"}}`, false)
	change(`{"sub2_account_id":42}`, true)
	change(`{"sub2_account_id":43}`, false)
	change(`{"sub2_account_id":0}`, true)
}
