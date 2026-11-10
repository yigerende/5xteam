package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func (s *Server) takeoverNow() time.Time {
	if s.proTakeover.now != nil {
		return s.proTakeover.now()
	}
	return time.Now()
}

func (s *Server) takeoverPending() (int, error) {
	tasks, e := s.store.ProRestockTasks()
	if e != nil {
		return 0, e
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		if t.Active() {
			seen[t.Email] = true
		}
	}
	s.proRestock.mu.Lock()
	for _, t := range s.proRestock.tasks {
		if t.Active() && t.DecisionID == "" {
			seen[t.Email] = true
		}
	}
	s.proRestock.mu.Unlock()
	// Include manually launched chains before the next restock observation.
	profiles, e := s.store.ProUnpushedProfiles(context.Background())
	if e != nil {
		return 0, e
	}
	for _, p := range profiles {
		if p.PushStatus != "completed" && s.proAutomationRunning(p.Email) {
			seen[p.Email] = true
		}
	}
	return len(seen), nil
}

func (s *Server) acceptTakeoverHeartbeat(w http.ResponseWriter, r *http.Request) {
	var request takeoverHeartbeat
	if decodeJSON(w, r, &request, 32<<10) != nil {
		return
	}
	if request.Pending < 0 || request.Covered < 0 {
		writeAPI(w, 400, nil, "无效状态")
		return
	}
	s.proTakeover.mu.Lock()
	defer s.proTakeover.mu.Unlock()
	c, _, e := s.store.ProTakeoverConfig()
	p, _, _, pe := s.store.ProSettings()
	rc, re := s.store.ProRestockConfig()
	st, se := s.store.ProTakeoverState()
	if e != nil || pe != nil || re != nil || se != nil {
		writeAPI(w, 500, nil, "接管状态读取失败")
		return
	}
	if c.Role != "receiver" || request.ID == "" || request.ID != c.PeerID || !request.Downstream.Matches(model.ProDownstream(p)) {
		writeAPI(w, 409, nil, "接管角色、对方身份或 Sub2 连接不一致")
		return
	}
	groups := c.Groups
	if st.Active() {
		groups = st.Groups
	}
	if c.Enabled && (len(groups) == 0 || !slices.Contains(groups, rc.GroupID)) {
		writeAPI(w, 409, nil, "接管分组必须包含本项目监控分组")
		return
	}
	for _, g := range groups {
		if slices.Contains(request.PushGroups, g) {
			writeAPI(w, 409, nil, "接管分组与对方常驻分组重复")
			return
		}
	}
	now := s.takeoverNow()
	pending, e := s.takeoverPending()
	if e != nil {
		writeAPI(w, 500, nil, "在途状态读取失败")
		return
	}
	start, end, wanted := rc.TakeoverWindow(now, c.LeadMinutes)
	wanted = wanted && c.Enabled && rc.Enabled && rc.Mode == "auto" && p.AutoCardProvider() == "ejh" && request.Willing
	st.RecoverAtEnd(now)
	if !st.Active() && wanted {
		st = restock.TakeoverState{Epoch: randomRegistrationID(), Phase: "preparing", PeerID: request.ID, Groups: slices.Clone(groups), Start: start, End: end}
	}
	if st.Active() {
		// Old epoch acknowledgements cannot release a new handoff.
		current := request.Epoch == st.Epoch
		st.ContactAt = now
		st.Pending = pending
		if current {
			st.PeerPending = request.Pending
			st.Covered = request.Covered
		}
		if wanted {
			st.Start, st.End = start, end
			st.Phase = "preparing"
			if !now.Before(start) {
				st.Phase = "active"
			}
			st.ReadyCount = 0
			st.RecoveryAt = time.Time{}
			if current && pending == 0 {
				st.LeaseUntil = minTakeoverTime(end, now.Add(time.Minute))
			} else if pending > 0 {
				st.LeaseUntil = now
			}
		} else if st.Taking() {
			// A shortened window or manual stop also waits for real replacements.
			st.BeginRecovery(now)
		}
		if st.Phase == "released" && current && !request.Released {
			snapshot, err := s.store.ProRestockSnapshot()
			ready, _, _, _ := restock.TakeoverRecoveryReady(rc, c, snapshot, pending, st.RecoveryAt, now)
			if err != nil || !ready || st.ReadyCount < c.ReturnConfirmations || !rc.Enabled || rc.Mode != "auto" || rc.NoRestockReason(now) != "" || (!st.EndReached(now) && request.Pending > 0) {
				st.BeginRecovery(now)
			}
		}
		if st.Phase == "released" && current && request.Released && (st.EndReached(now) || request.Pending == 0) {
			st = restock.TakeoverState{Phase: "idle", ContactAt: now}
		}
		st.Error = ""
		if e = s.store.SaveProTakeoverState(st); e != nil {
			writeAPI(w, 500, nil, "接管状态保存失败")
			return
		}
	}
	info, e := s.takeoverInfo()
	if e != nil {
		writeAPI(w, 500, nil, "接管状态读取失败")
		return
	}
	writeAPI(w, 200, info, "")
}
func minTakeoverTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Server) startProTakeoverMonitor(ctx context.Context) {
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
				s.tickProTakeover(ctx)
			}
		}
	}()
}
func (s *Server) tickProTakeover(ctx context.Context) {
	ctx, cancelPass := context.WithTimeout(ctx, 45*time.Second)
	defer cancelPass()
	if !s.proTakeover.mu.TryLock() {
		return
	}
	defer s.proTakeover.mu.Unlock()
	c, password, e := s.store.ProTakeoverConfig()
	if e != nil {
		return
	}
	st, e := s.store.ProTakeoverState()
	if e != nil || (!c.Enabled && !st.Active()) {
		return
	}
	ended := st.RecoverAtEnd(s.takeoverNow())
	if ended {
		if e = s.store.SaveProTakeoverState(st); e != nil {
			return
		}
	}
	if c.Role != "provider" || (!ended && time.Now().Before(s.proTakeover.next)) {
		return
	}
	defer func() { s.proTakeover.next = time.Now().Add(time.Duration(c.CheckSeconds) * time.Second) }()
	fail := func(message string) { st.Error = message; _ = s.store.SaveProTakeoverState(st) }
	p, secret, _, e := s.store.ProSettings()
	if e != nil {
		fail("Pro 设置读取失败")
		return
	}
	rc, e := s.store.ProRestockConfig()
	if e != nil {
		fail("补货设置读取失败")
		return
	}
	pending, e := s.takeoverPending()
	if e != nil {
		fail("在途读取失败")
		return
	}
	bindings, e := s.store.ProTakeoverBindings()
	if e != nil {
		fail("临时分组记录读取失败")
		return
	}
	st.Covered = len(bindings)
	request := takeoverHeartbeat{ID: s.store.TakeoverInstanceID(), Epoch: st.Epoch, Willing: c.Enabled && rc.Enabled && rc.Mode == "auto" && p.AutoCardProvider() == "nexovcc", Pending: pending, Covered: len(bindings), Released: len(bindings) == 0, Downstream: model.ProDownstream(p), PushGroups: p.Sub2.GroupIDs}
	var peer takeoverPeerInfo
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	e = takeoverPeerCall(callCtx, c, password, http.MethodPost, request, &peer)
	cancel()
	if e == nil {
		e = s.validateTakeoverPeer(c, p, peer)
	}
	if e != nil {
		fail(e.Error())
		return
	}
	old := st
	st = peer.State
	st.CoverageReady = old.CoverageReady && old.Epoch == st.Epoch
	st.Pending = pending
	st.PeerPending = peer.State.Pending
	st.ContactAt = s.takeoverNow()
	st.Error = ""
	st.RecoverAtEnd(s.takeoverNow())
	if len(bindings) > 0 && !st.Active() {
		st = old
		fail("对方未保留接管记录，保留现有临时分组并暂停新增，请核对对方数据")
		return
	}
	if e = s.store.SaveProTakeoverState(st); e != nil {
		return
	}
	if st.Taking() && s.takeoverNow().Before(st.LeaseUntil) && c.Enabled {
		if e = s.reconcileTakeoverGroups(ctx, p, secret, st, false); e != nil {
			st.CoverageReady = false
			fail(e.Error())
			return
		}
		st.CoverageReady = true
		_ = s.store.SaveProTakeoverState(st)
	} else if st.Phase == "released" && (st.EndReached(s.takeoverNow()) || pending == 0) {
		if e = s.reconcileTakeoverGroups(ctx, p, secret, st, true); e != nil {
			fail(e.Error())
			return
		}
	}
}

