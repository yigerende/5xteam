package httpapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

func (s *Server) linkProSubAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	s.proAutoMu.Lock()
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if s.proAutoClosed || s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) || !lock.TryLock() {
		s.proAutoMu.Unlock()
		writeAPI(w, 409, nil, "账号正在执行操作，请稍后关联")
		return
	}
	s.proAutoMu.Unlock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	p, err := s.performProSubLink(ctx, email)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	lock.Unlock()
	locked = false
	// Only the existing tail is scheduled: it cannot submit payment or OAuth.
	if p.ProAuto.ID != "" {
		s.queueDueProAutomation(email)
	} else {
		s.queueLinkedProQuota(email)
	}
	writeAPI(w, http.StatusAccepted, p, "")
}

func (s *Server) queueLinkedProQuota(email string) {
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if s.proAutoClosed || s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) || !lock.TryLock() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if s.proAutoJobs == nil {
		s.proAutoJobs = map[string]context.CancelFunc{}
	}
	s.proAutoJobs[email] = cancel
	s.proAutoWG.Add(1)
	go func() {
		defer s.proAutoWG.Done()
		defer cancel()
		defer func() { lock.Unlock(); s.proAutoMu.Lock(); delete(s.proAutoJobs, email); s.proAutoMu.Unlock() }()
		_, _ = s.performProQuota(ctx, email, true)
	}()
}

