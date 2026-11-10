package httpapi

import (
	"math"
	"net/http"
	"strings"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func (s *Server) saveProAutomationSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MerchantAPIKey           string                 `json:"merchant_api_key"`
		MerchantStarBridgeAPIKey string                 `json:"merchant_starbridge_api_key"`
		Automation               model.ProSettings      `json:"automation"`
		GPTPay                   gptpay.SettingsInput   `json:"gptpay"`
		Providers                []gptpay.SettingsInput `json:"providers"`
	}
	current, subPassword, _, readErr := s.store.ProSettings()
	if readErr != nil {
		writeAPI(w, 500, nil, "读取 Pro 配置失败")
		return
	}
	// Older clients omit the new field; retain its saved value. Explicit zero
	// still fails validation after decoding.
	input.Automation.BalanceFallbackEnabled = current.BalanceFallbackEnabled
	input.Automation.RechargeReplacementCount = current.RechargeReplacementCount
	input.Automation.RechargeFailoverCount = current.RechargeFailoverCount
	input.Automation.MergeConcurrency = current.MergeConcurrency
	input.Automation.AutoDeleteEnabled = current.AutoDeleteEnabled
	input.Automation.AutoMerchantEnabled = current.AutoMerchantEnabled
	input.Automation.AutoMergeV2Enabled = current.AutoMergeV2Enabled
	input.Automation.MerchantSuccessFeeUSD = current.MerchantSuccessFeeUSD
	input.Automation.MerchantAPIURL = current.MerchantAPIURL
	input.Automation.MerchantProvider = current.MerchantProvider
	input.Automation.MerchantStarBridgeAPIURL = current.MerchantStarBridgeAPIURL
	input.Automation.DeletionCheckIntervalSeconds = current.DeletionCheckIntervalSeconds
	input.Automation.OAIConfirmation = current.OAIConfirmation.Normalized()
	input.Automation.OAIConfirmationConcurrency = current.OAIConfirmationConcurrency
	input.Automation.OAIInquiry = current.OAIInquiry.Normalized()
	input.Automation.PurchaseScheduleEnabled, input.Automation.PurchaseWindows = current.PurchaseScheduleEnabled, current.PurchaseWindows
	input.Automation.EJHPlanTemplates, input.Automation.NexovccPlanTemplates = current.EJHPlanTemplates, current.NexovccPlanTemplates
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	v := input.Automation
	if v.RechargeFailoverCount < 0 || v.RechargeFailoverCount > 10 {
		writeAPI(w, 400, nil, "切换供应商后账号尝试次数须为 0～10")
		return
	}
	if v.RechargeReplacementCount < 0 || v.RechargeReplacementCount > 10 {
		writeAPI(w, 400, nil, "第二步失败换号次数须为 0～10")
		return
	}
	if v.OAIConfirmationConcurrency < 1 || v.OAIConfirmationConcurrency > 100 {
		writeAPI(w, 400, nil, "OAI 确认并发数须为 1～100")
		return
	}
	merchantURL, urlErr := refundmerchant.NormalizeSubmitURL(v.MerchantAPIURL)
	if urlErr != nil {
		writeAPI(w, 400, nil, urlErr.Error())
		return
	}
	v.MerchantAPIURL = merchantURL
	v.MerchantProvider, urlErr = refundmerchant.NormalizeProvider(v.MerchantProvider)
	if urlErr != nil {
		writeAPI(w, 400, nil, urlErr.Error())
		return
	}
	v.MerchantStarBridgeAPIURL, urlErr = refundmerchant.NormalizeProviderURL(refundmerchant.ProviderStarBridge, v.MerchantStarBridgeAPIURL)
	if urlErr != nil {
		writeAPI(w, 400, nil, urlErr.Error())
		return
	}
	keyPresent := current.MerchantKeyPresent || strings.TrimSpace(input.MerchantAPIKey) != ""
	if v.MerchantProvider == refundmerchant.ProviderStarBridge {
		keyPresent = current.MerchantStarBridgeKeyPresent || strings.TrimSpace(input.MerchantStarBridgeAPIKey) != ""
	}
	if v.AutoMerchantEnabled && !keyPresent {
		writeAPI(w, 400, nil, "请填写 API 商户密钥")
		return
	}
	if v.DeletionCheckIntervalSeconds < 1 || v.DeletionCheckIntervalSeconds > 86400 {
		writeAPI(w, 400, nil, "删除结果核验间隔须为 1～86400 秒")
		return
	}
	if err := v.ValidateDisposition(); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if v.MergeConcurrency < 1 || v.MergeConcurrency > 20 {
		writeAPI(w, 400, nil, "合并并发数须为 1～20")
		return
	}
	if err := s.validateEJHAutomation(v); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if v.ScheduledEnabled && (v.MaxUnmerged < 1 || v.MaxUnmerged > 10000 || v.ScheduleIntervalSeconds < 10 || v.ScheduleIntervalSeconds > 86400 || ((v.Disposition() == "merge" || v.Disposition() == "merge_v2") && strings.TrimSpace(v.TargetAdminID) == "")) {
		writeAPI(w, 400, nil, "定时开通需设置目标母号、最大未合并数（1～10000）和检查间隔（10～86400 秒）")
		return
	}
	if v.QuotaUsedThreshold <= 0 || v.QuotaUsedThreshold > 100 || math.IsNaN(v.QuotaUsedThreshold) || math.IsInf(v.QuotaUsedThreshold, 0) {
		writeAPI(w, 400, nil, "7 天已用额度阈值须大于 0 且不超过 100%")
		return
	}
	if v.QuotaCheckIntervalSeconds < 10 || v.QuotaCheckIntervalSeconds > 86400 || v.Concurrency < 1 || v.Concurrency > 20 || v.RetryCount < 0 || v.RetryCount > 10 || v.RetryIntervalSeconds < 1 {
		writeAPI(w, 400, nil, "请检查检测间隔（10～86400 秒）、并发（1～20）、重试次数（0～10）和重试间隔（至少 1 秒）")
		return
	}
	if v.TargetSeatType != "default" && v.TargetSeatType != "prolite" {
		writeAPI(w, 400, nil, "请选择普通或 5x 空间席位")
		return
	}
	if (v.AutoMergeEnabled || v.AutoMergeV2Enabled) && strings.TrimSpace(v.TargetAdminID) == "" {
		writeAPI(w, 400, nil, "开启自动合并前请选择目标母号")
		return
	}
	pay, key, err := gptpay.NormalizeProviderSettings(input.GPTPay.Settings, input.GPTPay.APIKey, input.Providers)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if err = s.validateProPurchaseConfig(v, pay); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	// The automation form cannot override the separately saved push connection.
	v.Provider, v.Sub2 = current.Provider, current.Sub2
	if err = s.validatePurchaseGroups(r.Context(), v, subPassword); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	v, pay, err = s.store.SaveProAutomationProviderSettings(v, pay, key, input.Providers, input.MerchantAPIKey, input.MerchantStarBridgeAPIKey)
	if err != nil {
		writeAPI(w, 500, nil, "保存 Pro 全自动配置失败")
		return
	}
	result := map[string]any{"automation": v, "gptpay": pay}
	if input.Providers != nil {
		rows := make([]gptpay.Settings, len(input.Providers))
		for i, row := range input.Providers {
			rows[i] = row.Settings
		}
		result["providers"] = rows
	}
	writeAPI(w, 200, result, "")
}
