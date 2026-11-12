package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

func (s *Server) getProSIWCSettings(w http.ResponseWriter, r *http.Request) {
	v, _, e := s.store.ProSIWCSettings()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	writeAPI(w, 200, v, "")
}

func (s *Server) getProSub2Variant(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.store.ProSub2Variant(r.URL.Query().Get("auth_mode"))
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}

func (s *Server) proSIWCProxies(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sub2     model.Sub2Settings `json:"sub2"`
		Password string             `json:"sub2_password"`
	}
	if decodeJSON(w, r, &in, 2<<20) != nil {
		return
	}
	_, secret, e := s.store.ProSIWCSettings()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	if in.Password != "" {
		secret = in.Password
	}
	s.respondSub2Proxies(w, r, in.Sub2, secret)
}
func (s *Server) saveProSIWCSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		model.ProSIWCSettings
		Password string `json:"password"`
	}
	if decodeJSON(w, r, &in, 2<<20) != nil {
		return
	}
	v, e := s.store.SaveProSIWCSettings(in.ProSIWCSettings, in.Password)
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) proSIWCGroups(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sub2     model.Sub2Settings `json:"sub2"`
		Password string             `json:"password"`
	}
	if decodeJSON(w, r, &in, 2<<20) != nil {
		return
	}
	_, secret, e := s.store.ProSIWCSettings()
	if in.Password != "" {
		secret = in.Password
	}
	if e == nil {
		var groups []sub2.Group
		groups, e = s.sub2.Groups(r.Context(), in.Sub2, secret)
		if e == nil {
			writeAPI(w, 200, map[string]any{"groups": groups}, "")
			return
		}
	}
	writeAPI(w, 400, nil, e.Error())
}

func siwcCredentials(c model.SIWCCredentials) map[string]any {
	v := buildSIWCCredentialExport(c)["accounts"].([]any)[0].(map[string]any)["credentials"].(map[string]any)
	v["plan_type"], v["chatgpt_plan_type"] = "pro", "pro"
	return v
}
func (s *Server) ensureProSIWC(ctx context.Context, p model.MailAccountProfile, c model.MailAccountCredentials) (model.SIWCCredentials, error) {
	if c.SIWC != nil && c.SIWC.ExpiresAt > time.Now().Add(time.Minute).Unix() && c.SIWC.WebAccessToken != "" {
		if expiry, ok := workflow.AccessTokenExpiry(c.SIWC.WebAccessToken); ok && expiry.After(time.Now().Add(time.Minute)) {
			return *c.SIWC, nil
		}
	}
	result, e := s.executeOpenAILoginContext(ctx, p.Email, "siwc", func(message string) { s.auditProEvent(p, "siwc", "running", "sub2", message, nil) }, nil)
	if e != nil {
		return model.SIWCCredentials{}, e
	}
	if result["success"] != true {
		return model.SIWCCredentials{}, fmt.Errorf("SIWC 授权未完成：%v", result["error"])
	}
	raw, e := json.Marshal(result)
	var tokens model.SIWCCredentials
	if e == nil {
		e = json.Unmarshal(raw, &tokens)
	}
	if e == nil {
		e = s.store.SaveMailAccountSIWC(p.Email, tokens)
	}
	return tokens, e
}
func (s *Server) performDirectSIWCPush(ctx context.Context, p model.MailAccountProfile, c model.MailAccountCredentials, v model.ProSettings, password string) (model.MailAccountProfile, error) {
	tokens, e := s.ensureProSIWC(ctx, p, c)
	if e != nil {
		return p, e
	}
	if len(v.Sub2.GroupIDs) == 0 {
		return p, errors.New("请选择 SIWC 推送分组")
	}
	if p.ProPendingPushTargetID == "" && p.PushStatus == "completed" && p.ProPushDownstream != nil && p.ProPushDownstream.Matches(model.ProDownstream(v)) && p.Sub2AccountID > 0 {
		_, e = s.sub2.ApplyOAuthCredentials(ctx, v.Sub2, password, p.Sub2AccountID, siwcCredentials(tokens))
		return p, e
	}
	p, groups, e := s.pinProSub2Push(p, v, password)
	if e != nil {
		return p, e
	}
	input := sub2.CreateAccountInput{Name: proAccountName(p), Credentials: siwcCredentials(tokens), GroupIDs: groups, Models: v.Sub2.Models, Concurrency: v.Sub2.AccountConcurrency, Priority: v.Sub2.Priority}
	created, e := s.createSub2WithProxy(ctx, v.Sub2, password, input, "pro-siwc:"+p.ID, "pro-siwc-"+p.ID, nil)
	if e != nil {
		return p, e
	}
	target := p.ProRestockTargetID
	updated, e := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		p.ProRestockTargetID = target
		completeProPushTarget(p, v)
		p.PushProvider, p.PushStatus = "sub2", "completed"
		p.Sub2ProxyBindingID = created.ProxyBindingID
		p.Sub2AccountID, p.Sub2AccountName = created.ID, created.Name
		ref := model.ProDownstream(v)
		p.ProPushDownstream = &ref
		now := time.Now()
		p.ProQualitySince = &now
		p.QuotaCheckedAt = nil
		p.ProLastError = ""
	})
	if e == nil {
		s.completeSub2Push(v.Sub2, "pro-siwc:"+p.ID)
	}
	return updated, e
}

