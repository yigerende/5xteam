package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/model"
)

const MaxProxyBindingCount int64 = 1000000000

var ErrSub2ProxyBindingLimit = errors.New("代理累计绑定次数已达到上限，未提交推送")
var ErrSub2ProxyUsageChanged = errors.New("累计次数已变化，请刷新后再修改")

type Sub2ProxyUsage struct {
	Count    int64  `json:"cumulative_bindings"`
	Pending  int64  `json:"pending_bindings"`
	Revision int64  `json:"binding_revision"`
	Since    string `json:"binding_since"`
}

func (s *Store) Sub2ProxyBindingLimit(endpoint string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var limit int64
	err := s.db.QueryRow("SELECT max_bindings FROM sub2_proxy_binding_policy WHERE endpoint=?", endpoint).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return limit, err
}

func (s *Store) SaveSub2ProxyBindingLimit(endpoint string, limit int64) error {
	if endpoint == "" || limit < 0 || limit > MaxProxyBindingCount {
		return errors.New("累计绑定上限须为 0 到 1000000000 的整数，0 表示不限制")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("INSERT INTO sub2_proxy_binding_policy(endpoint,max_bindings) VALUES(?,?) ON CONFLICT(endpoint) DO UPDATE SET max_bindings=excluded.max_bindings", endpoint, limit)
	return err
}

// The first observation supplies a known baseline; deletion/reimport and later
// changes in the current account count never reset the cumulative count.
func (s *Store) Sub2ProxyUsages(endpoint string, baselines map[string]int64) (map[string]Sub2ProxyUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, count := range baselines {
		if key == "" || count < 0 {
			continue
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO sub2_proxy_binding_usage(endpoint,proxy_key,binding_count,created_at,updated_at) VALUES(?,?,?,?,?)", endpoint, key, count, now, now); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(`SELECT u.proxy_key,u.binding_count,u.revision,u.created_at,(SELECT count(*) FROM sub2_proxy_binding_events e WHERE e.endpoint=u.endpoint AND e.proxy_key=u.proxy_key AND e.counted=0) FROM sub2_proxy_binding_usage u WHERE u.endpoint=?`, endpoint)
	if err != nil {
		return nil, err
	}
	out := map[string]Sub2ProxyUsage{}
	for rows.Next() {
		var key string
		var usage Sub2ProxyUsage
		if err = rows.Scan(&key, &usage.Count, &usage.Revision, &usage.Since, &usage.Pending); err != nil {
			rows.Close()
			return nil, err
		}
		out[key] = usage
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *Store) SetSub2ProxyUsage(endpoint, key string, count, revision int64) error {
	if key == "" || count < 0 || count > MaxProxyBindingCount || revision < 0 {
		return errors.New("累计次数须为 0 到 1000000000 的整数")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old, actual int64
	if err = tx.QueryRow("SELECT binding_count,revision FROM sub2_proxy_binding_usage WHERE endpoint=? AND proxy_key=?", endpoint, key).Scan(&old, &actual); err != nil {
		return err
	}
	if revision != actual {
		return ErrSub2ProxyUsageChanged
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec("UPDATE sub2_proxy_binding_usage SET binding_count=?,revision=revision+1,updated_at=? WHERE endpoint=? AND proxy_key=?", count, now, endpoint, key); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO sub2_proxy_binding_edits(endpoint,proxy_key,previous_count,new_count,created_at) VALUES(?,?,?,?,?)", endpoint, key, old, count, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Reserve quota and the encrypted idempotent request in one transaction.
// Unknown outcomes keep the reservation across process restarts.
func (s *Store) ReserveSub2ProxyBinding(id, payload, endpoint, key, attempt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	encrypted, err := s.encrypt(payload)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var limit, count, pending int64
	err = tx.QueryRow("SELECT max_bindings FROM sub2_proxy_binding_policy WHERE endpoint=?", endpoint).Scan(&limit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = tx.QueryRow("SELECT binding_count FROM sub2_proxy_binding_usage WHERE endpoint=? AND proxy_key=?", endpoint, key).Scan(&count); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT count(*) FROM sub2_proxy_binding_events WHERE endpoint=? AND proxy_key=? AND counted=0", endpoint, key).Scan(&pending); err != nil {
		return err
	}
	if limit > 0 && count+pending >= limit {
		return ErrSub2ProxyBindingLimit
	}
	if _, err = tx.Exec("INSERT INTO sub2_proxy_binding_events(attempt_key,endpoint,proxy_key,created_at) VALUES(?,?,?,?)", attempt, endpoint, key, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO sub2_push_attempts(id,encrypted_payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET encrypted_payload=excluded.encrypted_payload", id, encrypted); err != nil {
		return err
	}
	return tx.Commit()
}

// Commit a successful binding once with its saved response. The remote account
// uniqueness constraint also prevents counting the same create under two keys.
func (s *Store) CompleteSub2ProxyBinding(id, payload, attempt string, remoteID int64, bindings ...model.Sub2ProxyBinding) error {
	if remoteID <= 0 {
		return errors.New("Sub2 未返回有效账号 ID，保留原推送等待确认")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	encrypted, err := s.encrypt(payload)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var endpoint, key string
	var counted int
	err = tx.QueryRow("SELECT endpoint,proxy_key,counted FROM sub2_proxy_binding_events WHERE attempt_key=?", attempt).Scan(&endpoint, &key, &counted)
	// Legacy pending requests predate this counter and retain their original
	// request. Their existing downstream bindings are covered by the baseline.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && counted == 0 {
		var previous int
		if err = tx.QueryRow("SELECT count(*) FROM sub2_proxy_binding_events WHERE endpoint=? AND remote_id=? AND counted=1", endpoint, remoteID).Scan(&previous); err != nil {
			return err
		}
		if previous == 0 {
			if _, err = tx.Exec("UPDATE sub2_proxy_binding_usage SET binding_count=binding_count+1,revision=revision+1,updated_at=? WHERE endpoint=? AND proxy_key=?", time.Now().UTC().Format(time.RFC3339Nano), endpoint, key); err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE sub2_proxy_binding_events SET remote_id=?,counted=1 WHERE attempt_key=?", remoteID, attempt); err != nil {
				return err
			}
		} else {
			if _, err = tx.Exec("UPDATE sub2_proxy_binding_events SET counted=1 WHERE attempt_key=?", attempt); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("INSERT INTO sub2_push_attempts(id,encrypted_payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET encrypted_payload=excluded.encrypted_payload", id, encrypted); err != nil {
		return err
	}
	for _, binding := range bindings {
		if err = saveSub2ProxyBindingTx(tx, binding); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) RejectSub2ProxyBinding(id, attempt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM sub2_proxy_binding_events WHERE attempt_key=? AND counted=0", attempt); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sub2_push_attempts WHERE id=?", id); err != nil {
		return fmt.Errorf("清理原推送: %w", err)
	}
	return tx.Commit()
}
