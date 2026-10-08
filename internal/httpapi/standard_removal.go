package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

var errStandardRemovalPending = errors.New("转普通后移出尚未完成")

func (s *Server) saveStandardRemoval(ctx context.Context, p *model.FreeAccountProfile, stage, message string, delay time.Duration) error {
	next := *p.StandardRemoval
	next.Stage, next.Message, next.NextAt = stage, message, time.Now().Add(delay).UTC()
	updated, err := s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(v *model.FreeAccountProfile) {
		v.StandardRemoval = &next
		v.Status, v.RemoveStatus, v.LastError = "removing", "running", ""
		if stage == "failed" {
			v.Status, v.RemoveStatus, v.LastError = "remove_failed", "failed", message
		}
	})
	if err != nil {
		return err
	}
	*p = updated
	s.auditAccountEvent(ctx, p.ID, "standard_removal", "remove", "standard_removal", "", message, map[string]any{"phase": stage, "remove_method": next.Method, "team_account_id": p.TeamAccountID, "ordinary_seat_reserved": next.Lane})
	return nil
}

func (s *Server) waitStandardRemoval(ctx context.Context, p *model.FreeAccountProfile, stage, message string, delay time.Duration) (model.FreeAccountProfile, error) {
	if err := s.saveStandardRemoval(ctx, p, stage, message, delay); err != nil {
		return *p, err
	}
	return *p, fmt.Errorf("%w：%s", errStandardRemovalPending, message)
}

