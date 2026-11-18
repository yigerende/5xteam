package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Link profile and quota-only credentials atomically using the same optimistic
// version check as other operator changes. OAuth credentials are untouched.
func (s *Store) LinkProSubAccount(email string, expected time.Time, web *model.SIWCLinkedWebCredentials, mutate func(*model.MailAccountProfile)) (model.MailAccountProfile, error) {
	if expected.IsZero() || web != nil && (web.Endpoint == "" || web.AccountID < 1 || !strings.EqualFold(strings.TrimSpace(web.Email), strings.TrimSpace(email))) {
		return model.MailAccountProfile{}, errors.New("关联账号版本或网页额度身份无效")
	}
	return s.updateProAccount(email, expected, mutate, web)
}

func (s *Store) UpdateMailAccountSIWCLinkedWeb(email, endpoint string, accountID int64, update func(*model.SIWCCredentials) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sealed string
	if err := s.db.QueryRow("SELECT encrypted_credentials FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email))).Scan(&sealed); err != nil {
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
	w := c.SIWCWebLink
	if w == nil || w.Endpoint != endpoint || w.AccountID != accountID || !strings.EqualFold(w.Email, email) {
		return errors.New("手动关联的 SIWC 账号已变化，丢弃旧网页凭证")
	}
	v := model.SIWCCredentials{WebAccessToken: w.WebAccessToken, WebAccountID: w.WebAccountID, WebQuota: w.WebQuota}
	if err = update(&v); err != nil {
		return err
	}
	w.WebAccessToken, w.WebAccountID, w.WebQuota = v.WebAccessToken, v.WebAccountID, v.WebQuota
	sealed, err = s.encrypt(mustJSON(c))
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET encrypted_credentials=? WHERE email=?", sealed, strings.ToLower(strings.TrimSpace(email)))
	return err
}
