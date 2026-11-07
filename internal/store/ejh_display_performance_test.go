package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fullEJHCaseQuery struct{ db *sql.DB }

func (q fullEJHCaseQuery) Query(query string, args ...any) (*sql.Rows, error) {
	query = strings.ReplaceAll(query, ejhCaseOrderProjection, "o.profile")
	query = strings.ReplaceAll(query, ejhCaseAccountProjection, "COALESCE(m.profile,'{}')")
	return q.db.Query(query, args...)
}

func TestEJHCaseCompactProjectionEquivalent(t *testing.T) {
	s := transferStore(t)
	c := ejhFixtureCard(t, s)
	transferSource(t, s, "projection@example.com")
	o := transferOrder("projection@example.com", "projection-order", "success")
	o.Order.CardID = c.ID
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.EJHBackfillCases(); err != nil {
		t.Fatal(err)
	}
	// Old full profiles contain unrelated provider events and mailbox history.
	// All OAI prerequisite branches must agree with the compact projection.
	for _, fragment := range []string{
		`{}`, `{"pro_merge_v2":{"completed_at":"2026-11-01T00:00:00Z"}}`,
		`{"pro_deactivation":{"confirmed_at":"2026-11-01T00:00:00Z"}}`,
		`{"pro_merchant":{"completed_at":"2026-11-01T00:00:00Z","retired_at":"2026-11-01T00:00:00Z"}}`,
		`{"pro_invite_status":"completed","pro_accept_status":"completed","pro_transfer_status":"completed","pro_remove_status":"completed"}`,
		`{"pro_invite_status":"completed","pro_accept_status":"completed","pro_transfer_status":"completed","pro_remove_status":"completed","vm2api":{"stage":"delete_failed"}}`,
		`{"pro_invite_status":"completed","pro_accept_status":"completed","pro_transfer_status":"completed","pro_remove_status":"completed","pro_post_merge_groups":{"status":"failed"}}`,
		`{"pro_auto":{"order_id":"different"}}`,
	} {
		if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_patch(profile,?)`, fragment); err != nil {
			t.Fatal(err)
		}
		want, err := s.ejhCasesQuery(fullEJHCaseQuery{s.db}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.EJHCases()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("projection differs for %s", fragment)
		}
	}
}

func TestEJHDisplayBatchMatchesLegacyStates(t *testing.T) {
	s := transferStore(t)
	c := ejhFixtureCard(t, s)
	_, secret, err := s.GPTPayCard(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := proCardKey(secret.Number)
	transferSource(t, s, "batch@example.com")
	o := transferOrder("batch@example.com", "batch-order", "success")
	o.Order.CardID = c.ID
	if err = s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	changes := []struct {
		sql  string
		args []any
	}{
		{`UPDATE ejh_settings SET profile=json_set(profile,'$.enabled',0)`, nil},
		{`INSERT INTO pro_card_limits VALUES(?,7)`, []any{key}},
		{`INSERT INTO pro_card_reservations VALUES('reserved@example.com',?)`, []any{key}},
		{`UPDATE pro_card_usage SET status='unknown'`, nil},
		{`UPDATE pro_card_usage SET status='failed'`, nil},
		{`UPDATE pro_card_usage SET status='success'`, nil},
		{`INSERT INTO ejh_received_returns VALUES('returned','batch-order')`, nil},
		{`DELETE FROM mail_accounts`, nil},
		{`INSERT INTO pro_dead_cards VALUES(?,'batch@example.com','{}')`, []any{key}},
	}
	for _, change := range changes {
		if _, err = s.db.Exec(change.sql, change.args...); err != nil {
			t.Fatal(err)
		}
		want, _, err := s.GPTPayCards(100, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GPTPayCardsByType("ejh")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("batch differs after %s", change.sql)
		}
	}
}

func TestEJHDisplayRevisionScopedTransactional(t *testing.T) {
	s := transferStore(t)
	// Compare the persisted generation separately from its minute expiry bucket.
	revision := func() string {
		t.Helper()
		v, e := s.EJHDisplayRevision(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		return v[:strings.LastIndex(v, ":")]
	}
	first := revision()
	s.mu.Lock()
	_ = revision()
	s.mu.Unlock()
	if err := s.EJHPut("match_audit", "test", map[string]string{"log": "only"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EJHPut("finance_snapshot", "test", map[string]string{"log": "only"}); err != nil {
		t.Fatal(err)
	}
	if revision() != first {
		t.Fatal("audit invalidated financial view")
	}
	transferSource(t, s, "revision@example.com")
	first = revision()
	if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.quota_status','checked')`); err != nil {
		t.Fatal(err)
	}
	if revision() != first {
		t.Fatal("unrelated account quota invalidated view")
	}
	if _, err := s.db.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.user_cost_downstream_identity','source','$.user_cost_downstream_snapshot',12)`); err != nil {
		t.Fatal(err)
	}
	if revision() == first {
		t.Fatal("account revenue not observed")
	}
	first = revision()
	other, err := sql.Open("sqlite", "file:"+s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec(`UPDATE ejh_records SET kind='balance' WHERE kind='match_audit'`); err != nil {
		t.Fatal(err)
	}
	next := revision()
	if next == first {
		t.Fatal("external update not observed")
	}
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM ejh_records WHERE kind='balance'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if revision() != next {
		t.Fatal("rollback invalidated view")
	}
	if _, err = other.Exec(`DELETE FROM ejh_records WHERE kind='balance'`); err != nil {
		t.Fatal(err)
	}
	if revision() == next {
		t.Fatal("delete not observed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.EJHDisplayRevision(ctx); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestSettingsReaderDoesNotWaitForBusinessLock(t *testing.T) {
	s := transferStore(t)
	want := s.Settings()
	r, err := s.ProListReader(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan []byte, 1)
	go func() { b, _ := json.Marshal(r.Settings()); done <- b }()
	select {
	case b := <-done:
		expected, _ := json.Marshal(want)
		if string(b) != string(expected) {
			t.Fatal("settings changed")
		}
	case <-time.After(time.Second):
		t.Fatal("settings waited for business lock")
	}
}