// Caller owns both the child removal lock and the mother mutation lock.
// All uncertain outcomes are reconciled from live membership before any retry.
func (s *Server) performStandardRemoval(ctx context.Context, p model.FreeAccountProfile, method string, force bool) (out model.FreeAccountProfile, resultErr error) {
	if p.StandardRemoval == nil {
		p.StandardRemoval = &model.StandardRemoval{Method: method, FlowID: recoveryID(), MutationID: recoveryID(), RetryLimit: s.store.AutoRotationSettings().RetryCount}
	} else {
		copy := *p.StandardRemoval
		p.StandardRemoval = &copy
		if force || p.Dead || p.ReloginExhausted || p.Quality.Excluded {
			p.StandardRemoval.Method = "mother_kick"
		}
		if p.StandardRemoval.Stage == "failed" {
			p.StandardRemoval.Failures = 0
		}
	}
	if err := s.saveStandardRemoval(ctx, &p, "checking", "正在核实成员席位，准备转普通后移出", 0); err != nil {
		return p, err
	}
	defer func() {
		if resultErr == nil || errors.Is(resultErr, errStandardRemovalPending) {
			return
		}
		p.StandardRemoval.Failures++
		stage := "retry_wait"
		if p.StandardRemoval.Failures > p.StandardRemoval.RetryLimit {
			stage = "failed"
		}
		message := redactSensitiveText(resultErr.Error())
		if ctx.Err() != nil {
			stage = "retry_wait"
			p.StandardRemoval.Failures--
			message = "操作中断，下次先核实实际席位与成员状态"
		}
		if err := s.saveStandardRemoval(context.WithoutCancel(ctx), &p, stage, message, 30*time.Second); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		out = p
	}()
	if p.RemoteRemovedAt != nil && !p.StandardRemoval.Lane {
		// Departure and ordinary vacancy were already verified and persisted.
		// A downstream retry must not depend on the mother staying online.
		p, err := s.finishRemovedCycle(ctx, p)
		return p, err
	}
	if wait := time.Until(p.StandardRemoval.MutationAfter); wait > 0 {
		return s.waitStandardRemoval(ctx, &p, "cooldown", "等待同母号操作间隔结束后继续核实", wait)
	}
	admin, client, token, err := s.recoveryAdmin(ctx, p.AdminAccountID, p.TeamAccountID)
	if err != nil {
		return p, err
	}
	if p.UserID == admin.UserID || strings.EqualFold(p.Email, admin.Email) {
		return p, errors.New("禁止切换或移出空间所有者")
	}
	adminSettings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return p, err
	}
	s.auditAccountEvent(ctx, p.ID, "standard_removal_proxy", "remove", "standard_removal", "", "席位查询与切换使用母号专属代理", s.adminProxyLogDetails(adminSettings, admin))
	ctx = workflow.WithRequestObserver(ctx, func(o workflow.RequestObservation) {
		if o.Phase != "start" {
			s.auditAccountEvent(ctx, p.ID, "standard_removal_request", "remove", "standard_removal", "", fmt.Sprintf("%s %s：HTTP %d", o.Method, o.URL, o.StatusCode), map[string]any{"http_status": o.StatusCode, "duration_ms": o.Duration.Milliseconds(), "transport": o.Transport})
		}
	})
	member := func() (*workflow.TeamMember, error) {
		m, e := client.FindMemberByEmail(ctx, token, p.TeamAccountID, p.Email)
		if e != nil {
			return nil, e
		}
		if m != nil && (m.ID != p.UserID || m.Role == "account-owner" || m.Role == "owner") {
			return nil, errors.New("实时成员身份不匹配，未执行移出")
		}
		if m != nil && !m.Active {
			return nil, nil
		}
		return m, nil
	}
	m, err := member()
	if err != nil {
		return p, err
	}
	if m != nil {
		if p.RemoteRemovedAt != nil {
			return p, errors.New("本轮已记录离开，但远端再次出现该成员，未继续操作")
		}
		if m.SeatType != "prolite" && m.SeatType != "default" {
			return p, errors.New("未知实际席位类型，未执行切换或移出")
		}
		var leaveClient *workflow.Client
		var childAT string
		if p.StandardRemoval.Method == "child_leave" {
			_, credentials, e := s.store.MailAccountCredential(p.Email)
			if e != nil {
				return p, e
			}
			childAT = strings.TrimSpace(credentials.AccessToken)
			if childAT == "" {
				return p, errors.New("子号缺少 AT，无法自行退出，请获取 AT 后重试")
			}
			leaveClient, err = workflow.NewClient(s.store.Settings())
			if err != nil {
				return p, err
			}
		}
		if !p.StandardRemoval.Lane {
			p.StandardRemoval.Lane = true
			err = s.saveStandardRemoval(ctx, &p, "waiting_seat", "等待普通席位串行位置", 0)
			if errors.Is(err, store.ErrStandardSeatBusy) {
				p.StandardRemoval.Lane = false
				return s.waitStandardRemoval(ctx, &p, "waiting_seat", err.Error(), 5*time.Second)
			}
			if err != nil {
				return p, err
			}
		}
		if m.SeatType == "prolite" {
			if p.StandardRemoval.SwitchSent {
				p.StandardRemoval.Checks++
				if p.StandardRemoval.Checks < 12 {
					return s.waitStandardRemoval(ctx, &p, "switching", "切换请求已发送，等待实际席位变为普通", 5*time.Second)
				}
				p.StandardRemoval.SwitchSent, p.StandardRemoval.Checks = false, 0
				return p, errors.New("多次核实仍是 5x 席位，切换未确认，未执行移出")
			}
			capacity, e := client.RecoverySeatCapacity(ctx, token, p.TeamAccountID)
			if e != nil {
				return p, e
			}
			if capacity.Standard.Remaining < 1 {
				p.StandardRemoval.Lane = false
				return s.waitStandardRemoval(ctx, &p, "waiting_seat", "普通席位暂无空位，等待释放后再切换", 30*time.Second)
			}
			p.StandardRemoval.SwitchSent = true
			if err := s.saveStandardRemoval(ctx, &p, "switching", "母号正在将 5x 席位切为普通席位", 0); err != nil {
				return p, err
			}
			if err := ctx.Err(); err != nil {
				return p, err
			}
			_, switchErr := client.SwitchRecoverySeat(ctx, token, p.TeamAccountID, p.UserID, p.StandardRemoval.FlowID, p.StandardRemoval.MutationID)
			p.StandardRemoval.MutationAfter = time.Now().Add(time.Duration(s.store.AutoRotationSettings().TeamOperationIntervalSeconds) * time.Second).UTC()
			if err := s.saveStandardRemoval(ctx, &p, "switching", "切换请求已返回，等待操作间隔后核实", 0); err != nil {
				return p, err
			}
			s.waitTeamOperationInterval(ctx)
			m, err = member()
			if err != nil {
				return p, err
			}
			if m != nil && m.SeatType != "default" {
				if switchErr != nil {
					return s.waitStandardRemoval(ctx, &p, "switching", "切换请求异常，先核实实际席位："+redactSensitiveText(switchErr.Error()), 5*time.Second)
				}
				return s.waitStandardRemoval(ctx, &p, "switching", "切换请求已发送，正在核实普通席位", 5*time.Second)
			}
		}
		if m != nil {
			p.StandardRemoval.Checks = 0
			message := "已确认普通席位，正在由母号踢出"
			if p.StandardRemoval.Method == "child_leave" {
				message = "已确认普通席位，正在由子号退出（全局代理）"
			}
			p.StandardRemoval.LeaveSent = true
			if err := s.saveStandardRemoval(ctx, &p, "removing", message, 0); err != nil {
				return p, err
			}
			if err := ctx.Err(); err != nil {
				return p, err
			}
			var removeErr error
			if leaveClient != nil {
				_, removeErr = leaveClient.Leave(ctx, childAT, p.TeamAccountID, p.UserID)
			} else {
				_, removeErr = client.Kick(ctx, token, p.TeamAccountID, p.UserID)
			}
			p.StandardRemoval.MutationAfter = time.Now().Add(time.Duration(s.store.AutoRotationSettings().TeamOperationIntervalSeconds) * time.Second).UTC()
			if err := s.saveStandardRemoval(ctx, &p, "verifying", "移出请求已返回，等待操作间隔后核实", 0); err != nil {
				return p, err
			}
			s.waitTeamOperationInterval(ctx)
			m, err = member()
			if err != nil {
				return p, err
			}
			if m != nil {
				if removeErr != nil {
					return p, removeErr
				}
				return p, errors.New("移出请求成功但成员仍在空间，保留普通席位位置等待重试")
			}
		}
	}
	if p.RemoteRemovedAt == nil {
		now := time.Now()
		p, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(v *model.FreeAccountProfile) {
			v.RemoteRemovedAt, v.RemovedAt, v.AutoRemove = &now, &now, false
			v.RemoveMethod = p.StandardRemoval.Method
		})
		if err != nil {
			return p, err
		}
		_ = s.store.ReleaseSeatReservationByAccount(p.ID)
	}
	if p.StandardRemoval.Lane {
		capacity, e := client.RecoverySeatCapacity(ctx, token, p.TeamAccountID)
		if e != nil {
			return p, e
		}
		if capacity.Standard.Remaining < 1 {
			return s.waitStandardRemoval(ctx, &p, "verifying", "已确认成员离开，等待普通席位释放", 5*time.Second)
		}
		// Keep the persisted lane until departure and capacity are both confirmed.
		p.StandardRemoval.Lane = false
	}
	if err := s.saveStandardRemoval(ctx, &p, "cleanup", "成员已离开且普通席位已释放，正在完成下游清理", 0); err != nil {
		return p, err
	}
	p, err = s.finishRemovedCycle(ctx, p)
	if err != nil {
		return p, err
	}
	if err == nil {
		s.auditAccountEvent(ctx, p.ID, "standard_removal", "remove", "standard_removal", "", "转普通后移出完成", nil)
	}
	return p, err
}