// Called after a real fresh monitor round, never from a timer or cached page.
func (s *Server) observeTakeoverRecovery(c restock.Config, snap restock.Snapshot, tasks []restock.Task) {
	if !s.proTakeover.mu.TryLock() {
		return
	}
	defer s.proTakeover.mu.Unlock()
	tc, _, e := s.store.ProTakeoverConfig()
	st, se := s.store.ProTakeoverState()
	if e != nil || se != nil || tc.Role != "receiver" || (st.Phase != "recovering" && st.Phase != "released") {
		return
	}
	pending := 0
	for _, t := range tasks {
		if t.Active() {
			pending++
		}
	}
	ready, count, capacity, demand := restock.TakeoverRecoveryReady(c, tc, snap, pending, st.RecoveryAt, s.takeoverNow())
	st.OwnReady, st.OwnCapacity, st.Demand, st.Pending = count, capacity, demand, pending
	if !c.Enabled || c.Mode != "auto" || c.NoRestockReason(s.takeoverNow()) != "" || st.Block(tc, s.takeoverNow()) != "" || snap.TakeoverToken != st.Token(tc) {
		ready = false
	}
	if st.Phase == "released" {
		// Failed rounds are not persisted as the last good snapshot. Revoke the
		// pending release here too, before a peer can use that earlier sample.
		if !ready {
			st.BeginRecovery(s.takeoverNow())
		}
		_ = s.store.SaveProTakeoverState(st)
		return
	}
	if snap.At.After(st.LastReadySample) {
		st.LastReadySample = snap.At
		if ready {
			st.ReadyCount++
		} else {
			st.ReadyCount = 0
		}
	} else if !ready {
		st.ReadyCount = 0
	}
	if st.ReadyCount >= tc.ReturnConfirmations {
		st.Phase = "released"
	}
	_ = s.store.SaveProTakeoverState(st)
}

