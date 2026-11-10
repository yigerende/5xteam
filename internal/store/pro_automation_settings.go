package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

// Only automation fields are changed here. Downstream credentials and groups
// may be edited separately, so never overwrite them from an automation form.
func (s *Store) SaveProAutomationSettings(input model.ProSettings, pay gptpay.Settings, key string, merchantKeys ...string) (model.ProSettings, gptpay.Settings, error) {
	return s.saveProAutomationSettings(input, pay, key, nil, merchantKeys...)
}

func (s *Store) SaveProAutomationProviderSettings(input model.ProSettings, pay gptpay.Settings, key string, rows []gptpay.SettingsInput, merchantKeys ...string) (model.ProSettings, gptpay.Settings, error) {
	return s.saveProAutomationSettings(input, pay, key, rows, merchantKeys...)
}

func (s *Store) saveProAutomationSettings(input model.ProSettings, pay gptpay.Settings, key string, rows []gptpay.SettingsInput, merchantKeys ...string) (model.ProSettings, gptpay.Settings, error) {
	merchantProvider, providerErr := refundmerchant.NormalizeProvider(input.MerchantProvider)
	if providerErr != nil {
		return input, pay, providerErr
	}
	starBridgeURL, providerErr := refundmerchant.NormalizeProviderURL(refundmerchant.ProviderStarBridge, input.MerchantStarBridgeAPIURL)
	if providerErr != nil {
		return input, pay, providerErr
	}
	merchantURL, urlErr := refundmerchant.NormalizeSubmitURL(input.MerchantAPIURL)
	if urlErr != nil {
		return input, pay, urlErr
	}
	fee, feeErr := merchantFeeAmount(input.MerchantSuccessFeeUSD)
	if feeErr != nil {
		return input, pay, feeErr
	}
	input.MerchantSuccessFeeUSD = fee
	if err := input.ValidateDisposition(); err != nil {
		return input, pay, err
	}
	if err := input.ValidatePurchaseWindows(); err != nil {
		return input, pay, err
	}
	pay, key, err := gptpay.NormalizeProviderSettings(pay, key, rows)
	if err != nil {
		return input, pay, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return input, pay, err
	}
	defer tx.Rollback()
	v := model.DefaultProSettings()
	var raw string
	err = tx.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, pay, err
	}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return v, pay, err
		}
	}
	previousDeletionInterval := v.DeletionCheckInterval()
	v.DeletionCheckIntervalSeconds = int(input.DeletionCheckInterval() / time.Second)
	v.QuotaEnabled, v.AutoMergeEnabled = input.QuotaEnabled, input.AutoMergeEnabled
	v.AutoDeleteEnabled = input.AutoDeleteEnabled
	v.AutoMerchantEnabled = input.AutoMerchantEnabled
	v.AutoMergeV2Enabled = input.AutoMergeV2Enabled
	if input.MergeV2Recovery != nil {
		v.MergeV2Recovery = input.MergeV2Recovery
	}
	v.OAIConfirmation = input.OAIConfirmation.Normalized()
	v.OAIConfirmationConcurrency = input.OAIConfirmationConcurrency
	v.OAIInquiry = input.OAIInquiry.Normalized()
	v.MerchantSuccessFeeUSD = input.MerchantSuccessFeeUSD
	v.MerchantAPIURL = merchantURL
	v.MerchantProvider = merchantProvider
	v.MerchantStarBridgeAPIURL = starBridgeURL
	var merchantSecret string
	if err = tx.QueryRow("SELECT encrypted_key FROM pro_merchant_secret WHERE id=1").Scan(&merchantSecret); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, pay, err
	}
	if len(merchantKeys) > 0 && strings.TrimSpace(merchantKeys[0]) != "" {
		merchantSecret, err = s.encrypt(strings.TrimSpace(merchantKeys[0]))
		if err != nil {
			return v, pay, err
		}
		if _, err = tx.Exec("INSERT INTO pro_merchant_secret(id,encrypted_key) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET encrypted_key=excluded.encrypted_key", merchantSecret); err != nil {
			return v, pay, err
		}
	}
	v.MerchantKeyPresent = merchantSecret != ""
	var starBridgeSecret string
	if err = tx.QueryRow("SELECT encrypted_key FROM pro_starbridge_merchant_secret WHERE id=1").Scan(&starBridgeSecret); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, pay, err
	}
	if len(merchantKeys) > 1 && strings.TrimSpace(merchantKeys[1]) != "" {
		starBridgeSecret, err = s.encrypt(strings.TrimSpace(merchantKeys[1]))
		if err != nil {
			return v, pay, err
		}
		if _, err = tx.Exec("INSERT INTO pro_starbridge_merchant_secret(id,encrypted_key) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET encrypted_key=excluded.encrypted_key", starBridgeSecret); err != nil {
			return v, pay, err
		}
	}
	v.MerchantStarBridgeKeyPresent = starBridgeSecret != ""
	selectedKeyPresent := v.MerchantKeyPresent
	if merchantProvider == refundmerchant.ProviderStarBridge {
		selectedKeyPresent = v.MerchantStarBridgeKeyPresent
	}
	if v.AutoMerchantEnabled && !selectedKeyPresent {
		return v, pay, errors.New("请先填写 API 商户密钥")
	}
	v.CardProvider = input.AutoCardProvider()
	v.BalanceFallbackEnabled = input.BalanceFallbackEnabled
	v.PurchaseScheduleEnabled, v.PurchaseWindows = input.PurchaseScheduleEnabled, input.PurchaseWindows
	v.EJHPlanTemplates, v.NexovccPlanTemplates = input.EJHPlanTemplates, input.NexovccPlanTemplates
	v.NexovccAutoOpen, v.NexovccTemplateID, v.NexovccDailyOpenLimit, v.NexovccDailyAmountLimit = input.NexovccAutoOpen, input.NexovccTemplateID, input.NexovccDailyOpenLimit, input.NexovccDailyAmountLimit
	v.EJHAutoOpen, v.EJHTemplateID, v.EJHDailyOpenLimit, v.EJHDailyAmountLimit = input.EJHAutoOpen, input.EJHTemplateID, input.EJHDailyOpenLimit, input.EJHDailyAmountLimit
	v.ForceMergeEnabled, v.ForceMergeMinutes = input.ForceMergeEnabled, input.ForceMergeMinutes
	v.ScheduledEnabled, v.MaxUnmerged, v.ScheduleIntervalSeconds = input.ScheduledEnabled, input.MaxUnmerged, input.ScheduleIntervalSeconds
	v.QuotaUsedThreshold, v.QuotaCheckIntervalSeconds = input.QuotaUsedThreshold, input.QuotaCheckIntervalSeconds
	v.TargetAdminID, v.TargetSeatType = strings.TrimSpace(input.TargetAdminID), input.TargetSeatType
	v.RetryCount, v.RetryIntervalSeconds, v.Concurrency = input.RetryCount, input.RetryIntervalSeconds, input.Concurrency
	v.RechargeReplacementCount = input.RechargeReplacementCount
	v.RechargeFailoverCount = input.RechargeFailoverCount
	v.MergeConcurrency = input.MergeConcurrency
	normalizeProSettings(&v)
	if err = syncProPushTimeoutTx(tx, v); err != nil {
		return v, pay, err
	}
	v.NextQuotaSweepAt = nil
	if v.QuotaEnabled {
		next := time.Now().Add(time.Duration(v.QuotaCheckIntervalSeconds) * time.Second)
		v.NextQuotaSweepAt = &next
	}
	if _, err = tx.Exec(`INSERT INTO pro_settings(id,profile,encrypted_sub2_password,encrypted_cpa_key) VALUES(1,?,'','') ON CONFLICT(id) DO UPDATE SET profile=excluded.profile`, mustJSON(v)); err != nil {
		return v, pay, err
	}
	if err = s.saveGPTPaySettingsTx(tx, &pay, key); err != nil {
		return v, pay, err
	}
	// Save the active supplier first so archiving its previous configuration
	// cannot overwrite an edited inactive row when the selection changes.
	for i := range rows {
		if rows[i].Provider == pay.Provider {
			rows[i].Settings = pay
			continue
		}
		if err = s.saveGPTPayProviderSettingsTx(tx, &rows[i].Settings, rows[i].APIKey); err != nil {
			return v, pay, err
		}
	}
	if v.DeletionCheckInterval() != previousDeletionInterval {
		if err = rescheduleProDeactivationsTx(tx, v.DeletionCheckInterval()); err != nil {
			return v, pay, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO pro_schedule_clock VALUES(1,?) ON CONFLICT(id) DO UPDATE SET next_at=excluded.next_at`, time.Now().Add(time.Duration(v.ScheduleIntervalSeconds)*time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		return v, pay, err
	}
	return v, pay, tx.Commit()
}

// Run once for old profiles. Prefer the old active quota schedule; otherwise
// retain the full-automation interval. Per-account running snapshots are untouched.
func (s *Store) migrateProQuotaSettings() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := model.DefaultProSettings()
	var raw string
	err = tx.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var fields map[string]json.RawMessage
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
	}
	if _, exists := fields["quota_used_threshold"]; !exists {
		var legacyRaw string
		err = tx.QueryRow("SELECT profile FROM gptpay_settings WHERE id=1").Scan(&legacyRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if legacyRaw != "" {
			var legacy struct {
				Threshold float64 `json:"quota_used_threshold"`
				Interval  int     `json:"quota_interval_seconds"`
			}
			if err = json.Unmarshal([]byte(legacyRaw), &legacy); err != nil {
				return err
			}
			if legacy.Threshold > 0 {
				v.QuotaUsedThreshold = legacy.Threshold
			}
			if !v.QuotaEnabled && legacy.Interval >= 10 {
				v.QuotaCheckIntervalSeconds = legacy.Interval
			}
		}
		normalizeProSettings(&v)
		if _, err = tx.Exec(`INSERT INTO pro_settings(id,profile,encrypted_sub2_password,encrypted_cpa_key) VALUES(1,?,'','') ON CONFLICT(id) DO UPDATE SET profile=excluded.profile`, mustJSON(v)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE gptpay_settings SET profile=json_remove(profile,'$.quota_used_threshold','$.quota_interval_seconds') WHERE id=1 AND (json_type(profile,'$.quota_used_threshold') IS NOT NULL OR json_type(profile,'$.quota_interval_seconds') IS NOT NULL)`); err != nil {
		return err
	}
	return tx.Commit()
}
