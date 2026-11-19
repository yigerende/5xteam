package clientaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Candidate struct {
	Entity  string `json:"entity"`
	Key     int64  `json:"api_key_id"`
	Request string `json:"request_id"`
	Found   bool   `json:"found"`
}
type verifier interface {
	Verify(context.Context, Config, Secrets, []Candidate) ([]Candidate, error)
}

// Only indexed, exact request/key lookups are issued. Archive matches explain
// legitimate dedup records which have left the live table.
func (Postgres) Verify(ctx context.Context, c Config, s Secrets, items []Candidate) ([]Candidate, error) {
	conn, tx, err := connect(ctx, c, s)
	if err != nil {
		return nil, err
	}
	defer closeSource(conn, tx)
	for i := range items {
		q := "SELECT EXISTS(SELECT 1 FROM usage_logs WHERE request_id=$1 AND api_key_id=$2)"
		if items[i].Entity == "usage" {
			q = "SELECT EXISTS(SELECT 1 FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2) OR EXISTS(SELECT 1 FROM usage_billing_dedup_archive WHERE request_id=$1 AND api_key_id=$2)"
		}
		if err = tx.QueryRow(ctx, q, items[i].Request, items[i].Key).Scan(&items[i].Found); err != nil {
			return nil, sourceError(err)
		}
	}
	return items, nil
}

func (e *Engine) verifyCandidates(ctx context.Context, c Config, s Secrets, through int64) error {
	v, ok := e.source.(verifier)
	if !ok || !c.CheckBilling {
		return nil
	}
	rows, err := e.DB.db.QueryContext(ctx, `SELECT r.entity,r.key_id,r.request_id FROM audit_records r LEFT JOIN audit_verifications v ON v.source=r.source AND v.entity=r.entity AND v.key_id=r.key_id AND v.request_id=r.request_id WHERE r.source=? AND r.entity IN('usage','billing') AND r.request_id<>'' AND r.at<? AND (v.checked IS NULL OR v.checked<?) AND NOT EXISTS(SELECT 1 FROM audit_records o WHERE o.source=r.source AND o.entity=CASE r.entity WHEN 'usage' THEN 'billing' ELSE 'usage' END AND o.key_id=r.key_id AND o.request_id=r.request_id) ORDER BY coalesce(v.checked,0),r.at LIMIT 50`, c.SourceID(), through-int64(c.SettlementSeconds), e.now().Unix()-3600)
	if err != nil {
		return err
	}
	items := []Candidate{}
	for rows.Next() {
		var x Candidate
		if err = rows.Scan(&x.Entity, &x.Key, &x.Request); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(items) == 0 {
		return err
	}
	qctx, cancel := context.WithTimeout(ctx, time.Duration(c.QuerySeconds+2)*time.Second)
	defer cancel()
	items, err = v.Verify(qctx, c, s, items)
	if err != nil {
		return err
	}
	tx, err := e.DB.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, x := range items {
		if _, err = tx.Exec(`INSERT INTO audit_verifications VALUES(?,?,?,?,?,?) ON CONFLICT(source,entity,key_id,request_id) DO UPDATE SET found=excluded.found,checked=excluded.checked`, c.SourceID(), x.Entity, x.Key, x.Request, x.Found, e.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) Progress(ctx context.Context, c Config) (map[string]any, error) {
	tx, err := d.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cursors := map[string]Cursor{}
	for _, entity := range entities {
		cursors[entity] = Cursor{}
	}
	rows, err := tx.QueryContext(ctx, "SELECT entity,payload FROM audit_cursors WHERE source=?", c.SourceID())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var entity, payload string
		var cur Cursor
		if err = rows.Scan(&entity, &payload); err == nil {
			err = json.Unmarshal([]byte(payload), &cur)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		cursors[entity] = cur
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT started,finished,rows_count,message FROM audit_runs WHERE source=? ORDER BY id DESC LIMIT 30", c.SourceID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var start, end, n int64
		var msg string
		if err = rows.Scan(&start, &end, &n, &msg); err != nil {
			return nil, err
		}
		runs = append(runs, map[string]any{"started": start, "finished": end, "rows": n, "message": msg})
	}
	return map[string]any{"cursors": cursors, "runs": runs}, rows.Err()
}

// Validate the schema before syncing, using the same safe projections as reads.
func schemaQueries() []string {
	out := []string{}
	for entity, table := range tables {
		out = append(out, fmt.Sprintf("SELECT %s FROM %s LIMIT 0", projections[entity], table))
	}
	return append(out, "SELECT request_id,api_key_id FROM usage_billing_dedup_archive LIMIT 0")
}

func decodeMap(raw []byte) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}