func (s *Server) takeoverOwnedProfiles(p model.ProSettings, provider string) (map[int64]model.MailAccountProfile, error) {
	profiles, e := s.store.ProSupplyProfiles(context.Background())
	if e != nil {
		return nil, e
	}
	emails := []string{}
	for _, v := range profiles {
		emails = append(emails, v.Email)
	}
	cards, e := s.store.ProActivationCards(emails)
	if e != nil {
		return nil, e
	}
	out := map[int64]model.MailAccountProfile{}
	duplicates := map[int64]bool{}
	for _, v := range profiles {
		if v.Sub2AccountID <= 0 || v.PushStatus != "completed" || v.PushProvider != "sub2" || v.SpaceMergedOnce || restockAccountExiting(v) || v.ProWorkflowRunning || v.ProQuality.Routed || v.ProQuality.ActionStatus == "running" {
			continue
		}
		card := cards[strings.ToLower(strings.TrimSpace(v.Email))]
		if provider == "nexovcc" && card.NexovccCardID == "" || provider == "ejh" && card.EJHCardID == "" {
			continue
		}
		target, _, _, err := s.proAccountPushSettings(v)
		if err != nil || !model.ProDownstream(target).Matches(model.ProDownstream(p)) {
			continue
		}
		if _, exists := out[v.Sub2AccountID]; exists {
			duplicates[v.Sub2AccountID] = true
		}
		out[v.Sub2AccountID] = v
	}
	for id := range duplicates {
		delete(out, id)
	}
	return out, nil
}

