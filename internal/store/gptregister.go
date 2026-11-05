package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptregister"
	"chatgpt-space-merge/internal/model"
)

func (s *Store) initializeGPTRegister() error {
	_, err := s.db.Exec(`
 CREATE TABLE IF NOT EXISTS gptregister_settings (id INTEGER PRIMARY KEY CHECK(id=1), payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS gptregister_jobs (
 id TEXT PRIMARY KEY, request_key TEXT NOT NULL UNIQUE, profile TEXT NOT NULL,
 submission TEXT NOT NULL, state TEXT NOT NULL, next_at INTEGER NOT NULL,
 lease_token TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS gptregister_jobs_due ON gptregister_jobs(state,next_at,lease_until);
 CREATE TABLE IF NOT EXISTS gptregister_deliveries (
 platform TEXT NOT NULL, remote_user TEXT NOT NULL, account_id TEXT NOT NULL,
 email TEXT NOT NULL, credentials TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(platform,remote_user,account_id));
 CREATE TABLE IF NOT EXISTS gptregister_receipts (
 job_id TEXT NOT NULL, account_id TEXT NOT NULL, email TEXT NOT NULL,
 result TEXT NOT NULL, reason TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(job_id,account_id));`)
	return err
}

func registerID(prefix string) (string, error) {
	var id [16]byte
	_, err := rand.Read(id[:])
	return fmt.Sprintf("%s%x", prefix, id), err
}

func (s *Store) GPTRegisterSettings() (gptregister.Submission, error) {
	var raw string
	err := s.db.QueryRow(`SELECT payload FROM gptregister_settings WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return gptregister.Submission{}, nil
	}
	if err != nil {
		return gptregister.Submission{}, err
	}
	plain, err := s.decrypt(raw)
	if err != nil {
		return gptregister.Submission{}, err
	}
	var v gptregister.Submission
	err = json.Unmarshal([]byte(plain), &v)
	v.KeyPresent = v.APIKey != ""
	return v, err
}

// A blank key is retained only for the same endpoint, never sent to a new host.
func (s *Store) ResolveGPTRegisterSettings(v gptregister.Submission) (gptregister.Submission, error) {
	base, err := gptregister.NormalizeURL(v.URL)
	if err != nil {
		return v, err
	}
	v.URL = base
	if strings.TrimSpace(v.APIKey) == "" {
		old, e := s.GPTRegisterSettings()
		if e != nil {
			return v, errors.New("读取注册平台配置失败")
		}
		if old.URL != v.URL {
			return v, errors.New("更换平台注册地址时，请重新填写 API Key")
		}
		v.APIKey = old.APIKey
	}
	client, err := gptregister.New(v.URL, v.APIKey)
	if err != nil {
		return v, err
	}
	v.APIKey = client.Key
	v.KeyPresent = true
	return v, nil
}
func (s *Store) SaveGPTRegisterSettings(v gptregister.Submission) (gptregister.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.ResolveGPTRegisterSettings(v)
	if err != nil {
		return v.Settings, err
	}
	sealed, err := s.encrypt(mustJSON(v))
	if err != nil {
		return v.Settings, errors.New("保存注册平台配置失败")
	}
	_, err = s.db.Exec(`INSERT INTO gptregister_settings(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, sealed)
	if err != nil {
		return v.Settings, errors.New("保存注册平台配置失败")
	}
	return v.Settings, nil
}

func (s *Store) CreateGPTRegisterJob(key string, quantity int, oauth bool) (model.GPTRegisterJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var j model.GPTRegisterJob
	if len(key) < 8 || len(key) > 128 {
		return j, errors.New("注册请求标识无效")
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return j, errors.New("注册请求标识无效")
		}
	}
	if quantity < 1 || quantity > 1000 {
		return j, errors.New("注册数量须在 1 至 1000 之间，实际以上游限额为准")
	}
	var raw string
	err := s.db.QueryRow(`SELECT profile FROM gptregister_jobs WHERE request_key=?`, key).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &j); err != nil {
			return j, err
		}
		if j.Quantity != quantity || j.NeedOAuth != oauth {
			return j, errors.New("同一注册请求不能修改数量或 OAuth 选项")
		}
		return j, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return j, err
	}
	cfg, err := s.GPTRegisterSettings()
	if err != nil {
		return j, err
	}
	if _, err = gptregister.New(cfg.URL, cfg.APIKey); err != nil {
		return j, err
	}
	sealed, err := s.encrypt(mustJSON(cfg))
	if err != nil {
		return j, err
	}
	id, err := registerID("gpr-")
	if err != nil {
		return j, err
	}
	now := time.Now()
	j = model.GPTRegisterJob{ID: id, Quantity: quantity, NeedOAuth: oauth, EnterPro: cfg.EnterPro, PlatformURL: cfg.URL, State: "queued", NextAt: now, CreatedAt: now, UpdatedAt: now, Message: "等待注册平台接单"}
	_, err = s.db.Exec(`INSERT INTO gptregister_jobs(id,request_key,profile,submission,state,next_at) VALUES(?,?,?,?,?,?)`, id, key, mustJSON(j), sealed, j.State, now.UnixMilli())
	// Another process may have accepted the same browser intent concurrently.
	if err != nil {
		if e := s.db.QueryRow(`SELECT profile FROM gptregister_jobs WHERE request_key=?`, key).Scan(&raw); e == nil {
			var old model.GPTRegisterJob
			if json.Unmarshal([]byte(raw), &old) == nil && old.Quantity == quantity && old.NeedOAuth == oauth {
				return old, nil
			}
		}
	}
	return j, err
}

