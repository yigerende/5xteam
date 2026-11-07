package httpapi

import (
	"slices"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

// A queued workflow is not a submitted downstream create. Resolve the current
// settings at push time, retaining a connection only for existing accounts or
// an encrypted create attempt whose outcome still needs reconciliation.
func (s *Server) proNewPushSettings(p model.MailAccountProfile) (model.ProSettings, string, string, error) {
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

// Pin the settings actually used before a request can leave this process.
// Quota checks, cleanup and uncertain retries must address that same site.
func (s *Server) pinProSub2Push(p model.MailAccountProfile, v model.ProSettings, password string) (model.MailAccountProfile, []int64, error) {
	groups := slices.Clone(v.Sub2.GroupIDs)
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