func (s *Server) proSIWCBlock() string {
	profiles, e := s.store.ProSIWCCandidates(context.Background(), true)
	if e != nil {
		return "SIWC 转入状态读取失败，暂停自动补货"
	}
	for _, p := range profiles {
		if p.SIWCTransferring() {
			if p.ProSIWC.Status == "warming" {
				return "SIWC 已推送，等待 10 秒运行并重新采集监控，暂停自动补货"
			}
			if p.ProSIWC.Status == "failed" {
				return "SIWC 转入失败，等待重试或手动结束；自动补货暂停"
			}
			return "账号正在转入 SIWC，暂停自动补货"
		}
	}
	return ""
}
func (s *Server) queueProSIWC(p model.MailAccountProfile, source model.ProSettings, password string) error {
	if source.Sub2.AuthMode == "siwc" || p.ProSIWC != nil {
		return nil
	}
	cfg, secret, e := s.store.ProSIWCSettings()
	if e != nil {
		return e
	}
	if cfg.Sub2.URL == "" || cfg.Sub2.Email == "" || secret == "" || len(cfg.Sub2.GroupIDs) == 0 || len(cfg.MonitorGroupIDs) == 0 {
		return errors.New("请先配置 SIWC 连接、推送分组和监控分组")
	}
	src, e := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-siwc-source", Provider: "sub2", Sub2: source.Sub2, Secret: password})
	if e != nil {
		return e
	}
	dst, e := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-siwc", Provider: "sub2", Sub2: cfg.Sub2, Secret: secret})
	if e != nil {
		return e
	}
	quality, e := s.store.ProQualitySettings()
	if e != nil {
		return e
	}
	remove := slices.Clone(quality.NormalGroupIDs)
	if len(remove) == 0 {
		remove = slices.Clone(source.Sub2.GroupIDs)
	}
	// The durable pause is committed before removing any service capacity.
	s.proCardAdmissionMu.Lock()
	defer s.proCardAdmissionMu.Unlock()
	_, e = s.store.UpdateProAccount(p.Email, func(v *model.MailAccountProfile) {
		if v.ProSIWC == nil {
			v.ProSIWC = &model.ProSIWCState{Status: "queued", Stage: "remove_groups", MonitorGroupIDs: slices.Clone(cfg.MonitorGroupIDs), RemoveGroupIDs: remove, SourceTargetID: src, TargetID: dst, SourceAccountID: p.Sub2AccountID, StartedAt: time.Now()}
			v.ProQuality.Action, v.ProQuality.ActionStatus = "siwc", "running"
		}
	})
	return e
}
func (s *Server) startProSIWCWorker(ctx context.Context) {
	s.proAutoMu.Lock()
	if s.proAutoClosed {
		s.proAutoMu.Unlock()
		return
	}
	s.proAutoWG.Add(1)
	s.proAutoMu.Unlock()
	go func() {
		defer s.proAutoWG.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				profiles, e := s.store.ProSIWCCandidates(ctx, false)
				if e != nil {
					continue
				}
				for _, p := range profiles {
					if !p.SIWCTransferring() {
						if p.ProSIWC != nil && p.ProSIWC.SourceAccountID > 0 && (p.ProSIWC.SourceCostCheckedAt == nil || time.Since(*p.ProSIWC.SourceCostCheckedAt) > 120*time.Second) && (p.ProSIWC.EndedAt == nil || time.Since(*p.ProSIWC.EndedAt) < 24*time.Hour) {
							call, cancel := context.WithTimeout(ctx, 15*time.Second)
							_ = s.refreshSIWCSourceCost(call, p)
							cancel()
						}
						continue
					}
					deadline := p.ProPushDeadline()
					expired := deadline != nil && !time.Now().Before(*deadline)
					if !expired && p.ProSIWC.RetryAt != nil && time.Now().Before(*p.ProSIWC.RetryAt) {
						continue
					}
					if !expired && p.ProSIWC.Status == "failed" && p.ProSIWC.Attempts >= 3 {
						continue
					}
					value, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
					lock := value.(*sync.Mutex)
					if !lock.TryLock() {
						continue
					}
					call, cancel := context.WithTimeout(ctx, 6*time.Minute)
					err := s.runProSIWC(call, p.Email)
					cancel()
					lock.Unlock()
					if err != nil {
						_, _ = s.store.UpdateProAccount(p.Email, func(v *model.MailAccountProfile) {
							if v.ProSIWC == nil || v.ProSIWC.Status == "ended" {
								return
							}
							v.ProSIWC.Status = "failed"
							v.ProSIWC.Error = err.Error()
							next := time.Now().Add(60 * time.Second)
							v.ProSIWC.RetryAt = &next
							v.ProQuality.ActionStatus, v.ProQuality.ActionError = "failed", err.Error()
						})
					}
				}
			}
		}
	}()
}
func (s *Server) runProSIWC(ctx context.Context, email string) error {
	p, c, e := s.store.MailAccountCredential(email)
	if e != nil {
		return e
	}
	v := p.ProSIWC
	if v == nil || !p.SIWCTransferring() {
		return nil
	}
	if deadline := p.ProPushDeadline(); deadline != nil && !time.Now().Before(*deadline) {
		return s.endSIWCService(ctx, p)
	}
	target, e := s.store.DownstreamTarget(v.TargetID)
	if e != nil {
		return e
	}
	if v.AccountID > 0 && v.Stage == "warm" {
		if v.ReadyAt != nil && time.Now().Before(*v.ReadyAt) {
			return nil
		}
		groups := v.MonitorGroupIDs
		if len(groups) == 0 {
			groups = target.Sub2.GroupIDs
		}
		as, loads, e := s.sub2.MonitorGroups(ctx, target.Sub2, target.Secret, groups)
		if e != nil {
			return e
		}
		ready := false
		for _, a := range as {
			if a.ID == v.AccountID && a.Ready(time.Now(), target.Sub2.Models) {
				l, ok := loads.Account[a.ID]
				ready = ok && l.AccountID == a.ID && l.Capacity == a.Concurrency && l.Used >= 0 && l.Waiting >= 0 && (l.Known == nil || *l.Known) && validDemandLoads(loads, time.Now(), 30) && len(as) == len(loads.Account) && (v.ReadyAt == nil || !loads.At.Before(*v.ReadyAt))
			}
		}
		if !ready {
			return errors.New("SIWC 已创建，等待账号可调度及并发数据")
		}
		_, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if p.ProSIWC.Status == "ended" {
				return
			}
			p.ProSIWC.Status, p.ProSIWC.Error = "active", ""
			p.ProQuality.ActionStatus = "completed"
			p.ProSIWC.RetryAt = nil
		})
		return e
	}
	_, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProSIWC.Attempts++; p.ProSIWC.Error = "" })
	if e != nil {
		return e
	}
	if v.Stage == "remove_groups" {
		src, e := s.store.DownstreamTarget(v.SourceTargetID)
		if e != nil {
			return e
		}
		groups, e := s.sub2.AccountGroups(ctx, src.Sub2, src.Secret, v.SourceAccountID)
		if e != nil {
			return e
		}
		remove := v.RemoveGroupIDs
		if len(remove) == 0 {
			remove = src.Sub2.GroupIDs
		}
		wanted := []int64{}
		for _, id := range groups {
			if !slices.Contains(remove, id) {
				wanted = append(wanted, id)
			}
		}
		if v.SourceGroupIDs == nil {
			if _, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProSIWC.SourceGroupIDs = slices.Clone(groups) }); e != nil {
				return e
			}
		}
		if e = s.sub2.SetAccountGroups(ctx, src.Sub2, src.Secret, v.SourceAccountID, wanted); e != nil {
			return e
		}
		costs, e := s.sub2.QueryTotalCosts(ctx, src.Sub2, src.Secret, v.SourceAccountID)
		if e != nil {
			return e
		}
		p, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			now := time.Now()
			p.ProSIWC.SourceCostCheckedAt = &now
			p.ProSIWC.CarriedCost = max(p.CostDownstreamSnapshot, costs.StandardCostUSD)
			p.ProSIWC.CarriedUserCost = p.UserCostDownstreamSnapshot
			if costs.UserCostUSD != nil {
				p.ProSIWC.CarriedUserCost = max(p.ProSIWC.CarriedUserCost, *costs.UserCostUSD)
			}
			p.ProSIWC.Stage = "authorize"
			p.ProSIWC.Status = "authorizing"
		})
		if e != nil {
			return e
		}
		v = p.ProSIWC
	}
	tokens, e := s.ensureProSIWC(ctx, p, c)
	if e != nil {
		return e
	}
	latest, _, e := s.store.MailAccountCredential(email)
	if e != nil {
		return e
	}
	if !latest.SIWCTransferring() {
		return nil
	}
	if deadline := latest.ProPushDeadline(); deadline != nil && !time.Now().Before(*deadline) {
		return errors.New("原推送超时已到，等待超时收尾")
	}
	if v.AccountID == 0 {
		_, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProSIWC.Status = "pushing" })
		if e != nil {
			return e
		}
		input := sub2.CreateAccountInput{Name: proAccountName(p) + "+siwc", Credentials: siwcCredentials(tokens), GroupIDs: target.Sub2.GroupIDs, Models: target.Sub2.Models, Concurrency: target.Sub2.AccountConcurrency, Priority: target.Sub2.Priority}
		created, e := s.createSub2WithProxy(ctx, target.Sub2, target.Secret, input, "siwc-transfer:"+p.ID, "siwc-transfer-"+p.ID, nil)
		if e != nil {
			return e
		}
		p, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProSIWC.AccountID = created.ID
			p.ProSIWC.Stage = "warm"
			p.ProSIWC.Status = "warming"
			now := time.Now()
			ready := now.Add(10 * time.Second)
			p.ProSIWC.ReadyAt = &ready
			p.ProSIWC.RetryAt = nil
			p.Sub2ProxyBindingID = created.ProxyBindingID
			p.Sub2AccountID, p.Sub2AccountName = created.ID, created.Name
			p.ProPushDownstream = &model.ProDownstreamRef{Provider: "sub2", URL: target.Sub2.URL, LoginEmail: target.Sub2.Email}
			p.CostProvider, p.CostConnectionIdentity, p.CostDownstreamIdentity = "sub2", proCostConnectionIdentity(target.Sub2), fmt.Sprint(created.ID)
			p.UserCostDownstreamIdentity = fmt.Sprint(created.ID)
			p.CostDownstreamSnapshot = p.ProSIWC.CarriedCost
			p.UserCostDownstreamSnapshot = p.ProSIWC.CarriedUserCost
			p.Quota5H, p.Quota7D = nil, nil
			p.QuotaCheckedAt = nil
			p.ProQualitySince = &now
		})
		if e != nil {
			return e
		}
		s.completeSub2Push(target.Sub2, "siwc-transfer:"+p.ID)
	} else {
		_, e = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProSIWC.Status = "warming" })
	}
	return e
}

