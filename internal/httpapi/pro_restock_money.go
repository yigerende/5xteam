package httpapi

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/sub2"
)

type restockMoneyAccount struct {
	identity string
	epoch    uint64
}

// A roster change invalidates only that account. A whole-target/group change
// still starts a new scope. Epochs also reject a late read after leave/rejoin.
func (s *Server) prepareRestockMoney(scope string, snap *restock.Snapshot) {
	s.proRestock.mu.Lock()
	defer s.proRestock.mu.Unlock()
	r := &s.proRestock
	if r.moneyScope != scope || r.moneyAccounts == nil {
		r.moneyScope = scope
		r.money = map[int64]*restock.AccountMoney{}
		r.moneyAccounts = map[int64]restockMoneyAccount{}
		r.moneyNext = time.Time{}
		r.moneyGeneration++
	}
	seen := make(map[int64]bool, len(snap.Accounts))
	for i := range snap.Accounts {
		a := &snap.Accounts[i]
		seen[a.ID] = true
		key, _ := json.Marshal([]any{a.Plan, a.PurchasePlan, a.Ready, a.TargetID, demandServerKey(a.SourceURL), a.RemoteID})
		entry, exists := r.moneyAccounts[a.ID]
		if !exists || entry.identity != string(key) {
			r.moneySequence++
			entry = restockMoneyAccount{identity: string(key), epoch: r.moneySequence}
			r.moneyAccounts[a.ID] = entry
			delete(r.money, a.ID)
		}
		a.MoneyEpoch, a.Money = entry.epoch, r.money[a.ID]
	}
	for id := range r.moneyAccounts {
		if !seen[id] {
			delete(r.moneyAccounts, id)
			delete(r.money, id)
		}
	}
}

func restockMoneyUsage(u sub2.MoneyUsage, now time.Time) restock.AccountMoney {
	m := restock.AccountMoney{At: now, Day: u.Day}
	if u.UpdatedAt == nil || u.UpdatedAt.After(now.Add(5*time.Second)) || now.Sub(*u.UpdatedAt) > restock.MoneyMaxAge {
		m.Error = "数据过期"
		return m
	}
	m.BudgetAt = *u.UpdatedAt
	for i, w := range []*sub2.MoneyWindow{u.FiveHour, u.SevenDay} {
		if w == nil {
			continue
		}
		// Sub2 returns a zero-length, 0% placeholder for an unused 5h window.
		if w.Utilization != nil && *w.Utilization == 0 {
			continue
		}
		if w.Utilization == nil || w.ResetsAt == nil || w.Stats == nil || w.Stats.Cost == nil || !w.ResetsAt.After(now) {
			m.Error = "额度窗口或费用缺失"
			return m
		}
		if math.IsNaN(*w.Utilization) || math.IsInf(*w.Utilization, 0) || *w.Utilization < 0 || *w.Utilization > 100 || math.IsNaN(*w.Stats.Cost) || math.IsInf(*w.Stats.Cost, 0) || *w.Stats.Cost < 0 {
			m.Error = "额度费用数据无效"
			return m
		}
		seconds := int64(18000)
		if i == 1 {
			seconds = 604800
		}
		m.Windows = append(m.Windows, restock.MoneyWindow{Seconds: seconds, ResetAt: *w.ResetsAt, UsedPercent: *w.Utilization, UsedUSD: *w.Stats.Cost})
	}
	return m
}

// A configured concurrency interval can exceed three minutes. Continue cost
// sampling between those checks, without advancing pressure confirmations or
// using a stale group snapshot to make purchase decisions.
func (s *Server) sampleRestockMoneyBetweenChecks(c restock.Config, now time.Time) {
	if !c.Enabled {
		return
	}
	s.proRestock.mu.Lock()
	snap := s.proRestock.lastSnapshot
	if s.proRestock.moneyBusy || now.Before(s.proRestock.moneyNext) || !snap.Valid || !snap.LoadValid || snap.GroupID != c.GroupID || snap.At.IsZero() || now.Sub(snap.At) > time.Duration(c.FreshSeconds)*time.Second || snap.At.After(now.Add(5*time.Second)) {
		s.proRestock.mu.Unlock()
		return
	}
	snap.Accounts = append([]restock.Account(nil), snap.Accounts...)
	for i := range snap.Accounts {
		snap.Accounts[i].Money = s.proRestock.money[snap.Accounts[i].ID]
	}
	s.proRestock.mu.Unlock()
	p, password, _, err := s.store.ProSettings()
	if err != nil || snap.Binding != restockBinding(p) || validateRestockTarget(c, p) != nil {
		return
	}
	s.refreshRestockMoney(c, p, password, snap)
}

// Money sampling is independent of the quota-percent refresh interval: a long
// quota interval must not change the configured cost calculation window.
func (s *Server) refreshRestockMoney(c restock.Config, p model.ProSettings, password string, snap restock.Snapshot) {
	s.proRestock.mu.Lock()
	if s.proRestock.moneyBusy || time.Now().Before(s.proRestock.moneyNext) {
		s.proRestock.mu.Unlock()
		return
	}
	s.proRestock.moneyBusy = true
	s.proRestock.moneyNext = time.Now().Add(restock.ExitRefreshInterval(c, snap, time.Now()))
	generation := s.proRestock.moneyGeneration
	s.proRestock.mu.Unlock()
	s.proAutoMu.Lock()
	if s.proAutoClosed {
		s.proAutoMu.Unlock()
		s.proRestock.mu.Lock()
		s.proRestock.moneyBusy = false
		s.proRestock.mu.Unlock()
		return
	}
	s.proAutoWG.Add(1)
	s.proAutoMu.Unlock()
	go func() {
		defer s.proAutoWG.Done()
		defer func() { s.proRestock.mu.Lock(); s.proRestock.moneyBusy = false; s.proRestock.mu.Unlock() }()
		jobs := make(chan restock.Account)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for a := range jobs {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					target, secret, id, err := s.restockAccountTarget(a, p, password)
					var u sub2.MoneyUsage
					if err == nil {
						u, err = s.sub2.MonitorMoney(ctx, target, secret, id)
					}
					cancel()
					m := restock.AccountMoney{At: time.Now(), Error: "费用读取失败"}
					if err == nil {
						m = restockMoneyUsage(u, time.Now())
						m = restock.ObserveMoney(m, a.Money, u.DailyCost, c.MoneyWindowMinutes)
					}
					s.proRestock.mu.Lock()
					entry, present := s.proRestock.moneyAccounts[a.ID]
					if generation == s.proRestock.moneyGeneration && s.proRestock.quotaBinding == snap.Binding && present && entry.epoch == a.MoneyEpoch {
						if s.proRestock.money == nil {
							s.proRestock.money = map[int64]*restock.AccountMoney{}
						}
						s.proRestock.money[a.ID] = &m
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
			if a.Ready {
				jobs <- a
			}
		}
		close(jobs)
		wg.Wait()
	}()
}
