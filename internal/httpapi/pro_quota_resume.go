package httpapi

import (
	"net/http"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

func (s *Server) resumeProQuota(w http.ResponseWriter, r *http.Request, expected model.MailAccountProfile) {
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	value, _ := s.proLocks.LoadOrStore("account:"+expected.Email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "账号正在执行其他操作")
		return
	}
	defer lock.Unlock()
	p, _, err := s.store.MailAccountCredential(expected.Email)
	if err != nil || s.proAutoClosed || s.proAutoJobs[expected.Email] != nil || s.proLoginAlreadyRunning(expected.Email) ||
		p.ProAuto.ID != expected.ProAuto.ID || p.ProAuto.OrderID != expected.ProAuto.OrderID || p.ProAuto.StopVersion != expected.ProAuto.StopVersion || !p.CanResumeProQuota() {
		writeAPI(w, 409, nil, "账号状态已变化，或尚未完成授权和推送，请刷新后核对")
		return
	}
	order, _, err := s.store.GPTPayOrder(p.ProAuto.OrderID)
	if err != nil || order.Email != p.Email || order.Status != "success" {
		writeAPI(w, 409, nil, "原订单尚未确认开通成功，请先查询原订单")
		return
	}
	cfg, password, _, err := s.proAccountPushSettings(p)
	if err == nil {
		err = s.validateProAutoPush(p, cfg, password)
	}
	if err == nil {
		err = proImportedDownstreamOK(p, cfg)
	}
	if err != nil || cfg.Provider != p.PushProvider || proTransferNeedsConfirmation(p) {
		writeAPI(w, 409, nil, "当前推送连接或合并状态不允许恢复，请先核对配置")
		return
	}
	if cfg.Disposition() == "merge" && p.TargetAdminID == "" && cfg.TargetAdminID == "" {
		writeAPI(w, 409, nil, "请先选择合并目标母号")
		return
	}
	now := time.Now().UTC()
	p, err = s.store.UpdateProAccount(p.Email, func(v *model.MailAccountProfile) {
		v.ProAuto.StopRequested = false
		v.ProAuto.Status, v.ProAuto.Stage, v.ProAuto.Error = "waiting_quota", "quota", ""
		v.ProAuto.NextCheckAt, v.ProAuto.RetryAt = &now, nil
		v.ProAuto.UpdatedAt = now
		if v.ProAuto.Replacement != nil {
			v.ProAuto.Replacement.Message = "原账号已付款，已恢复原账号额度监控；换号记录保留"
		}
		if v.ProAuto.Steps == nil {
			v.ProAuto.Steps = map[string]string{}
		}
		v.ProAuto.Steps["recharge"], v.ProAuto.Steps["oauth"], v.ProAuto.Steps["push"], v.ProAuto.Steps["quota"] = "completed", "completed", "completed", "waiting"
		delete(v.ProManualStages, "quota")
		delete(v.ProStageOverrides, "quota")
		if v.ProAuto.QuotaIntervalSeconds < 1 {
			v.ProAuto.QuotaIntervalSeconds = cfg.QuotaCheckIntervalSeconds
		}
		if v.ProAuto.QuotaUsedThreshold <= 0 || v.ProAuto.QuotaUsedThreshold > 100 {
			v.ProAuto.QuotaUsedThreshold = cfg.QuotaUsedThreshold
		}
		if v.TargetAdminID == "" {
			v.TargetAdminID, v.TargetSeatType = cfg.TargetAdminID, cfg.TargetSeatType
		}
	})
	if err != nil {
		writeAPI(w, 500, nil, "恢复额度监控失败")
		return
	}
	s.auditProEvent(p, "quota_resume", "completed", p.PushProvider, "已恢复额度监控，沿用原订单、凭证和推送记录", map[string]any{"interval_seconds": p.ProAuto.QuotaIntervalSeconds, "threshold": p.ProAuto.QuotaUsedThreshold})
	writeAPI(w, http.StatusAccepted, p, "")
}