func (s *Server) performProSubLink(ctx context.Context, email string) (model.MailAccountProfile, error) {
	p, creds, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.ManagementScope != "pro" || p.IsProDead() || p.ProWorkflowRunning || p.DispositionStarted() || p.SpaceMergedOnce || p.ProAuto.Status == "completed" || p.VM2 != nil || p.ProSIWC != nil || proTransferNeedsConfirmation(p) || p.ProMigration != nil && p.ProMigration.Paused {
		return p, errors.New("账号已进入收尾或当前状态不允许关联，请核对原流程")
	}
	for _, stage := range p.ProManualStages {
		if stage.Status == "running" {
			return p, errors.New("账号有步骤正在执行，请稍后关联")
		}
	}
	paidAccountID := ""
	if p.ProAuto.ID != "" {
		o, snapshot, e := s.store.GPTPayOrder(p.ProAuto.OrderID)
		if e != nil || o.Email != p.Email || o.Status != "success" {
			return p, errors.New("原订单尚未确认付款成功，不能恢复全自动后续步骤")
		}
		paidAccountID = strings.TrimSpace(snapshot.Input.Session.Account.ID)
	}
	v, password, _, err := s.store.ProSettings()
	if err != nil {
		return p, err
	}
	s.applySIWCQuotaInterval(&v)
	if v.Provider != "sub2" {
		return p, errors.New("当前推送设置不是 Sub2")
	}
	if err = validateSub2Connection(v.Sub2.URL, v.Sub2.Email, password); err != nil {
		return p, err
	}
	if len(v.Sub2.GroupIDs) == 0 {
		return p, errors.New("请先选择 Sub 推送分组")
	}
	a, err := s.sub2.FindExistingAccount(ctx, v.Sub2, password, email)
	if err != nil {
		return p, err
	}
	if a.SIWC() && p.ProAuto.ID != "" {
		if paidAccountID == "" {
			return p, errors.New("原付款订单缺少空间标识，无法核对网页额度账号")
		}
		for _, id := range []string{a.Credentials.WebAccountID, a.Credentials.AccountID} {
			if id != "" && id != paidAccountID {
				return p, errors.New("Sub 账号空间与原付款订单不一致，未进行关联")
			}
		}
	}
	current, currentPassword, _, err := s.store.ProSettings()
	if err != nil {
		return p, err
	}
	s.applySIWCQuotaInterval(&current)
	if current.Provider != v.Provider || !reflect.DeepEqual(current.Sub2, v.Sub2) || currentPassword != password {
		return p, errors.New("查询期间推送设置已改变，请重新关联")
	}
	if err = ctx.Err(); err != nil {
		return p, err
	}
	endpoint, err := sub2.EndpointKey(v.Sub2.URL)
	if err != nil {
		return p, err
	}
	target, err := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: v.Sub2, Secret: password})
	if err != nil {
		return p, err
	}
	same := p.PushProvider == "sub2" && p.PushStatus == "completed" && p.Sub2AccountID == a.ID && p.ProPushDownstream != nil && p.ProPushDownstream.Matches(model.ProDownstream(v))
	var web *model.SIWCLinkedWebCredentials
	if a.SIWC() {
		web = &model.SIWCLinkedWebCredentials{Endpoint: endpoint, AccountID: a.ID, Email: p.Email, WebAccountID: a.Credentials.WebAccountID}
		if web.WebAccountID == "" {
			web.WebAccountID = a.Credentials.AccountID
		}
		if paidAccountID != "" {
			web.WebAccountID = paidAccountID
		}
		if same && creds.SIWCWebLink != nil && creds.SIWCWebLink.Endpoint == endpoint && creds.SIWCWebLink.AccountID == a.ID {
			web = creds.SIWCWebLink
			if paidAccountID != "" {
				if web.WebAccountID != "" && web.WebAccountID != paidAccountID {
					return p, errors.New("已有网页额度空间与原付款订单不一致，未进行关联")
				}
				web.WebAccountID = paidAccountID
			}
		}
	}
	now := time.Now().UTC()
	p, err = s.store.LinkProSubAccount(email, p.UpdatedAt, web, func(p *model.MailAccountProfile) {
		if !same {
			p.ClearProCostSnapshot()
			p.Quota5H, p.Quota7D, p.QuotaCheckedAt = nil, nil, nil
			p.QuotaStatus = ""
			p.Sub2ProxyBindingID = ""
		}
		p.ProRestockTargetID, p.ProPendingPushTargetID = target, ""
		p.PushProvider, p.PushStatus = "sub2", "completed"
		p.Sub2AccountID, p.Sub2AccountName = a.ID, a.Name
		ref := model.ProDownstream(v)
		p.ProPushDownstream = &ref
		p.CPAAuthFileName, p.ProLastError = "", ""
		p.ProAuto.OAuthSource = "sub_existing"
		if p.ProQualitySince == nil {
			p.ProQualitySince = &now
		}
		if p.ProMigration != nil {
			p.ProMigration.Downstream = ref
		}
		if p.ProAuto.ID != "" {
			p.ProAuto.Sub2PushGroupIDs = slices.Clone(v.Sub2.GroupIDs)
			p.ProAuto.RestockBinding = restockBinding(v)
			p.ProAuto.SIWCConsentFailure = nil
			p.ProAuto.StopRequested = false
			p.ProAuto.Status, p.ProAuto.Stage, p.ProAuto.Error = "waiting_quota", "quota", ""
			p.ProAuto.NextCheckAt, p.ProAuto.RetryAt = &now, nil
			p.ProAuto.UpdatedAt = now
			if p.ProAuto.Steps == nil {
				p.ProAuto.Steps = map[string]string{}
			}
			// The remote account proves authorization happened externally. No
			// local OAuthStatus or token-present flag is fabricated.
			p.ProAuto.Steps["oauth"], p.ProAuto.Steps["push"], p.ProAuto.Steps["quota"] = "completed", "completed", "waiting"
			if p.ProAuto.StageTimes == nil {
				p.ProAuto.StageTimes = map[string]time.Time{}
			}
			for _, stage := range []string{"oauth", "push"} {
				if p.ProAuto.StageTimes[stage+":completed"].IsZero() {
					p.ProAuto.StageTimes[stage+":completed"] = now
				}
				delete(p.ProManualStages, stage)
				delete(p.ProStageOverrides, stage)
			}
			delete(p.ProManualStages, "quota")
			delete(p.ProStageOverrides, "quota")
			if p.ProAuto.QuotaIntervalSeconds < 1 {
				p.ProAuto.QuotaIntervalSeconds = v.QuotaCheckIntervalSeconds
			}
			if p.ProAuto.QuotaUsedThreshold <= 0 || p.ProAuto.QuotaUsedThreshold > 100 {
				p.ProAuto.QuotaUsedThreshold = v.QuotaUsedThreshold
			}
		}
	})
	if err == nil {
		s.auditProEvent(p, "sub_link", "completed", "sub2", "已关联手动导入的 Sub 账号，继续额度检测；未重新授权或推送", map[string]any{"account_id": a.ID, "group_ids": v.Sub2.GroupIDs, "authorization_source": "sub_existing"})
	}
	return p, err
}
