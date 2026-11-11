package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type proFallbackRuntime struct {
	mu          sync.Mutex
	lastCheck   time.Time
	lastError   string
	observation restock.FallbackObservation
	source      string
	rules       []fallbackRuleState
	tick        sync.Mutex
	// Config saves only coordinate with reconciliation, not slow monitoring reads.
	apply           sync.RWMutex
	clients         map[string]*sub2.Client
	next            time.Time
	configVersion   int
	fallbackVersion int
	now             func() time.Time
}

func (s *Server) fallbackNow() time.Time {
	if s.proFallback.now != nil {
		return s.proFallback.now()
	}
	return time.Now()
}
func (s *Server) fallbackClient(key string) *sub2.Client {
	s.proFallback.mu.Lock()
	defer s.proFallback.mu.Unlock()
	if s.proFallback.clients == nil {
		s.proFallback.clients = map[string]*sub2.Client{}
	}
	if s.proFallback.clients[key] == nil {
		s.proFallback.clients[key] = sub2.New()
	}
	return s.proFallback.clients[key]
}
func (s *Server) startProFallbackMonitor(ctx context.Context) {
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
				_ = s.tickProFallback(ctx, false)
			}
		}
	}()
}

// Never persist upstream response bodies: admin responses may contain account credentials.
func fallbackError(action string, err error) string {
	var h *sub2.HTTPError
	if errors.As(err, &h) {
		return fmt.Sprintf("%s失败（Sub2 HTTP %d），等待重试", action, h.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return action + "超时或取消，等待下轮核验"
	}
	return action + "失败，请检查连接、认证及接口响应；等待重试"
}
func (s *Server) tickProFallback(parent context.Context, force bool) (result error) {
	if !s.proFallback.tick.TryLock() {
		return errors.New("兜底正在核验，请稍后重试")
	}
	defer s.proFallback.tick.Unlock()
	now := s.fallbackNow()
	c, err := s.store.ProRestockConfig()
	if err != nil {
		return err
	}
	f, targetID, err := s.store.ProFallbackConfig()
	if err != nil {
		return err
	}
	if !force && now.Before(s.proFallback.next) && c.Version == s.proFallback.configVersion && f.Version == s.proFallback.fallbackVersion {
		return nil
	}
	s.proFallback.configVersion = c.Version
	s.proFallback.fallbackVersion = f.Version
	defer func() {
		s.proFallback.mu.Lock()
		defer s.proFallback.mu.Unlock()
		s.proFallback.lastCheck = s.fallbackNow()
		s.proFallback.lastError = ""
		if result != nil {
			s.proFallback.lastError = "兜底核验未完成，请检查逐账号状态、连接认证或本地存储；后台将继续重试"
		}
	}()
	defer func() {
		s.proFallback.next = now.Add(time.Duration(f.CheckSeconds) * time.Second)
		if result != nil {
			s.proFallback.next = s.fallbackNow().Add(time.Duration(f.CheckSeconds) * time.Second)
		}
	}()
	if err = f.Validate(); err != nil {
		return err
	}
	rows := s.collectFallbackRules(parent, f, c)
	s.proFallback.mu.Lock()
	s.proFallback.rules = rows
	if len(rows) > 0 {
		s.proFallback.observation, s.proFallback.source = rows[0].Monitor, rows[0].Source
	}
	s.proFallback.mu.Unlock()
	bindings, err := s.store.ProFallbackBindings()
	if err != nil {
		return err
	}
	managed := f.Enabled && len(f.AccountIDs) > 0
	selected := map[int64]bool{}
	var target model.DownstreamTarget
	if managed {
		target, err = s.store.DownstreamTarget(targetID)
		if err != nil {
			return errors.New("兜底连接读取失败")
		}
		for _, id := range f.AccountIDs {
			selected[id] = true
		}
	}
	existing := map[int64]bool{}
	for _, b := range bindings {
		if b.ConnectionID == targetID {
			existing[b.AccountID] = true
		}
	}
	// Record ownership only while this round's configuration is still current.
	err = func() error {
		s.proFallback.apply.RLock()
		defer s.proFallback.apply.RUnlock()
		if e := s.fallbackConfigCurrent(c.Version, f.Version, targetID); e != nil {
			return e
		}
		for _, id := range f.AccountIDs {
			if managed && !existing[id] {
				b := store.FallbackBinding{ConnectionID: targetID, GroupID: f.GroupID, AccountID: id}
				if err = s.store.SaveProFallbackBinding(b); err != nil {
					return err
				}
				bindings = append(bindings, b)
			}
		}
		return nil
	}()
	if err != nil {
		return err
	}
	// Reconcile current selections before draining old connections.
	sort.SliceStable(bindings, func(i, j int) bool {
		a := managed && bindings[i].ConnectionID == targetID && selected[bindings[i].AccountID]
		b := managed && bindings[j].ConnectionID == targetID && selected[bindings[j].AccountID]
		return a && !b
	})
	ctx, cancel := context.WithTimeout(parent, time.Duration(min(40, max(10, f.CheckSeconds*2)))*time.Second)
	defer cancel()
	reconcile := func(b store.FallbackBinding) error {
		// Writers wait only for the bounded account operation, never a whole round.
		// Pending writers also prevent subsequent accounts from using an old config.
		s.proFallback.apply.RLock()
		defer s.proFallback.apply.RUnlock()
		if e := s.fallbackConfigCurrent(c.Version, f.Version, targetID); e != nil {
			return e
		}
		var err error
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current := managed && b.ConnectionID == targetID && selected[b.AccountID]
		t, e := s.store.DownstreamTarget(b.ConnectionID)
		if e != nil {
			b.Error = "原兜底连接读取失败，无法核验"
			b.CheckedAt = s.fallbackNow()
			if err = s.store.SaveProFallbackBinding(b); err != nil {
				return err
			}
			return errors.New(b.Error)
		}
		// Password/administrator changes on the same server must not turn off a still-selected account.
		if managed && b.ConnectionID != targetID && t.Sub2.URL == target.Sub2.URL && selected[b.AccountID] {
			if err = s.store.DeleteProFallbackBinding(b); err != nil {
				return err
			}
			return nil
		}
		// Use refreshed administrator credentials to drain deselected accounts on the same endpoint.
		clientKey := b.ConnectionID
		if targetID != "" && b.ConnectionID != targetID {
			latest, e := s.store.DownstreamTarget(targetID)
			if e == nil && latest.Sub2.URL == t.Sub2.URL {
				t = latest
				clientKey = targetID
			}
		}
		b.Desired = false
		if current {
			b.GroupID = f.GroupID
		}
		client := s.fallbackClient(clientKey)
		accountCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		a, e := client.FallbackAccount(accountCtx, t.Sub2, t.Secret, b.AccountID)
		var h *sub2.HTTPError
		if errors.As(e, &h) && h.Status == 404 && !current {
			stop()
			if err = s.store.DeleteProFallbackBinding(b); err != nil {
				return err
			}
			return nil
		}
		b.Error = ""
		if e != nil {
			b.Observed = nil
			b.Error = fallbackError("读取兜底账号", e)
		} else {
			b.Name = a.Name
			b.Observed = a.Schedulable
			if !a.Supported() {
				b.Error = "账号已不是 OpenAI API Key / OAuth 类型，请人工检查"
			} else {
				hold := false
				if current {
					var group int64
					b.Desired, hold, group, b.Error = fallbackAccountPlan(f, rows, b.AccountID, &a, s.fallbackNow())
					if group > 0 {
						b.GroupID = group
					}
					if hold {
						b.Desired = *a.Schedulable
					}
				}
				if !hold && *a.Schedulable != b.Desired {
					if current {
						latest, e := s.store.ProRestockConfig()
						if e != nil {
							return e
						}
						fresh, wait, _, _ := fallbackAccountPlan(f, rows, b.AccountID, &a, s.fallbackNow())
						if latest.Version != c.Version || wait || fresh != b.Desired {
							return errors.New("配置或主组快照已变化，等待下轮核验")
						}
						p, password, _, pe := s.store.ProSettings()
						if pe != nil {
							return pe
						}
						for _, row := range rows {
							if !row.Rule.Enabled {
								continue
							}
							policy := latest
							policy.GroupID = row.Rule.MonitorGroupID
							if fallbackSourceKey(policy, p, password) != row.Source {
								return errors.New("主监控配置已变更，保持调度等待新采集")
							}
						}
					}
					// Write-ahead intent survives an ambiguous POST or a process crash.
					if err = s.store.SaveProFallbackBinding(b); err != nil {
						stop()
						return err
					}
					e = client.SetFallbackScheduling(accountCtx, t.Sub2, t.Secret, b.AccountID, b.Desired)
					if e != nil {
						b.Error = fallbackError("切换兜底调度", e)
						b.Observed = nil
					} else {
						v := b.Desired
						b.Observed = &v
					}
				}
			}
		}
		stop()
		b.CheckedAt = s.fallbackNow()
		if b.Error == "" && !current && b.Observed != nil && !*b.Observed {
			err = s.store.DeleteProFallbackBinding(b)
		} else {
			err = s.store.SaveProFallbackBinding(b)
		}
		if err != nil {
			return err
		}
		if b.Error != "" {
			return errors.New(b.Error)
		}
		return nil
	}
	jobs := make(chan store.FallbackBinding)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	var firstErr error
	for _, row := range rows {
		if row.Plan.Hold {
			firstErr = errors.New(row.Plan.Reason)
			break
		}
	}
	for i := 0; i < min(8, len(bindings)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				if e := reconcile(b); e != nil {
					resultMu.Lock()
					if firstErr == nil {
						firstErr = e
					}
					resultMu.Unlock()
				}
			}
		}()
	}
