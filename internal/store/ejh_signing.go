package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

type ejhSigningRecord struct {
	PrivateKey string    `json:"private_key"`
	AppKey     string    `json:"app_key"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"created_at"`
}

// Called with the store mutex held. The existing encrypted record store keeps
// the key out of settings JSON, responses, and plaintext database fields.
func (s *Store) loadEJHSigningLocked(v *ejh.Settings) error {
	v.SigningPrivateKey, v.SigningPublicKey, v.SigningKeyPresent = "", "", false
	var sealed string
	err := s.db.QueryRow(`SELECT sealed FROM ejh_records WHERE kind='signing_key' AND id='rsa'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := s.decrypt(sealed)
	if err != nil {
		return errors.New("EJH 签名密钥读取失败")
	}
	var record ejhSigningRecord
	if json.Unmarshal([]byte(raw), &record) != nil || record.AppKey != v.AppKey || record.URL != v.URL {
		return errors.New("EJH 签名密钥与当前商户不匹配")
	}
	private, public, err := ejh.NormalizeSigningKey(record.PrivateKey)
	if err != nil {
		return err
	}
	v.SigningPrivateKey, v.SigningPublicKey, v.SigningKeyPresent = private, public, true
	return nil
}

// Idempotent: repeating generation returns the already saved public key and
// cannot rotate a key that may already be registered with the issuer.
func (s *Store) EnsureEJHSigningKey(private string) (ejh.Settings, error) {
	return s.saveEJHSigningKey(private, false)
}

// Explicit rotation changes both halves of the pair. The administrator must
// register the new public key and save the issuer's newly issued AppSecret.
func (s *Store) RegenerateEJHSigningKey() (ejh.Settings, error) {
	return s.saveEJHSigningKey("", true)
}

func (s *Store) saveEJHSigningKey(private string, regenerate bool) (ejh.Settings, error) {
	imported := private != ""
	v, secret, err := s.EJHSettings()
	if err != nil {
		return v, err
	}
	if v.AppKey == "" || secret == "" {
		return v, errors.New("请先保存 EJH AppKey 和 AppSecret")
	}
	var public string
	if private != "" {
		private, public, err = ejh.NormalizeSigningKey(private)
	} else if regenerate || !v.SigningKeyPresent {
		private, public, err = ejh.GenerateSigningKey()
	}
	if err != nil {
		return v, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err = s.db.QueryRow(`SELECT profile FROM ejh_settings WHERE id=1`).Scan(&raw); err != nil {
		return v, err
	}
	var current ejh.Settings
	if err = json.Unmarshal([]byte(raw), &current); err != nil {
		return v, err
	}
	if current.AppKey != v.AppKey || current.URL != v.URL {
		return v, errors.New("EJH 商户配置已变化，请刷新后重试")
	}
	if err = s.loadEJHSigningLocked(&v); err != nil {
		return v, err
	}
	if v.SigningKeyPresent && !regenerate {
		if imported && v.SigningPublicKey != public {
			return v, errors.New("已保存其他签名密钥，不能覆盖；请先核实 EJH 官网配置的公钥")
		}
		return v, nil
	}
	record := ejhSigningRecord{PrivateKey: private, AppKey: v.AppKey, URL: v.URL, CreatedAt: time.Now()}
	sealed, err := s.encrypt(mustJSON(record))
	if err != nil {
		return v, err
	}
	_, err = s.db.Exec(`INSERT INTO ejh_records(kind,id,sealed,updated_at) VALUES('signing_key','rsa',?,?) ON CONFLICT(kind,id) DO UPDATE SET sealed=excluded.sealed,updated_at=excluded.updated_at`, sealed, record.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return v, err
	}
	v.SigningPrivateKey, v.SigningPublicKey, v.SigningKeyPresent = private, public, true
	return v, nil
}
