package store

import (
	"database/sql"
	"errors"
	"strings"

	"chatgpt-space-merge/internal/model"
)

// Keep the exact provider session outside public account/credential responses.
// Each complete login attempt replaces its own flow's pin, including failover.
func (s *Store) SaveProAutoProxy(p model.MailAccountProfile, proxy, name string) error {
	if p.ProAuto.ID == "" || strings.TrimSpace(proxy) == "" {
		return errors.New("缺少全自动流程或登录代理会话")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, err := s.encrypt(proxy)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`INSERT INTO pro_auto_proxies(email,flow_id,name,encrypted_proxy)
	 SELECT email,?,?,? FROM mail_accounts WHERE email=?
	 AND json_extract(profile,'$.pro_auto.id')=?
	 AND coalesce(json_extract(profile,'$.pro_auto.stop_version'),0)=?
	 AND coalesce(json_extract(profile,'$.pro_auto.stop_requested'),0)=0
	 ON CONFLICT(email) DO UPDATE SET flow_id=excluded.flow_id,name=excluded.name,encrypted_proxy=excluded.encrypted_proxy`,
		p.ProAuto.ID, name, sealed, p.Email, p.ProAuto.ID, p.ProAuto.StopVersion)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("全自动流程已变化，未保存旧登录代理会话")
	}
	return err
}

// No pin means an older flow never saved its original provider session.
func (s *Store) ProAutoProxy(email, flowID string) (proxy, name string, err error) {
	if flowID == "" {
		return "", "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sealed string
	err = s.db.QueryRow(`SELECT a.encrypted_proxy,a.name FROM pro_auto_proxies a
	 JOIN mail_accounts m ON m.email=a.email WHERE a.email=? AND a.flow_id=?
	 AND json_extract(m.profile,'$.pro_auto.id')=a.flow_id`, strings.ToLower(strings.TrimSpace(email)), flowID).Scan(&sealed, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	proxy, err = s.decrypt(sealed)
	return proxy, name, err
}