// Called under the account lock after persisting a successful Sub2 push.
// Persist only the missing temporary memberships before sending the update,
// so a process restart or an ambiguous response can be retried safely.
func (s *Server) attachProPushTakeover(ctx context.Context, p model.ProSettings, password string, profile model.MailAccountProfile) error {
	if profile.SpaceMergedOnce || restockAccountExiting(profile) || profile.ProQuality.Routed {
		return nil
	}
	s.proTakeover.mu.Lock()
	defer s.proTakeover.mu.Unlock()
	c, _, err := s.store.ProTakeoverConfig()
	if err != nil || !c.Enabled || c.Role != "provider" {
		return err
	}
	st, err := s.store.ProTakeoverState()
	if err != nil || !st.Taking() || st.EndReached(s.takeoverNow()) || !s.takeoverNow().Before(st.LeaseUntil) {
		return err
	}
	current, _, _, err := s.store.ProSettings()
	if err != nil || !model.ProDownstream(current).Matches(model.ProDownstream(p)) {
		return err
	}
	cards, err := s.store.ProActivationCards([]string{profile.Email})
	if err != nil || cards[strings.ToLower(strings.TrimSpace(profile.Email))].NexovccCardID == "" {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	groups, err := s.sub2.AccountGroups(ctx, p.Sub2, password, profile.Sub2AccountID)
	if err != nil {
		return err
	}
	bindings, err := s.store.ProTakeoverBindings()
	if err != nil {
		return err
	}
	target, err := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-takeover", Provider: "sub2", Sub2: p.Sub2, Secret: password})
	if err != nil {
		return err
	}
	b := store.TakeoverBinding{AccountID: profile.Sub2AccountID, TargetID: target, Epoch: st.Epoch}
	for _, existing := range bindings {
		if existing.AccountID == b.AccountID && existing.TargetID == target {
			b = existing
			break
		}
	}
	wanted := slices.Clone(groups)
	for _, g := range st.Groups {
		if !slices.Contains(wanted, g) {
			wanted = append(wanted, g)
			if !slices.Contains(b.Added, g) {
				b.Added = append(b.Added, g)
			}
		}
	}
	if slices.Equal(groups, wanted) || st.EndReached(s.takeoverNow()) || !s.takeoverNow().Before(st.LeaseUntil) {
		return nil
	}
	if err = s.store.SaveProTakeoverBinding(b); err != nil {
		return err
	}
	return s.sub2.SetAccountGroups(ctx, p.Sub2, password, profile.Sub2AccountID, wanted)
}

func (s *Server) reconcileTakeoverGroups(ctx context.Context, p model.ProSettings, password string, st restock.TakeoverState, remove bool) error {
	bindings, e := s.store.ProTakeoverBindings()
	if e != nil {
		return e
	}
	byID := map[int64]store.TakeoverBinding{}
	for _, b := range bindings {
		byID[b.AccountID] = b
	}
	owned, e := s.takeoverOwnedProfiles(p, "nexovcc")
	if e != nil {
		return e
	}
	if !remove {
		rc, err := s.store.ProRestockConfig()
		if err != nil {
			return err
		}
		if len(p.Sub2.GroupIDs) == 0 {
			return errors.New("常驻推送分组为空，暂停接管挂组")
		}
		listing, err := s.sub2.MonitorAccounts(ctx, p.Sub2, password, rc.GroupID)
		if err != nil {
			return err
		}
		available := map[int64]sub2.MonitorAccount{}
		for _, a := range listing {
			if a.Ready(s.takeoverNow(), p.Sub2.Models) {
				available[a.ID] = a
			}
		}
		target, e := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-takeover", Provider: "sub2", Sub2: p.Sub2, Secret: password})
		if e != nil {
			return e
		}
		for id, v := range owned {
			observed, ok := available[id]
			if !ok {
				continue
			}
			attached := true
			for _, g := range st.Groups {
				if !slices.Contains(observed.GroupIDs, g) {
					attached = false
				}
			}
			if attached {
				continue
			}
			if !s.takeoverNow().Before(st.LeaseUntil) {
				return errors.New("接管授权到期，下一轮续期后继续挂组")
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lockValue, _ := s.proLocks.LoadOrStore("account:"+v.Email, &sync.Mutex{})
			lock := lockValue.(*sync.Mutex)
			if !lock.TryLock() {
				continue
			}
			e = func() error {
				defer lock.Unlock()
				latest, _, err := s.store.MailAccountCredential(v.Email)
				if err != nil {
					return err
				}
				if restockAccountExiting(latest) || latest.ProWorkflowRunning || latest.ProQuality.Routed {
					return nil
				}
				check, cancel := context.WithTimeout(ctx, 12*time.Second)
				defer cancel()
				groups, err := s.sub2.AccountGroups(check, p.Sub2, password, id)
				if err != nil {
					return err
				}
				if !slices.Contains(groups, rc.GroupID) {
					return nil
				}
				b, exists := byID[id]
				if exists && b.TargetID != target {
					pinned, pe := s.store.DownstreamTarget(b.TargetID)
					if pe != nil || !model.ProDownstream(model.ProSettings{Provider: pinned.Provider, Sub2: pinned.Sub2}).Matches(model.ProDownstream(p)) {
						return errors.New("账号原接管连接不同，保留临时分组等待核对")
					}
				}
				if !exists {
					b = store.TakeoverBinding{AccountID: id, TargetID: target, Epoch: st.Epoch}
				}
				wanted := slices.Clone(groups)
				for _, g := range st.Groups {
					if !slices.Contains(wanted, g) {
						wanted = append(wanted, g)
						if !slices.Contains(b.Added, g) {
							b.Added = append(b.Added, g)
						}
					}
				}
				if len(b.Added) == 0 {
					return nil
				}
				// Persist intention before the remote write. Timeout/restart retries read
				// actual memberships and never adopt unrelated pre-existing groups.
				if err = s.store.SaveProTakeoverBinding(b); err != nil {
					return err
				}
				if !slices.Equal(groups, wanted) {
					return s.sub2.SetAccountGroups(check, p.Sub2, password, id, wanted)
				}
				return nil
			}()
			if e != nil {
				return e
			}
		}
		return nil
	}
	for _, b := range bindings {
		check, cancel := context.WithTimeout(ctx, 12*time.Second)
		e = func() error {
			defer cancel()
			// The original encrypted target survives later configuration changes.
			target, err := s.store.DownstreamTarget(b.TargetID)
			if err != nil {
				return err
			}
			var unlock func()
			profiles, err := s.store.ProSub2Profiles(check, b.AccountID)
			if err != nil {
				return err
			}
			for _, v := range profiles {
				if v.Sub2AccountID == b.AccountID {
					lockValue, _ := s.proLocks.LoadOrStore("account:"+v.Email, &sync.Mutex{})
					lock := lockValue.(*sync.Mutex)
					if !lock.TryLock() {
						return errors.New("账号正在处理，稍后撤回临时分组")
					}
					unlock = lock.Unlock
					break
				}
			}
			if unlock != nil {
				defer unlock()
			}
			groups, err := s.sub2.AccountGroups(check, target.Sub2, target.Secret, b.AccountID)
			if err != nil {
				var remote *sub2.HTTPError
				if errors.As(err, &remote) && remote.Status == http.StatusNotFound {
					return s.store.DeleteProTakeoverBinding(b)
				}
				return err
			}
			wanted := []int64{}
			for _, g := range groups {
				if !slices.Contains(b.Added, g) {
					wanted = append(wanted, g)
				}
			}
			if !slices.Equal(groups, wanted) {
				if err = s.sub2.SetAccountGroups(check, target.Sub2, target.Secret, b.AccountID, wanted); err != nil {
					return err
				}
			}
			return s.store.DeleteProTakeoverBinding(b)
		}()
		if e != nil {
			return e
		}
	}
	return nil
}
