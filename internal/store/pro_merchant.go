package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

type ProMerchantSubmission struct {
	Provider string          `json:"provider"`
	APIURL   string          `json:"api_url"`
	APIKey   string          `json:"api_key"`
	Payload  json.RawMessage `json:"payload"`
}

// Pin the endpoint, key and exact payload before sending anything. Retrying a lost POST
// response uses the original idempotency key even after credentials are edited.
func (s *Store) PrepareProMerchant(email string) (model.MailAccountProfile, ProMerchantSubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.MailAccountProfile
	var submission ProMerchantSubmission
	email = strings.ToLower(strings.TrimSpace(email))
	var raw, sealed string
	if err := s.db.QueryRow("SELECT profile,encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&raw, &sealed); err != nil {
		return p, submission, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, submission, err
	}
	if !p.ProFinalStepAllowed("merchant") || p.ProAuto.StopRequested {
		return p, submission, errors.New("账号不在 Pro 管理中或流程已停止")
	}
	var requestID, encrypted string
	err := s.db.QueryRow("SELECT request_id,encrypted_submission FROM pro_merchant_requests WHERE email=?", email).Scan(&requestID, &encrypted)
	if err == nil {
		if !p.MerchantStarted() || requestID != p.ProMerchant.RequestID {
			return p, submission, errors.New("商户申请记录不一致，未重复提交")
		}
		plain, e := s.decrypt(encrypted)
		if e != nil {
			return p, submission, e
		}
		e = json.Unmarshal([]byte(plain), &submission)
		if e == nil {
			submission.Provider, e = refundmerchant.NormalizeProvider(submission.Provider)
		}
		if e == nil {
			submission.APIURL, e = refundmerchant.NormalizeProviderURL(submission.Provider, submission.APIURL)
		}
		return p, submission, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, submission, err
	}
	if p.MerchantStarted() {
		return p, submission, errors.New("商户申请凭据快照缺失，未重复提交")
	}
	if p.DeletionNeedsContinuation() {
		return p, submission, errors.New("已有删除请求等待确认，不能另行提交商户")
	}
	// Legacy snapshots without an address belong to the original merchant.
	// Only a new request reads the current merchant configuration.
	var settingsRaw string
	var settings model.ProSettings
	if err = s.db.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&settingsRaw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, submission, err
	}
	if settingsRaw != "" {
		if err = json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
			return p, submission, err
		}
	}
	submission.Provider, err = refundmerchant.NormalizeProvider(settings.MerchantProvider)
	if err != nil {
		return p, submission, err
	}
	endpoint, keyQuery := settings.MerchantAPIURL, "SELECT encrypted_key FROM pro_merchant_secret WHERE id=1"
	if submission.Provider == refundmerchant.ProviderStarBridge {
		endpoint, keyQuery = settings.MerchantStarBridgeAPIURL, "SELECT encrypted_key FROM pro_starbridge_merchant_secret WHERE id=1"
	}
	if err = s.db.QueryRow(keyQuery).Scan(&encrypted); err != nil || encrypted == "" {
		return p, submission, errors.New("请先配置所选 API 商户的密钥")
	}
	submission.APIKey, err = s.decrypt(encrypted)
	if err != nil {
		return p, submission, err
	}
	submission.APIURL, err = refundmerchant.NormalizeProviderURL(submission.Provider, endpoint)
	if err != nil {
		return p, submission, err
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return p, submission, err
	}
	var credentials model.MailAccountCredentials
	if err = json.Unmarshal([]byte(plain), &credentials); err != nil {
		return p, submission, err
	}
	if credentials.GptPassword == "" || strings.TrimSpace(credentials.TotpSecret) == "" {
		return p, submission, errors.New("API 商户需要账号的 ChatGPT 密码和 2FA 密钥，请先补齐")
	}
	submission.Payload, err = json.Marshal(refundmerchant.Submission{Authorized: true, Accounts: []refundmerchant.Account{{Email: email, Password: credentials.GptPassword, TOTPSecret: strings.TrimSpace(credentials.TotpSecret)}}})
	if err != nil {
		return p, submission, err
	}
	encrypted, err = s.encrypt(mustJSON(submission))
	if err != nil {
		return p, submission, err
	}
	now := time.Now().UTC()
	next := now.Add(15 * time.Second)
	p.ProMerchant = &model.ProMerchant{Provider: submission.Provider, RequestID: "space-" + rand.Text(), Status: "pending", Message: "准备提交商户", StartedAt: now, UpdatedAt: now, NextCheckAt: &next}
	if err = s.pinProMerchantFee(p.ProMerchant, p); err != nil {
		return p, submission, err
	}
	p.ReconcileProMerchant(now)
	p.FreezeProPushDuration(now)
	p.UpdatedAt = now
	tx, err := s.db.Begin()
	if err != nil {
		return p, submission, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO pro_merchant_requests(email,request_id,encrypted_submission) VALUES(?,?,?)", email, p.ProMerchant.RequestID, encrypted); err != nil {
		return p, submission, err
	}
	if _, err = tx.Exec("UPDATE mail_accounts SET profile=?,updated_at=? WHERE email=?", mustJSON(p), formatTime(now), email); err != nil {
		return p, submission, err
	}
	return p, submission, tx.Commit()
}

func (s *Store) DueProMerchants(now time.Time) ([]model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT profile FROM mail_accounts WHERE (json_extract(profile,'$.management_scope')='pro' OR (json_extract(profile,'$.management_scope')='mail' AND json_type(profile,'$.mail_pro_actions')='object'))
	AND json_extract(profile,'$.pro_merchant.request_id') IS NOT NULL
	AND json_extract(profile,'$.pro_merchant.completed_at') IS NULL
	AND COALESCE(json_extract(profile,'$.pro_merchant.status'),'')<>'failed'
	AND COALESCE(json_extract(profile,'$.pro_workflow_running'),0)=0
	AND COALESCE(json_extract(profile,'$.pro_auto.stop_requested'),0)=0
	AND COALESCE(json_extract(profile,'$.pro_migration.paused'),0)=0
	AND julianday(json_extract(profile,'$.pro_merchant.next_check_at'))<=julianday(?)
	ORDER BY julianday(json_extract(profile,'$.pro_merchant.next_check_at')) LIMIT 10`, formatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MailAccountProfile
	for rows.Next() {
		var raw string
		var p model.MailAccountProfile
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
