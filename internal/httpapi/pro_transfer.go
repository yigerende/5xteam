package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func (s *Server) exportProAccounts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emails     []string `json:"emails"`
		All        bool     `json:"all"`
		Query      string   `json:"query"`
		MergeState string   `json:"merge_state"`
	}
	if decodeJSON(w, r, &input, 2<<20) != nil {
		return
	}
	emails := input.Emails
	if input.All {
		if len(emails) > 0 {
			writeAPI(w, 400, nil, "导出范围不能同时选择全部和指定账号")
			return
		}
		var err error
		emails, err = s.store.ProExportEmails(input.Query, input.MergeState)
		if err != nil {
			writeAPI(w, 500, nil, "读取导出范围失败")
			return
		}
	}
	if len(emails) == 0 || len(emails) > 10000 {
		writeAPI(w, 400, nil, "请选择 1～10000 个 Pro 账号导出")
		return
	}
	settings, _, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取 Pro 配置失败")
		return
	}
	file, err := s.store.ExportProAccounts(emails, model.ProDownstream(settings))
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil || len(raw) > 64<<20 {
		writeAPI(w, 413, nil, "迁移文件超过 64MB，请分批选择账号导出")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="pro-accounts-`+time.Now().UTC().Format("20060102-150405")+`.json"`)
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

func (s *Server) importProAccounts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		File      store.ProTransferFile `json:"file"`
		Overwrite bool                  `json:"overwrite"`
	}
	if decodeJSON(w, r, &input, 64<<20) != nil {
		return
	}
	if err := store.ValidateProTransfer(input.File); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	settings, _, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取 Pro 配置失败")
		return
	}
	results := make([]store.ProTransferResult, 0, len(input.File.Accounts))
	counts := map[string]int{"imported": 0, "updated": 0, "skipped": 0, "failed": 0}
	for _, entry := range input.File.Accounts {
		email := strings.ToLower(strings.TrimSpace(entry.Profile.Email))
		result := store.ProTransferResult{Email: email, Status: "failed"}
		// Same startup gate as manual OAuth and the automatic runner. TryLock
		// avoids waiting behind a live payment/session/merge operation.
		s.proAutoMu.Lock()
		v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) {
			result.Message = "账号正在登录或执行全自动任务，未覆盖"
		} else if !lock.TryLock() {
			result.Message = "账号正在执行操作，未覆盖"
		} else {
			result, err = s.store.ImportProAccount(entry, input.File.ExportedAt, input.Overwrite, model.ProDownstream(settings))
			lock.Unlock()
			if err != nil {
				result.Status = "failed"
				result.Message = err.Error()
			}
		}
		s.proAutoMu.Unlock()
		counts[result.Status]++
		results = append(results, result)
	}
	writeAPI(w, 200, map[string]any{"results": results, "counts": counts, "total": len(results)}, "")
}

func (s *Server) importedProTarget(p model.MailAccountProfile) (model.MailAccountProfile, error) {
	if p.ProMigration == nil || p.ProMigration.TargetTeamID == "" {
		return p, nil
	}
	teamID := p.ProMigration.TargetTeamID
	for _, admin := range s.store.AdminAccounts() {
		if admin.TeamAccountID == teamID {
			return s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { p.TargetAdminID = admin.ID; p.TargetTeamID = teamID })
		}
	}
	return p, errors.New("请先在母号管理添加原空间 " + teamID + " 对应母号及专属代理，再续跑")
}

func proImportedDownstreamOK(p model.MailAccountProfile, settings model.ProSettings) error {
	if p.ProMigration != nil && p.PushStatus == "completed" && !p.ProMigration.Downstream.Matches(model.ProDownstream(settings)) {
		return errors.New("当前下游与迁移来源不同，请先推送到当前下游；旧下游账号 ID 不会用于当前服务")
	}
	return nil
}

// This explicit continuation never purchases or restarts login. Before OAuth
// is complete, the user resumes through the existing single-step buttons.
func (s *Server) resumeImportedPro(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "账号正在执行操作")
		return
	}
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	p, c, err := s.store.MailAccountCredential(email)
	if err != nil || p.ManagementScope != "pro" || p.ProMigration == nil {
		writeAPI(w, 404, nil, "迁移账号不存在")
		return
	}
	if p.ProWorkflowRunning || s.proAutomationRunning(email) || s.proLoginAlreadyRunning(email) {
		writeAPI(w, 409, nil, "账号正在执行任务")
		return
	}
	if p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed" {
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProMigration.Paused = false
			p.ProMigration.Notice = "流程已完成"
			if p.ProAuto.ID != "" {
				p.ProAuto.Status = "completed"
			}
		})
		if err != nil {
			writeAPI(w, 500, nil, err.Error())
			return
		}
		writeAPI(w, 200, p, "")
		return
	}
	p, err = s.importedProTarget(p)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	if proTransferNeedsConfirmation(p) {
		writeAPI(w, 409, nil, errProTransferUncertain.Error())
		return
	}
	if p.InviteStatus == "completed" || p.AcceptStatus == "completed" || p.TransferStatus == "completed" || p.SpaceMergedOnce {
		p, err = s.startProMergeJob(email, lock.Unlock)
		if err != nil {
			writeAPI(w, 409, nil, err.Error())
			return
		}
		locked = false
		writeAPI(w, 202, p, "")
		return
	}
	orders, err := s.store.ProLatestPaymentOrders([]string{email})
	if err != nil {
		writeAPI(w, 500, nil, "读取原订单失败")
		return
	}
	order, hasOrder := orders[email]
	if p.ProAuto.OrderID != "" {
		order, _, err = s.store.GPTPayOrder(p.ProAuto.OrderID)
		if err != nil {
			writeAPI(w, 409, nil, "原流程关联订单缺失，请先核实原订单")
			return
		}
		hasOrder = true
	}
	if hasOrder && order.Status != "success" {
		writeAPI(w, 409, nil, "请先在“开通 Pro”或 GPTPay 供应商中查询原订单；确认成功后再登录获取 RT/AT，不会重新开通")
		return
	}
	if !hasOrder && p.ProAuto.Steps["recharge"] != "completed" && p.ProManualStages["recharge"].Status != "completed" && !strings.Contains(strings.ToLower(p.CurrentPlanType+" "+p.SubscriptionPlan), "pro") {
		writeAPI(w, 409, nil, "尚未确认开通 Pro，请先使用“开通 Pro”完成开通；已在其他渠道开通的账号请先识别套餐")
		return
	}
	oauthReady := c.AccessToken != "" && c.RefreshToken != "" && p.OAuthStatus == "completed"
	if hasOrder && (p.OAuthAuthorizedAt == nil || p.OAuthAuthorizedAt.Before(order.CreatedAt)) {
		oauthReady = false
	}
	if !oauthReady {
		message := "请先使用“获取临时 AT → 开通 Pro → 登录获取 RT/AT”完成尚未执行的步骤"
		if hasOrder {
			message = "开通已成功，请点击“登录获取 RT/AT”，保存新凭证后再继续；不会重复开通"
		}
		writeAPI(w, 409, nil, message)
		return
	}
	settings, _, _, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取 Pro 配置失败")
		return
	}
	if err = proImportedDownstreamOK(p, settings); err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	if settings.Provider != "sub2" {
		writeAPI(w, 409, nil, "当前为 CPA，请继续使用推送、查额度和空间合并单项按钮")
		return
	}
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.ProAuto.ID == "" {
			p.ProAuto = model.ProAutoState{ID: randomRegistrationID(), Status: "interrupted", Stage: "oauth", StartedAt: time.Now(), Steps: map[string]string{}, QuotaUsedThreshold: settings.QuotaUsedThreshold, QuotaIntervalSeconds: settings.QuotaCheckIntervalSeconds}
		}
		if p.ProAuto.Steps == nil {
			p.ProAuto.Steps = map[string]string{}
		}
		p.ProAuto.Steps["oauth"] = "completed"
		if hasOrder {
			p.ProAuto.OrderID = order.ID
			p.ProAuto.PlanCode = order.PlanCode
			p.ProAuto.Steps["recharge"] = "completed"
		}
		if p.ChatGPTSessionPresent {
			p.ProAuto.Steps["login"] = "completed"
		}
		// The imported downstream binding was checked immediately above.
		if p.PushStatus == "completed" && p.PushProvider == "sub2" {
			p.ProAuto.Steps["push"] = "completed"
		} else {
			delete(p.ProAuto.Steps, "push")
		}
		// Obtain a fresh quota after migration before deciding to merge.
		delete(p.ProAuto.Steps, "quota")
	})
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	lock.Unlock()
	locked = false
	next := r.Clone(r.Context())
	next.Body = io.NopCloser(strings.NewReader("{}"))
	s.startProAutomation(w, next)
}
