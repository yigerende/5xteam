package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// SaveMailGPTInfo merges only queried fields into the latest profile. Identity
// and credential checks reject delayed responses after deletion or a new login.
func (s *Store) SaveMailGPTInfo(email, accountID, accessToken string, result model.MailGPTInfoResult) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_credentials FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email))).Scan(&raw, &encrypted); err != nil {
		return model.MailAccountProfile{}, errors.New("邮箱账号已不存在，未写入查询结果")
	}
	var profile model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return profile, err
	}
	if profile.ID != accountID {
		return profile, errors.New("邮箱账号已重新创建，未写入旧查询结果")
	}
	clear, err := s.decrypt(encrypted)
	if err != nil {
		return profile, err
	}
	var credentials model.MailAccountCredentials
	if err = json.Unmarshal([]byte(clear), &credentials); err != nil {
		return profile, err
	}
	if credentials.AccessToken != accessToken {
		return profile, errors.New("AT 已更新，请重新刷新 GPT 信息")
	}
	previousSource := ""
	if profile.GPTInfoCheck != nil {
		previousSource = profile.GPTInfoCheck.PlanSource
	}
	if result.Plan.OK {
		profile.CurrentPlanType = result.Plan.CurrentPlanType
		profile.PlanCheckedAt, profile.PlanLastSuccessAt = &result.Check.CheckedAt, &result.Check.CheckedAt
		profile.PlanCheckStatus, profile.PlanCheckError = "success", ""
		profile.PlanCheckHTTPStatus = result.Plan.HTTPStatus
		if result.Check.PlanSource == "entitlement" {
			profile.SubscriptionPlan, profile.HasActiveSubscription = result.Plan.SubscriptionPlan, result.Plan.HasActiveSubscription
			profile.PlusTrialEligible = result.Plan.PlusTrialEligible
			profile.PlanExpiresAt, profile.PlanRenewsAt = result.Plan.ExpiresAt, result.Plan.RenewsAt
			profile.BillingPeriod, profile.BillingCurrency = result.Plan.BillingPeriod, result.Plan.BillingCurrency
		}
	} else {
		profile.PlanCheckedAt = &result.Check.CheckedAt
		profile.PlanCheckStatus, profile.PlanCheckError = "failed", result.Check.Plan.Error
		profile.PlanCheckHTTPStatus = result.Check.Plan.HTTPStatus
		if result.Check.PlanSource == "jwt" && (profile.CurrentPlanType == "" || previousSource == "jwt") {
			profile.CurrentPlanType = result.Plan.CurrentPlanType
		} else if profile.CurrentPlanType != "" {
			// The displayed plan is still the last successful value, not the
			// stale JWT fallback from this unsuccessful attempt.
			result.Check.PlanSource = previousSource
		}
	}
	if result.CreatedAtOpenAI != nil {
		profile.CreatedAtOpenAI = result.CreatedAtOpenAI
	}
	profile.GPTInfoCheck = &result.Check
	profile.UpdatedAt = time.Now()
	encoded, err := json.Marshal(profile)
	if err != nil {
		return profile, err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?, updated_at=? WHERE email=? AND id=?", string(encoded), formatTime(profile.UpdatedAt), profile.Email, accountID)
	return profile, err
}