func (s *Store) GPTRegisterJobs() ([]model.GPTRegisterJob, error) {
	rows, err := s.db.Query(`SELECT profile FROM gptregister_jobs WHERE state!='completed' OR id IN (SELECT id FROM gptregister_jobs ORDER BY rowid DESC LIMIT 100) ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.GPTRegisterJob{}
	for rows.Next() {
		var raw string
		var j model.GPTRegisterJob
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ClaimGPTRegisterJob(now time.Time) (model.GPTRegisterJob, gptregister.Submission, string, error) {
	var j model.GPTRegisterJob
	var cfg gptregister.Submission
	lease, err := registerID("lease-")
	if err != nil {
		return j, cfg, "", err
	}
	var raw, sealed string
	err = s.db.QueryRow(`UPDATE gptregister_jobs SET lease_token=?,lease_until=? WHERE id=(SELECT id FROM gptregister_jobs WHERE state IN ('queued','syncing','retrying') AND next_at<=? AND lease_until<=? ORDER BY next_at LIMIT 1) RETURNING profile,submission`, lease, now.Add(90*time.Second).UnixMilli(), now.UnixMilli(), now.UnixMilli()).Scan(&raw, &sealed)
	if err != nil {
		return j, cfg, "", err
	}
	if err = json.Unmarshal([]byte(raw), &j); err != nil {
		return j, cfg, lease, err
	}
	plain, err := s.decrypt(sealed)
	if err == nil {
		err = json.Unmarshal([]byte(plain), &cfg)
	}
	return j, cfg, lease, err
}

func (s *Store) GPTRegisterCounts(id string) (imported, skipped int, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(result='imported'),0),COALESCE(SUM(result='skipped'),0) FROM gptregister_receipts WHERE job_id=?`, id).Scan(&imported, &skipped)
	return
}
func (s *Store) SaveGPTRegisterJob(j model.GPTRegisterJob, lease string, release bool) error {
	var err error
	j.Imported, j.Skipped, err = s.GPTRegisterCounts(j.ID)
	if err != nil {
		return err
	}
	j.UpdatedAt = time.Now()
	releaseValue := 0
	if release {
		releaseValue = 1
	}
	res, err := s.db.Exec(`UPDATE gptregister_jobs SET profile=?,state=?,next_at=?,lease_token=CASE WHEN ?=1 THEN '' ELSE lease_token END,lease_until=CASE WHEN ?=1 THEN 0 ELSE lease_until END WHERE id=? AND lease_token=? AND lease_until>?`, mustJSON(j), j.State, j.NextAt.UnixMilli(), releaseValue, releaseValue, j.ID, lease, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("注册任务租约已失效")
	}
	return err
}

// Retry keeps the order/intent, and only replaces an expired key for the same
// endpoint. The worker checks the original remote user before using that key.
func (s *Store) RetryGPTRegisterJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, sealed string
	var until int64
	err := s.db.QueryRow(`SELECT profile,submission,lease_until FROM gptregister_jobs WHERE id=?`, id).Scan(&raw, &sealed, &until)
	if err != nil {
		return errors.New("注册任务不存在")
	}
	var j model.GPTRegisterJob
	if err = json.Unmarshal([]byte(raw), &j); err != nil {
		return err
	}
	if j.State == "completed" {
		return errors.New("任务已同步完成")
	}
	if until > time.Now().UnixMilli() {
		return errors.New("任务正在同步，请稍后查看")
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return err
	}
	var cfg gptregister.Submission
	if err = json.Unmarshal([]byte(plain), &cfg); err != nil {
		return err
	}
	current, err := s.GPTRegisterSettings()
	if err != nil {
		return err
	}
	if current.URL == cfg.URL && current.APIKey != "" {
		cfg.APIKey = current.APIKey
	}
	sealed, err = s.encrypt(mustJSON(cfg))
	if err != nil {
		return err
	}
	j.State = "queued"
	j.NextAt = time.Now()
	j.Message = "继续同步原注册任务"
	j.UpdatedAt = time.Now()
	res, err := s.db.Exec(`UPDATE gptregister_jobs SET profile=?,submission=?,state=?,next_at=?,lease_until=0,lease_token='' WHERE id=? AND lease_until<=?`, mustJSON(j), sealed, j.State, j.NextAt.UnixMilli(), id, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("任务正在同步，请稍后查看")
	}
	return err
}

