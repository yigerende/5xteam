package clientaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// The independent source snapshot is already in memory. Creating a backup never
// sends additional SQL to sub2api and never derives balances from usage totals.
func (d *Database) BackupBalances(ctx context.Context, c Config, objects map[string][]json.RawMessage, at int64) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var last sql.NullInt64
	if err = tx.QueryRow("SELECT max(at) FROM audit_backup_runs WHERE source=?", c.SourceID()).Scan(&last); err != nil {
		return err
	}
	if last.Valid && at-last.Int64 < int64(c.BackupIntervalSeconds) {
		return nil
	}
	count := 0
	var total Money
	for _, raw := range objects["user"] {
		m := decodeMap(raw)
		bal := units(m["balance"])
		if str(m, "status") != "active" || bal <= 0 || str(m, "deleted_at") != "" {
			continue
		}
		_, err = tx.Exec("INSERT INTO audit_balance_backups VALUES(?,?,?,?,?,?,?)", c.SourceID(), at, integer(m, "id"), str(m, "email"), bal, units(m["frozen_balance"]), units(m["total_recharged"]))
		if err != nil {
			return err
		}
		count++
		total += bal
	}
	if _, err = tx.Exec("INSERT INTO audit_backup_runs VALUES(?,?,?,?)", c.SourceID(), at, count, total); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Database) Balances(ctx context.Context, c Config, at int64, query string) (map[string]any, error) {
	runs := []map[string]any{}
	rows, err := d.db.QueryContext(ctx, "SELECT at,users,total FROM audit_backup_runs WHERE source=? ORDER BY at DESC LIMIT 200", c.SourceID())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ts int64
		var count int
		var total Money
		if err = rows.Scan(&ts, &count, &total); err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, map[string]any{"at": ts, "users": count, "balance": total.String()})
		if at == 0 {
			at = ts
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	if at == 0 {
		return map[string]any{"at": 0, "items": items, "runs": runs, "total": 0, "balance": "0"}, nil
	}
	var count int
	var total Money
	err = d.db.QueryRowContext(ctx, "SELECT users,total FROM audit_backup_runs WHERE source=? AND at=?", c.SourceID(), at).Scan(&count, &total)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("备份不存在或已超过保留期")
	}
	if err != nil {
		return nil, err
	}
	rows, err = d.db.QueryContext(ctx, "SELECT user_id,email,balance,frozen,recharged FROM audit_balance_backups WHERE source=? AND at=? AND (?='' OR instr(lower(email),lower(?))>0 OR cast(user_id AS TEXT)=?) ORDER BY balance DESC,user_id", c.SourceID(), at, query, query, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var email string
		var bal, frozen, recharged Money
		if err = rows.Scan(&id, &email, &bal, &frozen, &recharged); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "email": email, "balance": bal.String(), "frozen": frozen.String(), "recharged": recharged.String()})
	}
	return map[string]any{"at": at, "items": items, "runs": runs, "total": count, "balance": total.String()}, rows.Err()
}
