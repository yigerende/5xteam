package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

func normalizeProSettings(v *model.ProSettings) {
	v.Provider = strings.ToLower(strings.TrimSpace(v.Provider))
	if v.Provider != "cpa" {
		v.Provider = "sub2"
	}
	if v.QuotaCheckIntervalSeconds < 10 {
		v.QuotaCheckIntervalSeconds = 120
	}
	if v.TargetSeatType != "prolite" {
		v.TargetSeatType = "default"
	}
	if v.RetryCount < 0 {
		v.RetryCount = 0
	}
	if v.RetryCount > 10 {
		v.RetryCount = 10
	}
	if v.RetryIntervalSeconds < 1 {
		v.RetryIntervalSeconds = 3
	}
	if v.Concurrency < 1 {
		v.Concurrency = 2
	}
	if v.Concurrency > 20 {
		v.Concurrency = 20
	}
	normalizeSub2Settings(&v.Sub2)
	v.Sub2.Provider = v.Provider
	if v.CPA.StatusCheckIntervalSeconds < 10 {
		v.CPA.StatusCheckIntervalSeconds = 120
	}
	if v.CPA.QuotaCheckIntervalSeconds < 10 {
		v.CPA.QuotaCheckIntervalSeconds = 120
	}
}

func (s *Store) ProSettings() (model.ProSettings, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := model.DefaultProSettings()
	var raw, encryptedSub2, encryptedCPA string
	if err := s.db.QueryRow("SELECT profile, encrypted_sub2_password, encrypted_cpa_key FROM pro_settings WHERE id=1").Scan(&raw, &encryptedSub2, &encryptedCPA); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return v, "", "", nil
		}
		return v, "", "", err
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, "", "", err
	}
	normalizeProSettings(&v)
	subPassword, err := s.decryptOptional(encryptedSub2)
	if err != nil {
		return v, "", "", err
	}
	cpaKey, err := s.decryptOptional(encryptedCPA)
	if err != nil {
		return v, "", "", err
	}
	v.Sub2.PasswordPresent, v.CPA.KeyPresent = encryptedSub2 != "", encryptedCPA != ""
	return v, subPassword, cpaKey, nil
}

func (s *Store) SaveProSettings(v model.ProSettings, subPassword, cpaKey string) (model.ProSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalizeProSettings(&v)
	var oldSub, oldCPA string
	_ = s.db.QueryRow("SELECT encrypted_sub2_password, encrypted_cpa_key FROM pro_settings WHERE id=1").Scan(&oldSub, &oldCPA)
	var err error
	if strings.TrimSpace(subPassword) != "" {
		oldSub, err = s.encrypt(strings.TrimSpace(subPassword))
		if err != nil {
			return v, err
		}
	}
	if strings.TrimSpace(cpaKey) != "" {
		oldCPA, err = s.encrypt(strings.TrimSpace(cpaKey))
		if err != nil {
			return v, err
		}
	}
	v.Sub2.PasswordPresent, v.CPA.KeyPresent = oldSub != "", oldCPA != ""
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	_, err = s.db.Exec(`INSERT INTO pro_settings(id,profile,encrypted_sub2_password,encrypted_cpa_key) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_sub2_password=excluded.encrypted_sub2_password,encrypted_cpa_key=excluded.encrypted_cpa_key`, string(raw), oldSub, oldCPA)
	return v, err
}

func (s *Store) SaveProOAuthSession(session model.ProOAuthSession, verifier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, err := s.encrypt(verifier)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO pro_oauth_sessions(id,profile,encrypted_verifier,expires_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_verifier=excluded.encrypted_verifier,expires_at=excluded.expires_at", session.ID, string(raw), sealed, formatTime(session.ExpiresAt))
	return err
}

func (s *Store) ProOAuthSession(id string) (model.ProOAuthSession, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec("DELETE FROM pro_oauth_sessions WHERE expires_at < ?", formatTime(time.Now()))
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile,encrypted_verifier FROM pro_oauth_sessions WHERE id=?", strings.TrimSpace(id)).Scan(&raw, &encrypted); err != nil {
		return model.ProOAuthSession{}, "", errors.New("OAuth 会话不存在或已过期")
	}
	var session model.ProOAuthSession
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return session, "", err
	}
	verifier, err := s.decrypt(encrypted)
	return session, verifier, err
}

func (s *Store) DeleteProOAuthSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM pro_oauth_sessions WHERE id=?", strings.TrimSpace(id))
	return err
}

func (s *Store) UpdateProAccount(email string, mutate func(*model.MailAccountProfile)) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		return model.MailAccountProfile{}, errors.New("Pro 账号不存在")
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	if normalizeMailManagementScope(p.ManagementScope) != "pro" {
		return p, errors.New("账号不在 Pro 管理中")
	}
	if mutate != nil {
		mutate(&p)
	}
	p.UpdatedAt = time.Now()
	encoded, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", string(encoded), formatTime(p.UpdatedAt), email)
	return p, err
}

func (s *Store) RecoverProWorkflows() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT email,profile FROM mail_accounts")
	if err != nil {
		return err
	}
	type item struct {
		email   string
		profile model.MailAccountProfile
	}
	var updates []item
	for rows.Next() {
		var email, raw string
		if rows.Scan(&email, &raw) != nil {
			continue
		}
		var p model.MailAccountProfile
		if json.Unmarshal([]byte(raw), &p) != nil || normalizeMailManagementScope(p.ManagementScope) != "pro" || !p.ProWorkflowRunning {
			continue
		}
		p.ProWorkflowRunning = false
		for _, status := range []*string{&p.InviteStatus, &p.AcceptStatus, &p.TransferStatus, &p.RemoveStatus} {
			if *status == "running" {
				*status = "pending"
			}
		}
		p.ProLastError = "服务重启，流程将在下次检测或手动执行时续跑"
		p.UpdatedAt = time.Now()
		updates = append(updates, item{email: email, profile: p})
	}
	_ = rows.Close()
	for _, update := range updates {
		raw, _ := json.Marshal(update.profile)
		if _, err = s.db.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", string(raw), formatTime(update.profile.UpdatedAt), update.email); err != nil {
			return err
		}
	}
	return nil
}