type GPTRegisterReceipt struct {
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	Result    string `json:"result"`
	Reason    string `json:"reason"`
}

func (s *Store) GPTRegisterReceipts(id string) ([]GPTRegisterReceipt, error) {
	rows, err := s.db.Query(`SELECT account_id,email,result,reason FROM gptregister_receipts WHERE job_id=? ORDER BY created_at,account_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GPTRegisterReceipt{}
	for rows.Next() {
		var r GPTRegisterReceipt
		if err = rows.Scan(&r.AccountID, &r.Email, &r.Result, &r.Reason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Import and durable dedup receipt commit together. Receipts deliberately have
// no FK to mail_accounts: deleting a mailbox never makes it deliverable again.
func (s *Store) ImportGPTRegisterDelivery(j model.GPTRegisterJob, lease string, a gptregister.Account, c gptregister.Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	email := strings.ToLower(strings.TrimSpace(a.Email))
	if a.ID == "" || a.OrderID != j.RemoteOrderID || a.NeedOAuth != j.NeedOAuth || !strings.EqualFold(strings.TrimSpace(c.Email), email) || !strings.Contains(email, "@") || strings.TrimSpace(c.Password) == "" || strings.TrimSpace(c.TOTP) == "" {
		return errors.New("注册平台交付资料不完整或与订单不匹配")
	}
	if j.NeedOAuth && (c.Token("access_token") == "" || c.Token("refresh_token") == "") {
		return errors.New("OAuth 交付尚未包含完整令牌，稍后继续同步")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM gptregister_jobs WHERE id=? AND lease_token=? AND lease_until>?`, j.ID, lease, time.Now().UnixMilli()).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return errors.New("注册任务租约已失效")
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM gptregister_receipts WHERE job_id=? AND account_id=?`, j.ID, a.ID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	result, reason := "imported", "已导入邮件账号"
	if j.EnterPro {
		reason = "已直接进入 Pro 管理"
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM gptregister_deliveries WHERE platform=? AND remote_user=? AND account_id=?`, j.PlatformURL, j.RemoteUserID, a.ID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		result, reason = "skipped", "该交付以前已处理，不重复导入"
	} else {
		if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM mail_accounts WHERE email=?)+(SELECT COUNT(*) FROM pro_dead_accounts WHERE email=?)`, email, email).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			result, reason = "skipped", "邮箱已存在或已标记死号，保留原资料和流程"
		} else {
			id, e := registerID("mail-")
			if e != nil {
				return e
			}
			now := time.Now()
			p := model.MailAccountProfile{ID: id, Email: email, Label: "GPT Register", Group: "free", RegistrationStatus: "success", CreatedAt: now, UpdatedAt: now}
			if j.EnterPro {
				p.ManagementScope = "pro"
				p.ProManagedAt = &now
			}
			creds := model.MailAccountCredentials{Email: email, GptPassword: c.Password, TotpSecret: c.TOTP, AccessToken: c.Token("access_token"), RefreshToken: c.Token("refresh_token"), IDToken: c.Token("id_token")}
			p = mailProfileWithCredentials(p, creds)
			if creds.AccessToken != "" && creds.RefreshToken != "" {
				p.OAuthStatus = "completed"
				p.OAuthAuthorizedAt = &now
				p.OAuthAccountID = c.Token("account_id")
			}
			encrypted, e := s.encrypt(mustJSON(creds))
			if e != nil {
				return e
			}
			if _, err = tx.Exec(`INSERT INTO mail_accounts(id,email,label,group_name,profile,encrypted_credentials,updated_at) VALUES(?,?,?,?,?,?,?)`, id, email, p.Label, p.Group, mustJSON(p), encrypted, formatTime(now)); err != nil {
				return err
			}
		}
		encrypted, e := s.encrypt(mustJSON(c))
		if e != nil {
			return e
		}
		if _, err = tx.Exec(`INSERT INTO gptregister_deliveries(platform,remote_user,account_id,email,credentials,created_at) VALUES(?,?,?,?,?,?)`, j.PlatformURL, j.RemoteUserID, a.ID, email, encrypted, formatTime(time.Now())); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO gptregister_receipts(job_id,account_id,email,result,reason,created_at) VALUES(?,?,?,?,?,?)`, j.ID, a.ID, email, result, reason, formatTime(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}
