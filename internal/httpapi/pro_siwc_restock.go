package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

// Negative, endpoint-scoped IDs cannot collide with native positive Sub2 IDs.
// Retain the real ID separately for every outbound request.
func siwcRestockID(endpoint string, id int64) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", demandServerKey(endpoint), id)))
	return -int64(binary.BigEndian.Uint64(sum[:8])&((1<<53)-1)) - 1
}
func (s *Server) siwcRestockProfileID(p model.MailAccountProfile, primary model.ProSettings) int64 {
	if p.ProSIWC != nil && p.ProSIWC.AccountID > 0 {
		if target, e := s.store.DownstreamTarget(p.ProSIWC.TargetID); e == nil && demandServerKey(target.Sub2.URL) != demandServerKey(primary.Sub2.URL) {
			return siwcRestockID(target.Sub2.URL, p.Sub2AccountID)
		}
	}
	return p.Sub2AccountID
}
func (s *Server) restockAccountTarget(a restock.Account, p model.ProSettings, password string) (model.Sub2Settings, string, int64, error) {
	if a.TargetID == "" {
		return p.Sub2, password, a.ID, nil
	}
	target, e := s.store.DownstreamTarget(a.TargetID)
	return target.Sub2, target.Secret, a.RemoteID, e
}
func (s *Server) siwcQuotaPolicy() (model.ProSIWCSettings, error) {
	v, _, e := s.store.ProSIWCSettings()
	return v, e
}
func (s *Server) applySIWCQuotaInterval(v *model.ProSettings) {
	if v.Sub2.AuthMode == "siwc" {
		if cfg, e := s.siwcQuotaPolicy(); e == nil {
			v.Sub2.QuotaCheckIntervalSeconds = cfg.PollSeconds
		}
	}
}

func siwcQuotaSweepDue(p model.MailAccountProfile, now, retryAt time.Time, seconds int) bool {
	if now.Before(retryAt) {
		return false
	}
	return p.QuotaCheckedAt == nil || !now.Before(p.QuotaCheckedAt.Add(time.Duration(max(10, seconds))*time.Second))
}
func (s *Server) proQuotaExitReached(p model.MailAccountProfile, v model.ProSettings, threshold float64) bool {
	if p.SIWCTransferring() {
		return false
	}
	if p.ProSIWC != nil && p.ProSIWC.Status == "ended" {
		return true
	}
	if v.Sub2.AuthMode != "siwc" {
		return validProQuotaWindow(p.Quota7D) && p.Quota7D.UsedPercent >= threshold
	}
	cfg, e := s.siwcQuotaPolicy()
	if e != nil || p.QuotaCheckedAt == nil || time.Since(*p.QuotaCheckedAt) > 5*time.Minute {
		return false
	}
	return cfg.FiveHourEnabled && validProQuotaWindow(p.Quota5H) && p.Quota5H.UsedPercent >= 100-cfg.FiveHourRemaining || cfg.SevenDayEnabled && validProQuotaWindow(p.Quota7D) && p.Quota7D.UsedPercent >= 100-cfg.SevenDayRemaining
}
func applySIWCExitPolicy(a *restock.Account, cfg model.ProSIWCSettings) {
	a.SIWC = true
	a.QuotaMergeThreshold = nil
	a.Quota5HThreshold = nil
	if cfg.FiveHourEnabled {
		v := 100 - cfg.FiveHourRemaining
		a.Quota5HThreshold = &v
	}
	if cfg.SevenDayEnabled {
		v := 100 - cfg.SevenDayRemaining
		a.QuotaMergeThreshold = &v
	}
}

