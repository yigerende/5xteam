package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

func GlobalProxyKey(url string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(url))))
}

func readGlobalProxyHealth(tx *sql.Tx, key string) (model.GlobalProxyHealth, error) {
	h := model.GlobalProxyHealth{Key: key, State: "available"}
	var raw string
	err := tx.QueryRow("SELECT profile,generation,next_at FROM global_proxy_health WHERE key=?", key).Scan(&raw, &h.Generation, &h.NextAt)
	if errors.Is(err, sql.ErrNoRows) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	err = json.Unmarshal([]byte(raw), &h)
	return h, err
}
func writeGlobalProxyHealth(tx *sql.Tx, h model.GlobalProxyHealth, claim int64) error {
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO global_proxy_health(key,profile,next_at,claim_until,generation) VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET profile=excluded.profile,next_at=excluded.next_at,claim_until=excluded.claim_until,generation=excluded.generation`, h.Key, string(raw), h.NextAt, claim, h.Generation)
	return err
}
func (s *Store) GlobalProxyHealth(urls []string) (map[string]model.GlobalProxyHealth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]model.GlobalProxyHealth{}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, url := range urls {
		h, e := readGlobalProxyHealth(tx, GlobalProxyKey(url))
		if e != nil {
			return nil, e
		}
		out[url] = h
	}
	return out, nil
}

// A lease observes a failure generation. Late failures from other old leases
// cannot repeatedly extend the same cooldown, nor poison edited proxy URLs.
func (s *Store) FailGlobalProxy(url string, generation int64, now time.Time) (model.GlobalProxyHealth, bool, error) {
	cfg := s.Settings().ProxyFailover.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.GlobalProxyHealth{}, false, err
	}
	defer tx.Rollback()
	h, err := readGlobalProxyHealth(tx, GlobalProxyKey(url))
	if err != nil {
		return h, false, err
	}
	if h.Generation != generation {
		return h, false, nil
	}
	// Deleted/edited resources must not be revived by a late worker.
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM proxies WHERE url=?", url).Scan(&exists); err != nil {
		return h, false, err
	}
	if exists == 0 {
		return h, false, nil
	}
	h.Generation++
	h.State = "cooling"
	h.Until = now.Add(time.Duration(cfg.CooldownSeconds) * time.Second).UnixMilli()
	h.NextAt = h.Until
	h.Attempts = 0
	h.CheckedAt = now.UnixMilli()
	h.Message = "登录发生网络/代理连接故障"
	if err = writeGlobalProxyHealth(tx, h, 0); err != nil {
		return h, false, err
	}
	return h, true, tx.Commit()
}

func (s *Store) ReadyGlobalProxy(url string, now time.Time) (model.GlobalProxyHealth, bool, error) {
	cfg := s.Settings().ProxyFailover.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.GlobalProxyHealth{}, false, err
	}
	defer tx.Rollback()
	h, err := readGlobalProxyHealth(tx, GlobalProxyKey(url))
	if err != nil {
		return h, false, err
	}
	if h.State == "available" {
		return h, true, nil
	}
	if cfg.RecoveryTests == nil && h.Until <= now.UnixMilli() {
		h.Generation++
		h.State = "available"
		h.NextAt = 0
		h.Until = 0
		h.Message = "冷却结束，按配置直接恢复"
		if err = writeGlobalProxyHealth(tx, h, 0); err != nil {
			return h, false, err
		}
		return h, true, tx.Commit()
	}
	return h, false, nil
}

type GlobalProxyProbe struct {
	Health model.GlobalProxyHealth
	URL    string
	Name   string
}

// Indexed due queue; at most two bounded network probes per batch. No network
// call occurs while a store lock or transaction is held.
func (s *Store) ClaimGlobalProxyProbes(now time.Time) ([]GlobalProxyProbe, error) {
	cfg := s.Settings().ProxyFailover.Normalized()
	profiles := s.Proxies()
	known := map[string]model.ProxyProfile{}
	for _, p := range profiles {
		known[GlobalProxyKey(p.URL)] = p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT key FROM global_proxy_health WHERE next_at>0 AND next_at<=? AND claim_until<=? ORDER BY next_at,key LIMIT 2", now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			break
		}
		keys = append(keys, key)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	result := []GlobalProxyProbe{}
	for _, key := range keys {
		h, e := readGlobalProxyHealth(tx, key)
		if e != nil {
			return nil, e
		}
		profile, exists := known[key]
		if !exists || cfg.RecoveryTests == nil {
			h.Generation++
			h.State = "available"
			h.NextAt = 0
			h.Until = 0
			h.Message = "冷却结束，按配置直接恢复"
			if e = writeGlobalProxyHealth(tx, h, 0); e != nil {
				return nil, e
			}
			continue
		}
		if h.State == "cooling" {
			h.Attempts = 0
		}
		h.State = "testing"
		h.Generation++
		if e = writeGlobalProxyHealth(tx, h, now.Add(60*time.Second).UnixMilli()); e != nil {
			return nil, e
		}
		result = append(result, GlobalProxyProbe{Health: h, URL: profile.URL, Name: profile.Name})
	}
	return result, tx.Commit()
}

func (s *Store) CompleteGlobalProxyProbe(probe GlobalProxyProbe, ok bool, now time.Time) error {
	cfg := s.Settings().ProxyFailover.Normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	h, err := readGlobalProxyHealth(tx, probe.Health.Key)
	if err != nil {
		return err
	}
	if h.Generation != probe.Health.Generation || h.State != "testing" {
		return nil
	}
	var exists int
	if err = tx.QueryRow("SELECT count(*) FROM proxies WHERE url=?", probe.URL).Scan(&exists); err != nil {
		return err
	}
	h.CheckedAt = now.UnixMilli()
	h.Attempts++
	if ok || exists == 0 || cfg.RecoveryTests == nil {
		h.State = "available"
		h.NextAt = 0
		h.Until = 0
		h.Message = "恢复测试通过"
	} else if h.Attempts < *cfg.RecoveryTests {
		h.State = "recovering"
		h.NextAt = now.Add(time.Duration(cfg.TestIntervalSeconds) * time.Second).UnixMilli()
		h.Message = "恢复测试失败，等待下一次测试"
	} else {
		h.State = "cooling"
		h.Until = now.Add(time.Duration(cfg.CooldownSeconds) * time.Second).UnixMilli()
		h.NextAt = h.Until
		h.Message = "恢复测试次数用尽，再次冷却"
	}
	if err = writeGlobalProxyHealth(tx, h, 0); err != nil {
		return err
	}
	return tx.Commit()
}
