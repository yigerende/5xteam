package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/sub2"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type proRestockContextKey struct{}
type proRestockRuntime struct {
	money            map[int64]*restock.AccountMoney
	moneyNext        time.Time
	moneyBusy        bool
	moneyScope       string
	moneyGeneration  uint64
	tasks            []restock.Task
	tick             sync.Mutex
	mu               sync.Mutex
	next             time.Time
	quotaNext        time.Time
	quotaBusy        bool
	quota            map[int64][]restock.Window
	quotaPlans       map[int64]string
	quotaBinding     string
	lastSnapshot     restock.Snapshot
	liveSnapshot     restock.Snapshot // Collected data, separate from the last evaluated sample.
	monitorState     proRestockMonitorState
	decision         restock.Decision
	exitConfirmation int
	confirmation     int
	lastVersion      int
	lastSample       time.Time
	lastRecord       time.Time
	lastReason       string
	monitor          func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error)
}

func restockBinding(p model.ProSettings) string {
	b, _ := json.Marshal(struct {
		Ref    model.ProDownstreamRef
		Groups []int64
		Models []string
		Slots  int
	}{model.ProDownstream(p), p.Sub2.GroupIDs, p.Sub2.Models, p.Sub2.AccountConcurrency})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func restockPlan(provider, code, country string, quantity int) string {
	return fmt.Sprintf("%s:%s:%s:%d", provider, code, country, quantity)
}
func validateRestockTarget(c restock.Config, p model.ProSettings) error {
	if p.Provider != "sub2" {
		return errors.New("Pro 自动补货仅支持当前 Sub2 推送目标")
	}
	for _, g := range p.Sub2.GroupIDs {
		if g == c.GroupID {
			return nil
		}
	}
	return errors.New("监控分组必须包含在 Pro 推送目标分组中")
}
func (s *Server) startProRestockMonitor(ctx context.Context) {
	s.startProFallbackMonitor(ctx)
	s.startProTakeoverMonitor(ctx)
	if saved, err := s.store.ProRestockSnapshot(); err == nil && saved.Valid && saved.LoadValid {
		s.proRestock.lastSnapshot = saved
		s.proRestock.quotaBinding = saved.Binding
		s.proRestock.quota = map[int64][]restock.Window{}
		s.proRestock.quotaPlans = map[int64]string{}
		for _, a := range saved.Accounts {
			s.proRestock.quota[a.ID] = a.Windows
			s.proRestock.quotaPlans[a.ID] = a.Plan
		}
	}
	s.proAutoWG.Add(1)
	go func() {
		defer s.proAutoWG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tickProRestock(ctx, false)
			}
		}
	}()
}
func (s *Server) collectProRestock(ctx context.Context, c restock.Config, p model.ProSettings, password string) (restock.Snapshot, error) {
	now := time.Now()
	snap := restock.Snapshot{ID: randomRegistrationID(), At: now, CollectionStartedAt: now, GroupID: c.GroupID, Binding: restockBinding(p), Accounts: []restock.Account{}}
	if e := validateRestockTarget(c, p); e != nil {
		return snap, e
	}
	// Local profile reads can wait for the store lock. Do them before taking
	// the upstream concurrency sample so they do not consume its freshness.
	profiles, e := s.store.ProSupplyProfiles(ctx)
	if e != nil {
		return snap, e
	}
	groups, e := s.sub2.Groups(ctx, p.Sub2, password)
	if e != nil {
		return snap, e
	}
	tc, _, e := s.store.ProTakeoverConfig()
	if e != nil {
		return snap, e
	}
	ts, e := s.store.ProTakeoverState()
	if e != nil {
		return snap, e
	}
	snap.TakeoverToken = ts.Token(tc)
	snap.MonitorGroups = restock.TakeoverGroups(c.GroupID, tc, ts)
	for _, id := range snap.MonitorGroups {
		found := false
		for _, g := range groups {
			if g.ID == id {
				found = true
			}
		}
		if !found {
			return snap, errors.New("监控或接管分组不存在或无法访问")
		}
	}
	accounts, loads, e := s.sub2.MonitorGroups(ctx, p.Sub2, password, snap.MonitorGroups)
	if e != nil {
		return snap, e
	}
	own := map[int64]model.MailAccountProfile{}
	scoped := ts.Active() && (tc.Role == "receiver" || ts.Taking())
	if scoped {
		issuer := "ejh"
		if tc.Role == "provider" {
			issuer = "nexovcc"
		}
		own, e = s.takeoverOwnedProfiles(p, issuer)
		if e != nil {
			return snap, e
		}
	}
	pay, _, payErr := s.store.GPTPaySettings()
	if payErr != nil {
		return snap, payErr
	}
	_, pay, payErr = s.resolveProPurchase(p, pay, c, time.Now())
	if payErr != nil {
		return snap, payErr
	}
	snap.NewPlan = restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)

	// Compare server timestamps with collection completion, not request start.
	now = time.Now()
	snap.At = loads.At
	snap.Valid = true
	snap.LoadValid = len(loads.Account) == len(accounts)
	if !snap.LoadValid {
		snap.Error = "Sub2 账号列表与并发负载数量不一致，暂停新增并等待重新采集"
	}
	// Native Sub2 has no quality extension. Honor explicit failures when present.
	if q := loads.Quality; q != nil && (!q.Complete || q.Expected != len(accounts) || q.Succeeded != len(accounts) || q.Failed != 0) {
		snap.LoadValid = false
		snap.Error = "Sub2 报告并发采集不完整，暂停新增并等待重新采集"
	}
	byID := map[int64]model.MailAccountProfile{}
	monitored := make(map[int64]bool, len(accounts))
	for _, a := range accounts {
		monitored[a.ID] = true
	}
	duplicates := map[int64]bool{}
	for _, v := range profiles {
		if !monitored[v.Sub2AccountID] || v.PushStatus != "completed" || v.PushProvider != "sub2" || (!v.IsProDead() && (v.DispositionStarted() || v.SpaceMergedOnce || v.RemoveStatus == "completed" || v.RemoveStatus == "team_removed")) {
			continue
		}
		matched := false
		if v.ProPushDownstream != nil {
			matched = v.ProPushDownstream.Matches(model.ProDownstream(p))
		} else if v.ProRestockTargetID != "" {
			target, _, _, err := s.proAccountPushSettings(v)
			if err != nil {
				return snap, err
			}
			matched = model.ProDownstream(target).Matches(model.ProDownstream(p))
		} else if v.ProMigration != nil {
			matched = v.ProMigration.Downstream.Matches(model.ProDownstream(p))
		} else {
			matched = v.ProAuto.RestockBinding == snap.Binding
		}
		if !matched {
			continue
		}
		if _, ok := byID[v.Sub2AccountID]; ok {
			duplicates[v.Sub2AccountID] = true
		}
		byID[v.Sub2AccountID] = v
	}
	for id := range duplicates {
		delete(byID, id)
	}
	emails := make([]string, 0, len(byID))
	for _, v := range byID {
		emails = append(emails, v.Email)
	}
	cards, e := s.store.ProActivationCards(emails)
	if e != nil {
		return snap, e
	}
	s.proRestock.mu.Lock()
	if s.proRestock.quotaBinding != snap.Binding {
		s.proRestock.quota = map[int64][]restock.Window{}
		s.proRestock.quotaPlans = map[int64]string{}
		s.proRestock.quotaBinding = snap.Binding
		s.proRestock.quotaNext = time.Time{}
	}
	members := make([]string, 0, len(accounts))
	for _, a := range accounts {
		members = append(members, fmt.Sprintf("%d:%s:%t", a.ID, a.Credentials.Plan, a.Ready(now, p.Sub2.Models)))
	}
	sort.Strings(members)
	moneyScope := fmt.Sprintf("%s:%v:%t:%s:%v", snap.Binding, snap.MonitorGroups, scoped, tc.Role, members)
	if moneyScope != s.proRestock.moneyScope {
		s.proRestock.moneyScope = moneyScope
		s.proRestock.money = map[int64]*restock.AccountMoney{}
		s.proRestock.moneyNext = time.Time{}
		s.proRestock.moneyGeneration++
	}
	for _, a := range accounts {
		v := restock.Account{Fault: a.Status == "error" || (a.OverloadUntil != nil && a.OverloadUntil.After(now)) || (a.TempUntil != nil && a.TempUntil.After(now)), ID: a.ID, Name: a.Name, Plan: a.Credentials.Plan, Ready: a.Ready(now, p.Sub2.Models), Shared: len(a.GroupIDs) > 1, Capacity: a.Concurrency, Windows: append([]restock.Window{}, s.proRestock.quota[a.ID]...)}
		v.Money = s.proRestock.money[a.ID]
		if scoped {
			_, local := own[a.ID]
			complete := true
			for _, g := range snap.MonitorGroups {
				if !slices.Contains(a.GroupIDs, g) {
					complete = false
				}
			}
			v.SupplyExcluded = !local || !complete
			v.TakeoverPending = local && !complete
		}
		if a.ExpiresAt != nil {
			t := time.Unix(*a.ExpiresAt, 0)
			v.ExpiresAt = &t
		}
		if local, ok := byID[a.ID]; ok {
			if local.IsProDead() {
				v.Ready = false
				v.SupplyExcluded = true
				v.Fault = false
			}
			applyRestockExitPolicy(&v, local, p)
			if local.ProAuto.QuotaPlan != "" {
				v.PurchasePlan = local.ProAuto.QuotaPlan
			}
			card := cards[strings.ToLower(strings.TrimSpace(local.Email))]
			if card.EJHCardID != "" || card.NexovccCardID != "" {
				v.EJH = card.EJHCardID != ""
				v.CardProvider = "ejh"
				if card.NexovccCardID != "" {
					v.CardProvider = "nexovcc"
				}
				v.PushTimeoutEnabled = local.ProPushTimeoutEnabled
				v.ForceMergeDeadline = local.ProPushDeadline()
				if local.ProPushStartedAt != nil && !local.ProPushStartedAt.After(now.Add(5*time.Second)) {
					key, _ := json.Marshal([]any{model.ProDownstream(p), c.GroupID, local.ID, local.Email, local.ProPushStartedAt.UTC()})
					v.ForceMergeKey = fmt.Sprintf("%x", sha256.Sum256(key))
				} else {
					v.ForceMergeDeadline = nil
				}
			}
		}
		if s.proRestock.quotaPlans[a.ID] != v.Plan {
			v.Windows = nil
		}
		load, ok := loads.Account[a.ID]
		if !ok || load.AccountID != a.ID || load.Used < 0 || load.Waiting < 0 || (load.Known != nil && !*load.Known) || load.Capacity != a.Concurrency {
			snap.LoadValid = false
			snap.Error = "账号成员、并发容量或采集质量不一致"
		} else {
			v.Used, v.Waiting = load.Used, load.Waiting
		}
		for _, w := range v.Windows {
			if w.Fresh(now, time.Duration(c.QuotaSeconds*2)*time.Second) && w.Used >= 100 {
				v.Ready = false
			}
		}
		snap.Accounts = append(snap.Accounts, v)
	}
	s.proRestock.mu.Unlock()
	now = time.Now()
	if now.Sub(snap.At) > time.Duration(c.FreshSeconds)*time.Second || snap.At.After(now.Add(5*time.Second)) {
		snap.Valid = false
		snap.Error = "Sub2 快照时间过期或服务器时钟不一致"
	}
	if e := s.appendSIWCRestock(ctx, c, p, profiles, &snap); e != nil {
		snap.Valid = false
		snap.LoadValid = false
		snap.Error = e.Error()
	}
	s.refreshRestockQuota(c, p, password, snap)
	if snap.Valid && snap.LoadValid {
		s.refreshRestockMoney(c, p, password, snap)
	}
	return snap, nil
}
func (s *Server) refreshRestockQuota(c restock.Config, p model.ProSettings, password string, snap restock.Snapshot) {
	s.proRestock.mu.Lock()
	if s.proRestock.quotaBusy || time.Now().Before(s.proRestock.quotaNext) {
		s.proRestock.mu.Unlock()
		return
	}
	s.proRestock.quotaBusy = true
	s.proRestock.quotaNext = time.Now().Add(time.Duration(c.QuotaSeconds) * time.Second)
	s.proRestock.mu.Unlock()
	s.proAutoMu.Lock()
	if s.proAutoClosed {
		s.proAutoMu.Unlock()
		s.proRestock.mu.Lock()
		s.proRestock.quotaBusy = false
		s.proRestock.mu.Unlock()
		return
	}
	s.proAutoWG.Add(1)
	s.proAutoMu.Unlock()
	go func() {
		defer s.proAutoWG.Done()
		defer func() { s.proRestock.mu.Lock(); s.proRestock.quotaBusy = false; s.proRestock.mu.Unlock() }()
		jobs := make(chan restock.Account)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for a := range jobs {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					target, secret, id, e := s.restockAccountTarget(a, p, password)
					var q sub2.QuotaUsage
					if e == nil {
						q, e = s.sub2.QueryQuota(ctx, target, secret, id)
					}
					cancel()
					if e != nil {
						continue
					}
					f, se := q.Windows()
					windows := []restock.Window{}
					for _, w := range []*model.FreeQuotaWindow{f, se} {
						if w != nil {
							at := time.Now()
							reset := w.ResetAt
							if reset == 0 && w.ResetAfterSeconds > 0 {
								reset = at.Unix() + w.ResetAfterSeconds
							}
							windows = append(windows, restock.Window{Seconds: w.LimitWindowSeconds, ResetAt: reset, Used: w.UsedPercent, At: at})
						}
					}
					windows = restock.ObserveWindows(windows, a.Windows, true, time.Duration(c.QuotaSeconds*3)*time.Second)
					s.proRestock.mu.Lock()
					if s.proRestock.quotaBinding == snap.Binding {
						s.proRestock.quota[a.ID] = windows
						s.proRestock.quotaPlans[a.ID] = a.Plan
					}
					s.proRestock.mu.Unlock()
				}
			}()
		}
		for _, a := range snap.Accounts {
			s.proAutoMu.Lock()
			closed := s.proAutoClosed
			s.proAutoMu.Unlock()
			if closed {
				break
			}
			jobs <- a
		}
		close(jobs)
		wg.Wait()
	}()
}
