package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Stable per mailbox/installation, including retries before the first success.
func (s *Store) MailSIWCHostID(email string, c model.MailAccountCredentials) string {
	if c.SIWC != nil && c.SIWC.ExtAgentHostID != "" {
		return c.SIWC.ExtAgentHostID
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("space-siwc-host-v1:" + strings.ToLower(strings.TrimSpace(email))))
	b := mac.Sum(nil)[:16]
	b[6] = (b[6] & 15) | 0x40
	b[8] = (b[8] & 63) | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (s *Store) SaveMailAccountSIWC(email string, tokens model.SIWCCredentials) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if tokens.AuthMode != "siwc" || strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" || strings.TrimSpace(tokens.IDToken) == "" || tokens.ClientID == "" || tokens.ClientID == "dynamic_agent_client" || tokens.ExtAgentHostID == "" || tokens.Subject == "" || tokens.ExpiresAt <= time.Now().Unix() {
		return errors.New("SIWC 凭证不完整或已过期")
	}
	if !strings.EqualFold(strings.TrimSpace(tokens.Email), email) {
		return errors.New("SIWC 凭证账号不匹配")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&raw, &encrypted); err != nil {
		return errors.New("邮箱账号不存在")
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	plain, err := s.decrypt(encrypted)
	if err != nil {
		return err
	}
	var c model.MailAccountCredentials
	if err = json.Unmarshal([]byte(plain), &c); err != nil {
		return err
	}
	tokens.SavedAt = time.Now()
	c.SIWC = &tokens
	p = mailProfileWithCredentials(p, c)
	p.UpdatedAt = tokens.SavedAt
	sealed, err := s.encrypt(mustJSON(c))
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?, encrypted_credentials=?, updated_at=? WHERE email=?", mustJSON(p), sealed, formatTime(p.UpdatedAt), email)
	return err
}

// Re-read under the store lock and change only quota credentials. A concurrent
// SIWC token renewal must never be overwritten by a stale login snapshot.
func (s *Store) UpdateMailAccountSIWCWeb(email, clientID string, update func(*model.SIWCCredentials) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sealed string
	if err := s.db.QueryRow("SELECT encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&sealed); err != nil {
		return err
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return err
	}
	var c model.MailAccountCredentials
	if err = json.Unmarshal([]byte(plain), &c); err != nil {
		return err
	}
	if c.SIWC == nil || c.SIWC.ClientID != clientID {
		return errors.New("SIWC 授权已变化，已丢弃旧网页凭证")
	}
	if err = update(c.SIWC); err != nil {
		return err
	}
	sealed, err = s.encrypt(mustJSON(c))
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET encrypted_credentials=? WHERE email=?", sealed, email)
	return err
}
