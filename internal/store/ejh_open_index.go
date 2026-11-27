package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

// Non-secret lookup fields are maintained with the encrypted operation. Backfill
// once on upgrade; allocation and preparation never decrypt the whole history.
func (s *Store) initializeEJHOpenIndex() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS ejh_open_index(id TEXT PRIMARY KEY,status TEXT NOT NULL,task_id TEXT NOT NULL,email TEXT NOT NULL COLLATE NOCASE,created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS ejh_open_index_pending ON ejh_open_index(status);
CREATE INDEX IF NOT EXISTS ejh_open_index_created ON ejh_open_index(created_at);
CREATE INDEX IF NOT EXISTS ejh_open_index_task ON ejh_open_index(task_id);
CREATE TABLE IF NOT EXISTS ejh_open_index_version(id INTEGER PRIMARY KEY)`)
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM ejh_open_index_version WHERE id=1)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		rows, e := tx.Query(`SELECT id,sealed FROM ejh_records WHERE kind='operation'`)
		if e != nil {
			return e
		}
		type record struct {
			id string
			op ejh.Operation
		}
		var all []record
		for rows.Next() {
			var id, sealed string
			if err = rows.Scan(&id, &sealed); err != nil {
				break
			}
			var raw string
			if raw, err = s.decrypt(sealed); err != nil {
				break
			}
			var op ejh.Operation
			if err = json.Unmarshal([]byte(raw), &op); err != nil {
				break
			}
			all = append(all, record{id, op})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, rec := range all {
			if err = saveEJHOpenIndex(tx, rec.id, rec.op); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`INSERT INTO ejh_open_index_version VALUES(1)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func saveEJHOpenIndex(tx *sql.Tx, id string, op ejh.Operation) error {
	if op.Method != "open" {
		_, err := tx.Exec(`DELETE FROM ejh_open_index WHERE id=?`, id)
		return err
	}
	_, err := tx.Exec(`INSERT INTO ejh_open_index VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,task_id=excluded.task_id,email=excluded.email,created_at=excluded.created_at`, id, op.Status, op.RestockTaskID, op.AccountEmail, formatTime(op.CreatedAt))
	return err
}

func (s *Store) checkEJHOpenOwner(q proCapacityQuery, email, task string) error {
	var id string
	err := q.QueryRow(`SELECT i.id FROM ejh_open_index i LEFT JOIN pro_restock_tasks t ON t.id=i.task_id
WHERE i.status IN ('submitting','unknown','import_pending') AND (
 (?<>'' AND (i.email=? OR t.email=?)) OR (?<>'' AND i.task_id=?)
 OR (?='' AND ?='' AND i.email='' AND i.task_id='')) LIMIT 1`, email, email, email, task, task, email, task).Scan(&id)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("本账号原开卡 %s 尚未确认，已隔离原任务，其他账号可继续补货", id)
}

func (s *Store) CheckEJHOpenOwner(email, task string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkEJHOpenOwner(s.db, email, task)
}

func (s *Store) EJHRecentOpenOperations(since time.Time) ([]ejh.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT r.sealed FROM ejh_open_index i JOIN ejh_records r ON r.kind='operation' AND r.id=i.id WHERE i.created_at>=?`, formatTime(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ejh.Operation
	for rows.Next() {
		var sealed string
		if err = rows.Scan(&sealed); err != nil {
			return nil, err
		}
		raw, e := s.decrypt(sealed)
		if e != nil {
			return nil, e
		}
		var op ejh.Operation
		if err = json.Unmarshal([]byte(raw), &op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
