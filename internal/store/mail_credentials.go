package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

var ErrMailCredentialsChanged = errors.New("凭证已被其他操作更新，请重新打开窗口后再编辑")

type MailCredentialsPatch struct {
	Revision       string  `json:"revision"`
	GPTPassword    *string `json:"gpt_password,omitempty"`
	TOTPSecret     *string `json:"totp_secret,omitempty"`
	AccessToken    *string `json:"access_token,omitempty"`
	RefreshToken   *string `json:"refresh_token,omitempty"`
	ChatGPTSession *string `json:"chatgpt_session,omitempty"`
	AccountID      *string `json:"chatgpt_account_id,omitempty"`
}

// A keyed revision detects concurrent credential changes without exposing secrets.
func (s *Store) MailCredentialsRevision(p model.MailAccountProfile, c model.MailAccountCredentials) string {
	for _, value := range []*string{&c.Email, &c.MailPassword, &c.ClientID, &c.MailRefreshToken, &c.PickupURL, &c.GptPassword, &c.TotpSecret, &c.AccessToken, &c.RefreshToken, &c.IDToken, &c.ChatGPTSession} {
		*value = cleanImportedCredential(*value)
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(mustJSON(c)))
	mac.Write([]byte(p.OAuthAccountID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) PatchMailCredentials(email string, patch MailCredentialsPatch) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&raw, &encrypted); err != nil {
		return model.MailAccountProfile{}, errors.New("邮箱账号不存在")
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	plain, err := s.decrypt(encrypted)
	if err != nil {
		return p, err
	}
	var c model.MailAccountCredentials
	if err = json.Unmarshal([]byte(plain), &c); err != nil {
		return p, err
	}
	if patch.Revision == "" || !hmac.Equal([]byte(patch.Revision), []byte(s.MailCredentialsRevision(p, c))) {
		return p, ErrMailCredentialsChanged
	}
	for _, field := range []struct{ value, target *string }{
		{patch.GPTPassword, &c.GptPassword}, {patch.TOTPSecret, &c.TotpSecret},
		{patch.AccessToken, &c.AccessToken}, {patch.RefreshToken, &c.RefreshToken},
		{patch.ChatGPTSession, &c.ChatGPTSession}, {patch.AccountID, &p.OAuthAccountID},
	} {
		if field.value != nil {
			*field.target = *field.value
		}
	}
	if patch.RefreshToken != nil {
		p.RefreshTokenEdited = true
	}
	if patch.AccessToken != nil {
		p.ATValid, p.ATCheckedAt, p.ATCheckHTTPStatus, p.ATCheckMessage = false, nil, 0, ""
		p.OAuthExpiresAt = nil
	}
	if patch.AccessToken != nil || patch.RefreshToken != nil {
		p.OAuthStatus = "pending"
		if c.AccessToken != "" && c.RefreshToken != "" {
			p.OAuthStatus = "completed"
		}
	}
	p = mailProfileWithCredentials(p, c)
	p.UpdatedAt = time.Now()
	sealed, err := s.encrypt(mustJSON(c))
	if err != nil {
		return p, err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?, encrypted_credentials=?, updated_at=? WHERE email=?", mustJSON(p), sealed, formatTime(p.UpdatedAt), email)
	return p, err
}
