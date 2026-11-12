package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

func saveSub2ProxyBindingTx(tx *sql.Tx, b model.Sub2ProxyBinding) error {
	if b.ID == "" || b.TargetID == "" || b.AccountID <= 0 || b.ProxyID <= 0 || b.BoundAt.IsZero() {
		return errors.New("代理绑定记录不完整")
	}
	b.State = "verifying"
	b.NextAt = time.Now().UnixMilli()
	b.Revision = 0
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	// The same downstream account can be returned after a lost response. Never
	// reset the original deadline or resurrect an already completed release.
	_, err = tx.Exec(`INSERT OR IGNORE INTO sub2_proxy_release(id,endpoint,remote_id,profile,next_at,claim_until,revision) VALUES(?,?,?,?,?,0,0)`, b.ID, b.Endpoint, b.AccountID, string(raw), b.NextAt)
	return err
}

func (s *Store) Sub2ProxyBindings(ids []string) (map[string]model.Sub2ProxyBinding, error) {
	out := map[string]model.Sub2ProxyBinding{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 500 {
		return nil, errors.New("一次最多读取 500 个代理绑定")
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	// This is a projection only: no secret decryption and no upstream I/O.
	rows, err := s.db.Query(`SELECT id,profile FROM sub2_proxy_release WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		var b model.Sub2ProxyBinding
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &b); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

func (s *Store) ClaimSub2ProxyReleases(now time.Time) ([]model.Sub2ProxyBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT profile,revision FROM sub2_proxy_release WHERE next_at>0 AND next_at<=? AND claim_until<=? ORDER BY next_at,id LIMIT 2`, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := []model.Sub2ProxyBinding{}
	for rows.Next() {
		var raw string
		var revision int64
		var b model.Sub2ProxyBinding
		if err = rows.Scan(&raw, &revision); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &b); err != nil {
			break
		}
		b.Revision = revision + 1
		out = append(out, b)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	for _, b := range out {
		if _, err = tx.Exec(`UPDATE sub2_proxy_release SET claim_until=?,revision=? WHERE id=?`, now.Add(90*time.Second).UnixMilli(), b.Revision, b.ID); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func (s *Store) FinishSub2ProxyRelease(b model.Sub2ProxyBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	r, err := s.db.Exec(`UPDATE sub2_proxy_release SET profile=?,next_at=?,claim_until=0 WHERE id=? AND revision=?`, string(raw), b.NextAt, b.ID, b.Revision)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("代理解绑任务已由其他执行接管")
	}
	return nil
}

func (s *Store) RetrySub2ProxyRelease(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var claim int64
	if err := s.db.QueryRow(`SELECT profile,claim_until FROM sub2_proxy_release WHERE id=?`, id).Scan(&raw, &claim); err != nil {
		return errors.New("代理解绑记录不存在")
	}
	var b model.Sub2ProxyBinding
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return err
	}
	if claim > now.UnixMilli() {
		return errors.New("正在核实代理，请稍后刷新")
	}
	if b.State != "retry" {
		return errors.New("仅失败待重试的解绑任务可以重试")
	}
	_, err := s.db.Exec(`UPDATE sub2_proxy_release SET next_at=? WHERE id=?`, now.UnixMilli(), id)
	return err
}
