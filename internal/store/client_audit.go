package store

import (
	"chatgpt-space-merge/internal/clientaudit"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
)

func (s *Store) ClientAuditPath() string {
	return filepath.Join(filepath.Dir(s.path), "client-audit.db")
}
func (s *Store) ClientAuditSettings() (clientaudit.Config, clientaudit.Secrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := clientaudit.DefaultConfig()
	var secrets clientaudit.Secrets
	var raw, sealed string
	e := s.db.QueryRow("SELECT profile,secrets FROM client_audit_settings WHERE id=1").Scan(&raw, &sealed)
	if errors.Is(e, sql.ErrNoRows) {
		return c, secrets, nil
	}
	if e != nil {
		return c, secrets, e
	}
	if e = json.Unmarshal([]byte(raw), &c); e != nil {
		return c, secrets, e
	}
	if sealed != "" {
		p, x := s.decrypt(sealed)
		if x != nil {
			return c, secrets, x
		}
		if e = json.Unmarshal([]byte(p), &secrets); e != nil {
			return c, secrets, e
		}
	}
	c.DBPasswordPresent = secrets.DBPassword != ""
	return c, secrets, nil
}
func (s *Store) SaveClientAuditSettings(c clientaudit.Config, in clientaudit.Secrets) (clientaudit.Config, error) {
	if e := c.Validate(); e != nil {
		return c, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var old string
	var secrets clientaudit.Secrets
	e := s.db.QueryRow("SELECT secrets FROM client_audit_settings WHERE id=1").Scan(&old)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return c, e
	}
	if old != "" {
		p, e := s.decrypt(old)
		if e != nil {
			return c, e
		}
		if e = json.Unmarshal([]byte(p), &secrets); e != nil {
			return c, e
		}
	}
	if in.DBPassword != "" {
		secrets.DBPassword = in.DBPassword
	}
	c.DBPasswordPresent = secrets.DBPassword != ""
	raw, e := json.Marshal(c)
	if e != nil {
		return c, e
	}
	p, e := json.Marshal(secrets)
	if e != nil {
		return c, e
	}
	sealed, e := s.encrypt(string(p))
	if e != nil {
		return c, e
	}
	_, e = s.db.Exec("INSERT INTO client_audit_settings VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,secrets=excluded.secrets", string(raw), sealed)
	return c, e
}
