package httpapi

import (
	"math"
	"net/http"
	"strings"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func (s *Server) saveProAutomationSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Automation model.ProSettings `json:"automation"`
		GPTPay     struct {
			gptpay.Settings
			APIKey string `json:"api_key"`
		} `json:"gptpay"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	v := input.Automation
	if v.ScheduledEnabled && (v.MaxUnmerged < 1 || v.MaxUnmerged > 10000 || v.ScheduleIntervalSeconds < 10 || v.ScheduleIntervalSeconds > 86400 || strings.TrimSpace(v.TargetAdminID) == "") {
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
	if v.AutoMergeEnabled && strings.TrimSpace(v.TargetAdminID) == "" {
		writeAPI(w, 400, nil, "开启自动合并前请选择目标母号")
		return
	}
	pay, err := gptpay.NormalizeSettings(input.GPTPay.Settings)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	v, pay, err = s.store.SaveProAutomationSettings(v, pay, input.GPTPay.APIKey)
	if err != nil {
		writeAPI(w, 500, nil, "保存 Pro 全自动配置失败")
		return
	}
	writeAPI(w, 200, map[string]any{"automation": v, "gptpay": pay}, "")
}