// Recovery of accepted removal work is independent of quota/401 switches and
// downstream availability. Bounded workers, per-child locks and per-Team locks
// are shared with manual and automatic operations.
func (s *Server) monitorStandardRemovals(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.resumeStandardRemovals(ctx)
		}
	}
}

func (s *Server) resumeStandardRemovals(ctx context.Context) {
	items, err := s.store.PendingStandardRemovals(time.Now())
	if err != nil || len(items) == 0 {
		return
	}
	queue := make(chan model.FreeAccountProfile, len(items))
	for _, p := range items {
		queue <- p
	}
	close(queue)
	var wg sync.WaitGroup
	for i := 0; i < min(s.store.AutoRotationSettings().Concurrency, len(items)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range queue {
				if ctx.Err() != nil {
					return
				}
				value, _ := s.freeLocks.LoadOrStore(p.ID, &sync.Mutex{})
				lock := value.(*sync.Mutex)
				if !lock.TryLock() {
					continue
				}
				attempt, cancel := context.WithTimeout(ctx, 3*time.Minute)
				_, _ = s.performFreeAccountRemovalGuarded(attempt, p.ID, false, func(latest model.FreeAccountProfile) error {
					if latest.CycleID != p.CycleID || latest.StandardRemoval == nil || latest.StandardRemoval.Stage == "failed" {
						return errors.New("本轮移出状态已改变")
					}
					return nil
				})
				cancel()
				lock.Unlock()
			}
		}()
	}
	wg.Wait()
}