// All accounts in the selected SIWC monitor groups contribute demand and
// capacity. Multiple memberships and account IDs shared by two servers do not.
func (s *Server) appendSIWCRestock(ctx context.Context, c restock.Config, p model.ProSettings, profiles []model.MailAccountProfile, snap *restock.Snapshot) error {
	cfg, _, e := s.store.ProSIWCSettings()
	if e != nil {
		return e
	}
	if p.Sub2.AuthMode == "siwc" {
		for i := range snap.Accounts {
			applySIWCExitPolicy(&snap.Accounts[i], cfg)
		}
	}
	targets := map[string][]model.MailAccountProfile{}
	for _, v := range profiles {
		if v.ProSIWC != nil && v.ProSIWC.AccountID > 0 && !v.DispositionStarted() && !v.SpaceMergedOnce && v.ProSIWC.Status != "ended" {
			targets[v.ProSIWC.TargetID] = append(targets[v.ProSIWC.TargetID], v)
		}
	}
	seen := map[string]int{}
	for i, a := range snap.Accounts {
		seen[fmt.Sprintf("%s|%d", demandServerKey(p.Sub2.URL), a.ID)] = i
	}
	for targetID, owners := range targets {
		target, e := s.store.DownstreamTarget(targetID)
		if e != nil {
			return e
		}
		groups := []int64{}
		for _, owner := range owners {
			for _, group := range owner.ProSIWC.MonitorGroupIDs {
				if !slices.Contains(groups, group) {
					groups = append(groups, group)
				}
			}
		}
		if len(groups) == 0 {
			groups = target.Sub2.GroupIDs
		}
		accounts, loads, e := s.sub2.MonitorGroups(ctx, target.Sub2, target.Secret, groups)
		if e != nil {
			return fmt.Errorf("SIWC 监控：%w", e)
		}
		if !validDemandLoads(loads, time.Now(), c.FreshSeconds) || len(accounts) != len(loads.Account) {
			return errors.New("SIWC 并发数据过期或不完整，暂停新增")
		}
		if loads.At.Before(snap.At) {
			snap.At = loads.At
		}
		byID := map[int64]model.MailAccountProfile{}
		for _, v := range owners {
			byID[v.Sub2AccountID] = v
		}
		for _, a := range accounts {
			key := fmt.Sprintf("%s|%d", demandServerKey(target.Sub2.URL), a.ID)
			id := siwcRestockID(target.Sub2.URL, a.ID)
			if demandServerKey(target.Sub2.URL) == demandServerKey(p.Sub2.URL) {
				id = a.ID
			}
			v := restock.Account{ID: id, RemoteID: a.ID, SourceURL: target.Sub2.URL, TargetID: targetID, Name: a.Name, Plan: a.Credentials.Plan, Capacity: a.Concurrency, Ready: a.Ready(time.Now()), Shared: len(a.GroupIDs) > 1, Fault: a.Status == "error"}
			l, ok := loads.Account[a.ID]
			if !ok || l.AccountID != a.ID || l.Capacity != a.Concurrency || l.Used < 0 || l.Waiting < 0 || l.Known != nil && !*l.Known {
				return errors.New("SIWC 账号容量或并发数据不完整")
			}
			v.Used, v.Waiting = l.Used, l.Waiting
			s.proRestock.mu.Lock()
			v.Windows = slices.Clone(s.proRestock.quota[id])
			v.Money = s.proRestock.money[id]
			if s.proRestock.quotaPlans[id] != v.Plan {
				v.Windows = nil
			}
			s.proRestock.mu.Unlock()
			if local, ok := byID[a.ID]; ok {
				applyRestockExitPolicy(&v, local, p)
				v.PurchasePlan = local.ProAuto.QuotaPlan
				v.ForceMergeDeadline = local.ProPushDeadline()
				v.PushTimeoutEnabled = local.ProPushTimeoutEnabled
				if local.ProPushStartedAt != nil {
					v.ForceMergeKey = fmt.Sprintf("siwc:%s:%s:%s", targetID, local.ID, local.ProPushStartedAt.UTC())
				}
				if local.SIWCTransferring() {
					v.Ready = false
				}
			}
			applySIWCExitPolicy(&v, cfg)
			for _, w := range v.Windows {
				if w.Fresh(time.Now(), time.Duration(c.QuotaSeconds*2)*time.Second) && w.Used >= 100 {
					v.Ready = false
				}
			}
			if a.ExpiresAt != nil {
				at := time.Unix(*a.ExpiresAt, 0)
				v.ExpiresAt = &at
			}
			if index, ok := seen[key]; ok {
				v.Used = max(v.Used, snap.Accounts[index].Used)
				v.Waiting = max(v.Waiting, snap.Accounts[index].Waiting)
				snap.Accounts[index] = v
			} else {
				seen[key] = len(snap.Accounts)
				snap.Accounts = append(snap.Accounts, v)
			}
		}
	}
	return nil
}
