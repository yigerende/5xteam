package httpapi

import (
	"errors"
	"slices"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

// A queued workflow is not a submitted downstream create. Resolve the current
// settings at push time, retaining a connection only for existing accounts or
// an encrypted create attempt whose outcome still needs reconciliation.
func (s *Server) proNewPushSettings(p model.MailAccountProfile) (model.ProSettings, string, string, error) {
	if p.ProPendingPushTargetID != "" {
		return s.proPendingPushSettings(p)
	}
	pinned, password, key, err := s.proAccountPushSettings(p)
	if err != nil || p.Sub2AccountID > 0 || p.CPAAuthFileName != "" || p.VM2 != nil || p.ProSIWC != nil {
		return pinned, password, key, err
	}
	if pinned.Provider == "sub2" && pinned.Sub2.URL != "" {
		endpoint, e := sub2.EndpointKey(pinned.Sub2.URL)
		if e != nil {
			return pinned, password, key, e
		}
		for _, scope := range []string{"pro:" + p.ID, "pro-siwc:" + p.ID} {
			raw, e := s.store.Sub2PushAttempt(sub2PushAttemptID(endpoint, scope))
			if e != nil || raw != "" {
				return pinned, password, key, e
			}
		}
	}
	v, password, key, err := s.store.ProSettings()
	s.applySIWCQuotaInterval(&v)
	return v, password, key, err
}

func (s *Server) proPendingPushSettings(p model.MailAccountProfile) (model.ProSettings, string, string, error) {
	v, _, key, err := s.store.ProSettings()
	if err != nil {
		return v, "", key, err
	}
	t, err := s.store.DownstreamTarget(p.ProPendingPushTargetID)
	if err != nil {
		return v, "", key, err
	}
	if t.Provider != "sub2" {
		return v, "", key, errors.New("待确认推送连接类型不匹配")
	}
	v.Provider, v.Sub2 = "sub2", t.Sub2
	s.applySIWCQuotaInterval(&v)
	return v, t.Secret, key, nil
}

func (s *Server) proHasPushAttempt(p model.MailAccountProfile, v model.ProSettings) (bool, error) {
	if v.Provider != "sub2" || v.Sub2.URL == "" {
		return false, nil
	}
	endpoint, err := sub2.EndpointKey(v.Sub2.URL)
	if err != nil {
		return false, err
	}
	for _, scope := range []string{"pro:" + p.ID, "pro-siwc:" + p.ID} {
		raw, err := s.store.Sub2PushAttempt(sub2PushAttemptID(endpoint, scope))
		if err != nil || raw != "" {
			return raw != "", err
		}
	}
	return false, nil
}

// Only an explicit single/batch push selects the latest configuration for an
// already-bound account. Keep its working binding until the new create commits.
func (s *Server) prepareProManualPush(p model.MailAccountProfile) (model.MailAccountProfile, error) {
	v, password, _, err := s.store.ProSettings()
	if err != nil {
		return p, err
	}
	if p.ProPendingPushTargetID != "" {
		pending, _, _, err := s.proPendingPushSettings(p)
		if err != nil {
			return p, err
		}
		exists, err := s.proHasPushAttempt(p, pending)
		if err != nil || exists {
			return p, err
		}
		p, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { p.ProPendingPushTargetID = "" })
		if err != nil {
			return p, err
		}
	}
	// VM slots and managed SIWC transfers keep their existing lifecycle.
	if p.VM2 != nil || p.ProSIWC != nil || v.Provider != "sub2" {
		return p, nil
	}
	old, _, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return p, err
	}
	exists, err := s.proHasPushAttempt(p, old)
	if err != nil || exists {
		return p, err
	}
	if err = validateSub2Connection(v.Sub2.URL, v.Sub2.Email, password); err != nil {
		return p, err
	}
	if len(v.Sub2.GroupIDs) == 0 {
		return p, errors.New("请选择 Pro 的 Sub2 OpenAI 分组")
	}
	id, err := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: v.Sub2, Secret: password})
	if err != nil {
		return p, err
	}
	updated, err := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) { p.ProPendingPushTargetID = id })
	if err == nil {
		s.auditProEvent(updated, "push", "running", "sub2", "手动推送采用当前已保存的 Sub2 配置；成功后更新本地绑定，首次推送计时保留", map[string]any{"previous_target_id": p.ProRestockTargetID, "previous_account_id": p.Sub2AccountID, "target_id": id, "group_ids": v.Sub2.GroupIDs, "bind_proxy": v.Sub2.BindProxy, "proxy_ids": v.Sub2.ProxyIDs})
	}
	return updated, err
}

func completeProPushTarget(p *model.MailAccountProfile, v model.ProSettings) {
	if p.ProPendingPushTargetID == "" {
		return
	}
	p.ProRestockTargetID, p.ProPendingPushTargetID = p.ProPendingPushTargetID, ""
	p.ProAuto.Sub2PushGroupIDs = slices.Clone(v.Sub2.GroupIDs)
	if p.ProAuto.ID != "" || p.ProRestockTaskID != "" {
		p.ProAuto.RestockBinding = restockBinding(v)
	}
	// Account IDs can be identical on two sites. Never carry an old site's quota
	// or cost snapshot into the new identity; retained financial history remains.
	p.ClearProCostSnapshot()
	p.Quota5H, p.Quota7D, p.QuotaCheckedAt = nil, nil, nil
	p.QuotaStatus = ""
}

// Pin the settings actually used before a request can leave this process.
// Quota checks, cleanup and uncertain retries must address that same site.
func (s *Server) pinProSub2Push(p model.MailAccountProfile, v model.ProSettings, password string) (model.MailAccountProfile, []int64, error) {
	groups := slices.Clone(v.Sub2.GroupIDs)
	if p.ProPendingPushTargetID != "" {
		// The prepared encrypted target is separate from the working binding.
		return p, groups, nil
	}
	if p.ProRestockTargetID != "" && len(p.ProAuto.Sub2PushGroupIDs) > 0 {
		previous, err := s.store.DownstreamTarget(p.ProRestockTargetID)
		if err != nil {
			return p, nil, err
		}
		old := model.ProSettings{Provider: previous.Provider, Sub2: previous.Sub2}
		if model.ProDownstream(old).Matches(model.ProDownstream(v)) && old.Sub2.AuthMode == v.Sub2.AuthMode {
			// Preserve the purchase window's extra groups, but replace its old
			// base selection. Group IDs must never cross downstream sites.
			for _, id := range p.ProAuto.Sub2PushGroupIDs {
				if !slices.Contains(previous.Sub2.GroupIDs, id) && !slices.Contains(groups, id) {
					groups = append(groups, id)
				}
			}
		}
	}
	id, err := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: v.Sub2, Secret: password})
	if err != nil {
		return p, nil, err
	}
	previousID := p.ProRestockTargetID
	p, err = s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		p.ProRestockTargetID = id
		if len(p.ProAuto.Sub2PushGroupIDs) > 0 {
			p.ProAuto.Sub2PushGroupIDs = slices.Clone(groups)
		}
		if p.ProAuto.ID != "" || p.ProRestockTaskID != "" {
			p.ProAuto.RestockBinding = restockBinding(v)
		}
	})
	if err == nil && previousID != id {
		s.auditProEvent(p, "push", "running", "sub2", "本次推送采用最新保存的 Sub2 配置", map[string]any{
			"bind_proxy": v.Sub2.BindProxy, "proxy_ids": v.Sub2.ProxyIDs,
			"group_ids": groups, "concurrency": v.Sub2.AccountConcurrency, "priority": v.Sub2.Priority,
		})
	}
	return p, groups, err
}
