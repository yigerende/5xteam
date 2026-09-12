package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

type mailTOTPCode struct {
	Code       string    `json:"code"`
	ExpiresAt  time.Time `json:"expires_at"`
	ValidForMS int64     `json:"valid_for_ms"`
}

func generateMailTOTP(secret string, now time.Time) (mailTOTPCode, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return mailTOTPCode{}, errors.New("该账号未配置 OpenAI 2FA 密钥")
	}
	// Match pyotp.TOTP(secret).now(): SHA-1, six digits, a 30-second period.
	code, err := totp.GenerateCodeCustom(secret, now, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return mailTOTPCode{}, errors.New("OpenAI 2FA 密钥格式无效，无法生成验证码")
	}
	expires := time.Unix((now.Unix()/30+1)*30, 0).UTC()
	return mailTOTPCode{Code: code, ExpiresAt: expires, ValidForMS: expires.Sub(now).Milliseconds()}, nil
}

func (s *Server) getMailAccountTOTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	_, credentials, err := s.store.MailAccountCredential(email)
	if err != nil {
		writeAPI(w, http.StatusNotFound, nil, "无法读取该邮件账号的 2FA 密钥")
		return
	}
	result, err := generateMailTOTP(credentials.TotpSecret, time.Now())
	if err != nil {
		writeAPI(w, http.StatusConflict, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, result, "")
}
