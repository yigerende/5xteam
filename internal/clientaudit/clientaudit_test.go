package clientaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDB(t *testing.T) *Database {
	t.Helper()
	d, e := OpenDatabase(filepath.Join(t.TempDir(), "audit.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func usage(id int64, at time.Time, cost string) json.RawMessage {
	return raw(map[string]any{"id": id, "user_id": 1, "api_key_id": 2, "request_id": fmt.Sprint("req-", id), "model": "model", "created_at": at.Format(time.RFC3339Nano), "actual_cost": json.Number(cost), "total_cost": json.Number(cost), "rate_multiplier": 1, "billing_type": 0, "input_tokens": 100})
}
func current(at time.Time, balance string) map[string][]json.RawMessage {
	return map[string][]json.RawMessage{"user": {raw(map[string]any{"id": 1, "email": "test@example.invalid", "created_at": at.Add(-time.Hour).Format(time.RFC3339Nano), "updated_at": at.Format(time.RFC3339Nano), "balance": json.Number(balance), "frozen_balance": 0})}, "key": {raw(map[string]any{"id": 2, "user_id": 1, "created_at": at.Add(-time.Hour).Format(time.RFC3339Nano), "quota": 10, "quota_used": 0})}, "subscription": {}}
}
func TestMoneyPrecision(t *testing.T) {
	for _, s := range []string{"0.0000000001", "-1.2345678901", "15.0000000000"} {
		m, e := ParseMoney(s)
		if e != nil || m.String() != s {
			t.Fatal(s, m, e)
		}
	}
	if _, e := ParseMoney("1.00000000001"); e == nil {
		t.Fatal("precision accepted")
	}
	if units(raw(json.Number("0.00000000015"))).String() != "0.0000000002" {
		t.Fatal("rounding")
	}
}
func TestUpsertRollbackAndSourceIsolation(t *testing.T) {
	d := testDB(t)
	ctx := t.Context()
	at := time.Now().Add(-time.Hour)
	b := Batch{Rows: []json.RawMessage{usage(1, at, "0.1234567891")}, Next: Cursor{At: at.Format(time.RFC3339Nano), ID: 1}}
	for i := 0; i < 3; i++ {
		if e := d.SaveBatch(ctx, "s", "usage", b); e != nil {
			t.Fatal(e)
		}
	}
	var n, cost int64
	d.db.QueryRow("SELECT requests,cost FROM audit_daily WHERE source='s'").Scan(&n, &cost)
	if n != 1 || Money(cost).String() != "0.1234567891" {
		t.Fatal(n, cost)
	}
	b.Rows = []json.RawMessage{usage(1, at.Add(24*time.Hour), "2.0000000000")}
	if e := d.SaveBatch(ctx, "s", "usage", b); e != nil {
		t.Fatal(e)
	}
	d.db.QueryRow("SELECT sum(requests),sum(cost) FROM audit_daily WHERE source='s'").Scan(&n, &cost)
	if n != 1 || Money(cost).String() != "2.0000000000" {
		t.Fatal(n, cost)
	}
	b.Rows = append(b.Rows, json.RawMessage(`{"id":9}`))
	b.Next.ID = 9
	if e := d.SaveBatch(ctx, "s", "usage", b); e == nil {
		t.Fatal("invalid batch committed")
	}
	cur, _ := d.Cursor("s", "usage")
	if cur.ID != 1 {
		t.Fatal(cur)
	}
	if e := d.SaveBatch(ctx, "other", "usage", Batch{Rows: []json.RawMessage{usage(1, at, "8")}}); e != nil {
		t.Fatal(e)
	}
	d.db.QueryRow("SELECT sum(cost) FROM audit_daily WHERE source='s'").Scan(&cost)
	if Money(cost).String() != "2.0000000000" {
		t.Fatal("mixed sources")
	}
}

type fakeSource struct {
	rows  map[string][]json.RawMessage
	cur   map[string][]json.RawMessage
	now   time.Time
	fail  bool
	reads int
}

func (f *fakeSource) Test(context.Context, Config, Secrets) (map[string]any, error) { return nil, nil }
func (f *fakeSource) Current(context.Context, Config, Secrets) (map[string][]json.RawMessage, int64, error) {
	return f.cur, f.now.Unix(), nil
}
func (f *fakeSource) Read(ctx context.Context, c Config, s Secrets, entity string, cur Cursor, end time.Time) (Batch, error) {
	f.reads++
	if f.fail {
		return Batch{}, errors.New("source offline")
	}
	if e := ctx.Err(); e != nil {
		return Batch{}, e
	}
	out := Batch{Next: cur}
	from, _ := time.Parse(time.RFC3339Nano, cur.At)
	for _, r := range f.rows[entity] {
		m := decodeMap(r)
		at, _ := time.Parse(time.RFC3339Nano, str(m, "created_at"))
		id := integer(m, "id")
		if at.Before(from) || !at.Before(end) || (at.Equal(from) && id <= cur.ID) {
			continue
		}
		out.Rows = append(out.Rows, r)
		out.Next = Cursor{At: at.Format(time.RFC3339Nano), ID: id}
		if len(out.Rows) >= c.BatchSize {
			break
		}
	}
	out.Complete = len(out.Rows) < c.BatchSize
	if out.Complete {
		out.Next = Cursor{At: end.Format(time.RFC3339Nano), Through: end.Unix()}
	}
	return out, nil
}
func TestIncrementalFairnessResumeOverlapAndFailure(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	c.DBHost = "test"
	c.DBName = "test"
	c.DBUser = "read"
	c.BatchSize = 100
	c.MaxBatches = 1
	c.PauseMS = 0
	c.SegmentMinutes = 60
	c.OverlapMinutes = 60
	now := time.Unix(dayStart(time.Now().Unix()), 0).Add(5 * time.Minute)
	at := now.Add(-4 * time.Minute)
	f := &fakeSource{rows: map[string][]json.RawMessage{}, cur: current(at, "10"), now: now}
	for i := int64(1); i <= 205; i++ {
		f.rows["usage"] = append(f.rows["usage"], usage(i, at, "0.0000000001"))
	}
	e := NewEngine(d, f, nil)
	e.now = func() time.Time { return now }
	for i := 0; i < 15; i++ {
		if err := e.Run(t.Context(), c, Secrets{}); err != nil {
			t.Fatal(err)
		}
		v, _ := d.State(c.SourceID())
		if v.Coverage == "complete" {
			break
		}
	}
	v, _ := d.State(c.SourceID())
	if v.Coverage != "complete" {
		t.Fatal("one-batch scheduler starved", v)
	}
	var n int
	d.db.QueryRow("SELECT count(*) FROM audit_records WHERE entity='usage'").Scan(&n)
	if n != 205 {
		t.Fatal(n)
	}
	f.rows["usage"] = append(f.rows["usage"], usage(206, at, "1"))
	now = now.Add(time.Minute)
	f.now = now
	for i := 0; i < 15; i++ {
		if err := e.Run(t.Context(), c, Secrets{}); err != nil {
			t.Fatal(err)
		}
		v, _ = d.State(c.SourceID())
		if v.Coverage == "complete" {
			break
		}
	}
	d.db.QueryRow("SELECT sum(requests) FROM audit_daily").Scan(&n)
	if n != 206 {
		t.Fatal("overlap lost or duplicated", n)
	}
	before, _ := d.Cursor(c.SourceID(), "usage")
	f.fail = true
	now = now.Add(time.Minute)
	err := e.Run(t.Context(), c, Secrets{})
	after, _ := d.Cursor(c.SourceID(), "usage")
	if err == nil || before != after {
		t.Fatal("failed cursor advanced", err, before, after)
	}
	v, _ = d.State(c.SourceID())
	if v.Error == "" {
		t.Fatal("failure hidden")
	}
}
func TestIndependentBalanceAndPartialCoverage(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	c.DBHost = "test"
	c.CheckBilling = false
	c.CheckCharges = false
	c.CheckQuota = false
	source := c.SourceID()
	now := time.Now()
	at := now.Add(-time.Hour)
	ctx := t.Context()
	if e := d.SaveCurrent(ctx, source, current(at.Add(-time.Minute), "10"), at.Unix(), at.Unix()); e != nil {
		t.Fatal(e)
	}
	if e := d.SaveBatch(ctx, source, "usage", Batch{Rows: []json.RawMessage{usage(1, at.Add(time.Minute), "2")}}); e != nil {
		t.Fatal(e)
	}
	if e := d.SaveCurrent(ctx, source, current(at.Add(2*time.Minute), "8"), at.Add(10*time.Minute).Unix(), at.Add(9*time.Minute).Unix()); e != nil {
		t.Fatal(e)
	}
	d.SaveState(Status{Source: source, Start: dayStart(now.Unix()), Through: now.Add(-3 * time.Minute).Unix(), Coverage: "complete"})
	a, b, _ := Period("day", "", "", "", now)
	r, err := d.Report(ctx, c, a, b, "", "", 1, 20)
	if err != nil || len(r.Rows) != 1 || r.Rows[0].State != "consistent" {
		t.Fatal(r, err)
	}
	if e := d.SaveCurrent(ctx, source, current(at.Add(11*time.Minute), "9"), at.Add(20*time.Minute).Unix(), at.Add(19*time.Minute).Unix()); e != nil {
		t.Fatal(e)
	}
	r, err = d.Report(ctx, c, a, b, "", "", 1, 20)
	if err != nil || r.Rows[0].State != "review" || *r.Rows[0].Difference != "1.0000000000" {
		t.Fatal(r, err)
	}
	d.SaveState(Status{Source: source, Start: dayStart(now.Unix()), Error: "offline"})
	r, err = d.Report(ctx, c, a, b, "", "", 1, 20)
	if err != nil || r.Rows[0].State == "consistent" {
		t.Fatal("offline shown healthy", r, err)
	}
}
func TestBillingBothDirectionsArchiveAndUnknown(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	c.DBHost = "test"
	c.CheckBalance = false
	source := c.SourceID()
	now := time.Now()
	at := now.Add(-time.Hour)
	ctx := t.Context()
	d.SaveCurrent(ctx, source, current(at, "0"), now.Unix(), 0)
	d.SaveBatch(ctx, source, "usage", Batch{Rows: []json.RawMessage{usage(1, at, "1"), usage(2, at, "1"), usage(3, at, "1")}})
	d.SaveBatch(ctx, source, "billing", Batch{Rows: []json.RawMessage{raw(map[string]any{"id": 1, "api_key_id": 2, "request_id": "req-1", "created_at": at.Format(time.RFC3339Nano)}), raw(map[string]any{"id": 9, "api_key_id": 2, "request_id": "orphan", "created_at": at.Format(time.RFC3339Nano)})}})
	d.db.Exec("INSERT INTO audit_verifications VALUES(?,?,?,?,?,?)", source, "usage", 2, "req-2", 1, now.Unix())
	d.SaveState(Status{Source: source, Start: dayStart(at.Unix()), Through: now.Add(-3 * time.Minute).Unix(), Coverage: "complete"})
	r, e := d.Report(ctx, c, dayStart(at.Unix()), dayStart(now.Unix())+86400, "", "", 1, 20)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Rows) != 1 || r.Rows[0].Issues != 2 {
		t.Fatal(r)
	}
}
func TestFundingAndPrivacyProjection(t *testing.T) {
	for entity, fields := range projections {
		for _, forbidden := range []string{"password", "access_token", "refresh_token", "request_body", "ip_address", "full_prompt"} {
			if strings.Contains(fields, forbidden) {
				t.Fatal(entity, forbidden)
			}
		}
	}
	r, e := parseRecord("affiliate", raw(map[string]any{"id": 1, "user_id": 1, "action": "transfer", "amount": json.Number("1.23"), "created_at": time.Now().Format(time.RFC3339Nano)}))
	if e != nil || r.credit.String() != "1.2300000000" {
		t.Fatal(r, e)
	}
	r, e = parseRecord("affiliate", raw(map[string]any{"id": 1, "user_id": 1, "action": "commission", "amount": 10, "created_at": time.Now().Format(time.RFC3339Nano)}))
	if e != nil || r.credit != 0 {
		t.Fatal("unwithdrawn affiliate counted as wallet", r, e)
	}
}
func TestPruningKeepsRollups(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	now := time.Now()
	source := c.SourceID()
	d.SaveBatch(t.Context(), source, "usage", Batch{Rows: []json.RawMessage{usage(1, now.AddDate(0, 0, -40), "1")}})
	if e := d.Prune(t.Context(), c, source, now.Unix()); e != nil {
		t.Fatal(e)
	}
	var n int
	d.db.QueryRow("SELECT count(*) FROM audit_records").Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	d.db.QueryRow("SELECT sum(requests) FROM audit_daily").Scan(&n)
	if n != 1 {
		t.Fatal("rollup lost")
	}
}

func TestBalanceBackupFilterIntervalHistoryAndPrune(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	at := time.Now()
	objects := current(at, "12.34")
	objects["user"][0] = raw(map[string]any{"id": 1, "email": "live@example.invalid", "status": "active", "balance": json.Number("12.3400000001")})
	for i, entry := range []struct {
		status  string
		balance int
	}{{"disabled", 10}, {"active", 0}, {"active", -1}} {
		objects["user"] = append(objects["user"], raw(map[string]any{"id": i + 2, "status": entry.status, "balance": entry.balance}))
	}
	for _, sec := range []int64{0, 30} {
		if e := d.BackupBalances(t.Context(), c, objects, at.Unix()+sec); e != nil {
			t.Fatal(e)
		}
	}
	v, e := d.Balances(t.Context(), c, 0, "")
	if e != nil || v["total"] != 1 || v["balance"] != "12.3400000001" || len(v["runs"].([]map[string]any)) != 1 {
		t.Fatal(v, e)
	}
	objects["user"][0] = raw(map[string]any{"id": 1, "email": "live@example.invalid", "status": "active", "balance": 20})
	if e = d.BackupBalances(t.Context(), c, objects, at.Unix()+3600); e != nil {
		t.Fatal(e)
	}
	v, e = d.Balances(t.Context(), c, at.Unix(), "")
	if e != nil || v["balance"] != "12.3400000001" {
		t.Fatal("historical backup changed", v, e)
	}
	if e = d.Prune(t.Context(), c, c.SourceID(), at.AddDate(0, 0, c.BackupRetentionDays+2).Unix()); e != nil {
		t.Fatal(e)
	}
	v, e = d.Balances(t.Context(), c, 0, "")
	if e != nil || v["total"] != 0 {
		t.Fatal(v, e)
	}
}