func (s *Server) manageProSIWC(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	var in struct {
		Action string `json:"action"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	if in.Action != "retry" && in.Action != "end" {
		writeAPI(w, 400, nil, "操作无效")
		return
	}
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "SIWC 请求正在执行，请等待当前请求结束")
		return
	}
	defer lock.Unlock()
	current, _, readErr := s.store.MailAccountCredential(email)
	if readErr != nil {
		writeAPI(w, 400, nil, readErr.Error())
		return
	}
	if in.Action == "end" {
		if e := s.endSIWCService(r.Context(), current); e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
		current, _, _ = s.store.MailAccountCredential(email)
		writeAPI(w, 200, current, "")
		return
	}
	if current.ProSIWC == nil || current.ProSIWC.Status == "ended" || current.ProSIWC.Status == "active" || current.DispositionStarted() || current.SpaceMergedOnce {
		writeAPI(w, 409, nil, "当前状态不能重试转入")
		return
	}
	p, e := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.ProSIWC == nil {
			return
		}
		if in.Action == "retry" {
			p.ProSIWC.Attempts = 0
			p.ProSIWC.Status = "queued"
			if p.ProSIWC.AccountID > 0 {
				p.ProSIWC.Status = "warming"
			}
			p.ProSIWC.RetryAt = nil
		} else {
			p.ProSIWC.Status = "ended"
			now := time.Now()
			p.ProSIWC.EndedAt = &now
		}
	})
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	writeAPI(w, 200, p, "")
}
