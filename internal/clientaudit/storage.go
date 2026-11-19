package clientaudit

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Database struct {
	db   *sql.DB
	path string
}

func OpenDatabase(path string) (*Database, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	dsn := "file:" + strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(path)) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, e := sql.Open("sqlite", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	d := &Database{db, path}
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS audit_records(source TEXT NOT NULL,entity TEXT NOT NULL,id INTEGER NOT NULL,at INTEGER NOT NULL,user_id INTEGER NOT NULL DEFAULT 0,key_id INTEGER NOT NULL DEFAULT 0,request_id TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',cost INTEGER NOT NULL DEFAULT 0,raw_cost INTEGER NOT NULL DEFAULT 0,balance_cost INTEGER NOT NULL DEFAULT 0,credit INTEGER NOT NULL DEFAULT 0,flags TEXT NOT NULL DEFAULT '',payload BLOB NOT NULL,PRIMARY KEY(source,entity,id));
 CREATE INDEX IF NOT EXISTS audit_records_time ON audit_records(source,entity,at,user_id);
 CREATE INDEX IF NOT EXISTS audit_records_request ON audit_records(source,entity,key_id,request_id);
 CREATE INDEX IF NOT EXISTS audit_records_user ON audit_records(source,user_id,at);
 CREATE TABLE IF NOT EXISTS audit_daily(source TEXT NOT NULL,day INTEGER NOT NULL,user_id INTEGER NOT NULL,requests INTEGER NOT NULL DEFAULT 0,cost INTEGER NOT NULL DEFAULT 0,raw_cost INTEGER NOT NULL DEFAULT 0,balance_cost INTEGER NOT NULL DEFAULT 0,credit INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(source,day,user_id));
 CREATE INDEX IF NOT EXISTS audit_daily_user ON audit_daily(source,user_id,day);
 CREATE TABLE IF NOT EXISTS audit_cursors(source TEXT NOT NULL,entity TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(source,entity));
 CREATE TABLE IF NOT EXISTS audit_state(source TEXT PRIMARY KEY,payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS audit_snapshots(source TEXT NOT NULL,at INTEGER NOT NULL,user_id INTEGER NOT NULL,balance INTEGER NOT NULL,frozen INTEGER NOT NULL,recharged INTEGER NOT NULL,cost INTEGER NOT NULL,credit INTEGER NOT NULL,complete INTEGER NOT NULL,PRIMARY KEY(source,at,user_id));
 CREATE INDEX IF NOT EXISTS audit_snapshots_user ON audit_snapshots(source,user_id,at);
 CREATE TABLE IF NOT EXISTS audit_balance_backups(source TEXT NOT NULL,at INTEGER NOT NULL,user_id INTEGER NOT NULL,email TEXT NOT NULL,balance INTEGER NOT NULL,frozen INTEGER NOT NULL,recharged INTEGER NOT NULL,PRIMARY KEY(source,at,user_id));
 CREATE TABLE IF NOT EXISTS audit_backup_runs(source TEXT NOT NULL,at INTEGER NOT NULL,users INTEGER NOT NULL,total INTEGER NOT NULL,PRIMARY KEY(source,at));
 CREATE TABLE IF NOT EXISTS audit_pruning(source TEXT PRIMARY KEY,before INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS audit_verifications(source TEXT NOT NULL,entity TEXT NOT NULL,key_id INTEGER NOT NULL,request_id TEXT NOT NULL,found INTEGER NOT NULL,checked INTEGER NOT NULL,PRIMARY KEY(source,entity,key_id,request_id));
 CREATE TABLE IF NOT EXISTS audit_object_snapshots(source TEXT NOT NULL,entity TEXT NOT NULL,id INTEGER NOT NULL,at INTEGER NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(source,entity,id,at));
 CREATE INDEX IF NOT EXISTS audit_object_time ON audit_object_snapshots(source,entity,at);
 CREATE TABLE IF NOT EXISTS audit_runs(id INTEGER PRIMARY KEY,source TEXT NOT NULL,started INTEGER NOT NULL,finished INTEGER NOT NULL,rows_count INTEGER NOT NULL,message TEXT NOT NULL);
 `)
	if e != nil {
		db.Close()
		return nil, e
	}
	return d, nil
}
func (d *Database) Close() error { return d.db.Close() }
func pack(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	w, _ := zlib.NewWriterLevel(&b, zlib.BestSpeed)
	_, e := w.Write(raw)
	if e != nil {
		return nil, e
	}
	if e = w.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func unpack(raw []byte) (json.RawMessage, error) {
	r, e := zlib.NewReader(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, 2<<20))
	return b, e
}
func units(raw json.RawMessage) Money {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	s := strings.Trim(string(raw), "\"")
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0
	}
	r.Mul(r, big.NewRat(10000000000, 1))
	neg := r.Sign() < 0
	if neg {
		r.Neg(r)
	}
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if rem.Lsh(rem, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return Money(q.Int64())
}
func integer(m map[string]json.RawMessage, k string) int64 {
	var n int64
	_ = json.Unmarshal(m[k], &n)
	return n
}
func str(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}

type record struct {
	id, at, user, key          int64
	request, model, flags      string
	cost, raw, balance, credit Money
	payload                    []byte
}

func parseRecord(entity string, raw json.RawMessage) (record, error) {
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e != nil {
		return record{}, e
	}
	r := record{id: integer(m, "id"), user: integer(m, "user_id"), key: integer(m, "api_key_id"), request: str(m, "request_id"), model: str(m, "model")}
	if r.id <= 0 {
		return r, errors.New("源记录缺少ID")
	}
	if entity == "audit" {
		r.user = integer(m, "actor_user_id")
	}
	clock := "created_at"
	if entity == "funding" {
		clock = "used_at"
	}
	if entity == "payment" {
		clock = "updated_at"
	}
	t, e := time.Parse(time.RFC3339Nano, str(m, clock))
	if e != nil {
		return r, e
	}
	r.at = t.Unix()
	if entity == "usage" {
		r.cost = units(m["actual_cost"])
		r.raw = units(m["total_cost"])
		if integer(m, "billing_type") == 0 {
			r.balance = r.cost
		}
		tokens := integer(m, "input_tokens") + integer(m, "output_tokens") + integer(m, "cache_creation_tokens") + integer(m, "cache_read_tokens") + integer(m, "image_count") + integer(m, "video_count")
		if r.cost < 0 || r.raw < 0 || tokens < 0 {
			r.flags += "negative_value,"
		}
		if tokens > 0 && r.cost == 0 && units(m["rate_multiplier"]) > 0 {
			r.flags += "zero_charge_usage,"
		}
		if r.request == "" {
			r.flags += "missing_request_id,"
		}
		a, ok := new(big.Rat).SetString(string(m["actual_cost"]))
		b, ok2 := new(big.Rat).SetString(string(m["total_cost"]))
		rate, ok3 := new(big.Rat).SetString(string(m["rate_multiplier"]))
		if ok && ok2 && ok3 {
			b.Mul(b, rate)
			a.Sub(a, b)
			a.Abs(a)
			if a.Cmp(big.NewRat(1, 1000000)) > 0 {
				r.flags += "price_difference,"
			}
		}
	}
	if entity == "funding" && str(m, "status") == "used" && (str(m, "type") == "balance" || str(m, "type") == "admin_balance") {
		r.credit = units(m["value"])
	}
	if entity == "affiliate" && str(m, "action") == "transfer" {
		r.credit = units(m["amount"])
	}
	if entity == "payment" && units(m["refund_amount"]) != 0 {
		r.flags = "payment_review,"
	}
	r.payload, e = pack(raw)
	return r, e
}
func dayStart(at int64) int64 {
	t := time.Unix(at, 0).In(Beijing)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Beijing).Unix()
}
func adjust(tx *sql.Tx, source, entity string, r record, sign int64) error {
	if entity != "usage" && entity != "funding" && entity != "affiliate" {
		return nil
	}
	n := int64(0)
	if entity == "usage" {
		n = 1
	}
	_, e := tx.Exec(`INSERT INTO audit_daily(source,day,user_id,requests,cost,raw_cost,balance_cost,credit) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(source,day,user_id) DO UPDATE SET requests=requests+excluded.requests,cost=cost+excluded.cost,raw_cost=raw_cost+excluded.raw_cost,balance_cost=balance_cost+excluded.balance_cost,credit=credit+excluded.credit`, source, dayStart(r.at), r.user, n*sign, int64(r.cost)*sign, int64(r.raw)*sign, int64(r.balance)*sign, int64(r.credit)*sign)
	return e
}
func (d *Database) SaveBatch(ctx context.Context, source, entity string, b Batch) error {
	tx, e := d.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var pruned int64
	if x := tx.QueryRow("SELECT before FROM audit_pruning WHERE source=?", source).Scan(&pruned); x != nil && !errors.Is(x, sql.ErrNoRows) {
		return x
	}
	for _, raw := range b.Rows {
		r, e := parseRecord(entity, raw)
		if e != nil {
			return e
		}
		if r.at < pruned && (entity == "usage" || entity == "billing" || entity == "audit") {
			continue
		}
		var old record
		e = tx.QueryRow("SELECT at,user_id,cost,raw_cost,balance_cost,credit,payload FROM audit_records WHERE source=? AND entity=? AND id=?", source, entity, r.id).Scan(&old.at, &old.user, &old.cost, &old.raw, &old.balance, &old.credit, &old.payload)
		if e == nil {
			if bytes.Equal(old.payload, r.payload) {
				continue
			}
			if e = adjust(tx, source, entity, old, -1); e != nil {
				return e
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = tx.Exec(`INSERT INTO audit_records(source,entity,id,at,user_id,key_id,request_id,model,cost,raw_cost,balance_cost,credit,flags,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source,entity,id) DO UPDATE SET at=excluded.at,user_id=excluded.user_id,key_id=excluded.key_id,request_id=excluded.request_id,model=excluded.model,cost=excluded.cost,raw_cost=excluded.raw_cost,balance_cost=excluded.balance_cost,credit=excluded.credit,flags=excluded.flags,payload=excluded.payload`, source, entity, r.id, r.at, r.user, r.key, r.request, r.model, r.cost, r.raw, r.balance, r.credit, r.flags, r.payload)
		if e != nil {
			return e
		}
		if e = adjust(tx, source, entity, r, 1); e != nil {
			return e
		}
	}
	raw, _ := json.Marshal(b.Next)
	_, e = tx.Exec("INSERT INTO audit_cursors VALUES(?,?,?) ON CONFLICT(source,entity) DO UPDATE SET payload=excluded.payload", source, entity, string(raw))
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (d *Database) Cursor(source, entity string) (Cursor, error) {
	var raw string
	e := d.db.QueryRow("SELECT payload FROM audit_cursors WHERE source=? AND entity=?", source, entity).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return Cursor{}, nil
	}
	var c Cursor
	if e == nil {
		e = json.Unmarshal([]byte(raw), &c)
	}
	return c, e
}
func (d *Database) State(source string) (Status, error) {
	v := Status{Source: source}
	var raw string
	e := d.db.QueryRow("SELECT payload FROM audit_state WHERE source=?", source).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e == nil {
		e = json.Unmarshal([]byte(raw), &v)
	}
	if f, err := os.Stat(d.path); err == nil {
		v.DBBytes = f.Size()
	}
	return v, e
}
func (d *Database) SaveState(v Status) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = d.db.Exec("INSERT INTO audit_state VALUES(?,?) ON CONFLICT(source) DO UPDATE SET payload=excluded.payload", v.Source, string(raw))
	return e
}

func (d *Database) SaveCurrent(ctx context.Context, source string, entities map[string][]json.RawMessage, at int64, through int64) error {
	tx, e := d.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, entity := range []string{"user", "key", "subscription"} {
		// Current tables are replaced atomically; history survives in snapshots.
		if _, e = tx.Exec("DELETE FROM audit_records WHERE source=? AND entity=?", source, entity); e != nil {
			return e
		}
		for _, raw := range entities[entity] {
			r, e := parseRecord(entity, raw)
			if e != nil {
				return e
			}
			if entity == "user" {
				r.user = r.id
				m := decodeMap(raw)
				if str(m, "status") == "active" && units(m["balance"]) > 0 && str(m, "deleted_at") == "" {
					r.flags = "active_positive"
				}
			}
			if _, e = tx.Exec("INSERT INTO audit_records(source,entity,id,at,user_id,flags,payload) VALUES(?,?,?,?,?,?,?)", source, entity, r.id, r.at, r.user, r.flags, r.payload); e != nil {
				return e
			}
			if entity == "key" || entity == "subscription" {
				var prior string
				x := tx.QueryRow("SELECT payload FROM audit_object_snapshots WHERE source=? AND entity=? AND id=? ORDER BY at DESC LIMIT 1", source, entity, r.id).Scan(&prior)
				if x != nil && !errors.Is(x, sql.ErrNoRows) {
					return x
				}
				if prior != string(raw) {
					if _, e = tx.Exec("INSERT OR REPLACE INTO audit_object_snapshots VALUES(?,?,?,?,?)", source, entity, r.id, at, string(raw)); e != nil {
						return e
					}
				}
			}
			if entity == "user" && through > 0 {
				var m map[string]json.RawMessage
				_ = json.Unmarshal(raw, &m)
				updated, _ := time.Parse(time.RFC3339Nano, str(m, "updated_at"))
				complete := through > 0 && !updated.IsZero() && updated.Unix() < through
				if !complete {
					continue
				}
				var last int64
				x := tx.QueryRow("SELECT max(at) FROM audit_snapshots WHERE source=? AND user_id=?", source, r.id).Scan(&last)
				if x == nil && at-last < 300 {
					continue
				}
				_, e = tx.Exec(`INSERT OR REPLACE INTO audit_snapshots(source,at,user_id,balance,frozen,recharged,cost,credit,complete) SELECT ?,?,?,?,?,?,coalesce(sum(balance_cost),0),coalesce(sum(credit),0),? FROM audit_daily WHERE source=? AND user_id=?`, source, at, r.id, units(m["balance"]), units(m["frozen_balance"]), units(m["total_recharged"]), complete, source, r.id)
				// SQL column/value arity is intentionally checked by persistence tests.
				if e != nil {
					return e
				}
			}
		}
	}
	return tx.Commit()
}
func (d *Database) Prune(ctx context.Context, c Config, source string, now int64) error {
	// A persistent boundary prevents a later overlap from adding pruned rows to
	// retained daily totals a second time.
	before := dayStart(now) - int64(c.DetailDays-1)*86400
	if _, e := d.db.ExecContext(ctx, "INSERT INTO audit_pruning VALUES(?,?) ON CONFLICT(source) DO UPDATE SET before=max(before,excluded.before)", source, before); e != nil {
		return e
	}
	// Compact records only; daily rollups and independent snapshots have their own retention.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM audit_records WHERE rowid IN(SELECT rowid FROM audit_records WHERE source=? AND entity IN('usage','billing','audit') AND at<? LIMIT 10000)", []any{source, dayStart(now) - int64(c.DetailDays-1)*86400}},
		{"DELETE FROM audit_snapshots WHERE source=? AND at<?", []any{source, now - int64(c.RetentionDays)*86400}},
		{"DELETE FROM audit_balance_backups WHERE source=? AND at<?", []any{source, now - int64(c.BackupRetentionDays)*86400}},
		{"DELETE FROM audit_backup_runs WHERE source=? AND at<?", []any{source, now - int64(c.BackupRetentionDays)*86400}},
		{"DELETE FROM audit_verifications WHERE source=? AND checked<?", []any{source, now - int64(c.DetailDays)*86400}},
		{"DELETE FROM audit_object_snapshots WHERE source=? AND at<?", []any{source, now - int64(c.RetentionDays)*86400}},
		{"DELETE FROM audit_runs WHERE source=? AND started<?", []any{source, now - int64(c.RetentionDays)*86400}},
	} {
		if _, e := d.db.ExecContext(ctx, q.sql, q.args...); e != nil {
			return e
		}
	}
	return nil
}