dispatch:
	for _, b := range bindings {
		select {
		case jobs <- b:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return firstErr
}

// Called under apply so API saves cannot race remote scheduling changes.
func (s *Server) fallbackConfigCurrent(restockVersion, fallbackVersion int, targetID string) error {
	c, err := s.store.ProRestockConfig()
	if err != nil {
		return err
	}
	f, target, err := s.store.ProFallbackConfig()
	if err != nil {
		return err
	}
	if c.Version != restockVersion || f.Version != fallbackVersion || target != targetID {
		return errors.New("配置已变化，等待下轮兜底核验")
	}
	return nil
}

func fallbackSourceKey(c restock.Config, p model.ProSettings, password string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", restockBinding(p), c.GroupID, password))))
}

// This sampler needs no quota/concurrency endpoint and never reads Pro task state.
func (s *Server) collectFallbackSource(ctx context.Context, c restock.Config) (restock.FallbackObservation, string) {
	o := restock.FallbackObservation{GroupID: c.GroupID}
	p, password, _, err := s.store.ProSettings()
	if err != nil {
		o.Error = "主 Sub2 推送配置读取失败"
		return o, ""
	}
	key := fallbackSourceKey(c, p, password)
	if c.GroupID <= 0 || p.Provider != "sub2" || p.Sub2.URL == "" || p.Sub2.Email == "" || password == "" {
		o.Error = "请先配置 Pro 推送 Sub2 连接和自动补货监控分组"
		return o, key
	}
	client := s.fallbackClient("primary")
	groups, err := client.Groups(ctx, p.Sub2, password)
	if err != nil {
		o.Error = fallbackError("读取主监控分组", err)
		return o, key
	}
	found := false
	for _, g := range groups {
		if g.ID == c.GroupID {
			found = true
			o.GroupName = g.Name
			break
		}
	}
	if !found {
		o.Error = "主监控分组不存在或无法访问，保持当前兜底调度"
		return o, key
	}
	accounts, err := client.MonitorAccounts(ctx, p.Sub2, password, c.GroupID)
	if err != nil {
		o.Error = fallbackError("读取主监控组账号", err)
		return o, key
	}
	count, err := sub2.CountGroupOAuth(accounts, c.GroupID)
	if err != nil {
		o.Error = err.Error()
		return o, key
	}
	o.Count, o.At = &count, s.fallbackNow()
	return o, key
}
