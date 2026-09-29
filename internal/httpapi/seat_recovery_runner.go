package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

func (s *Server) runSeatRecoveryStep(parent context.Context, id string) {
	if parent.Err() != nil {
		return
	}
	p, err := s.store.SeatRecoveryTask(id)
	if err != nil || p.Finished || (p.Paused && !p.Lane) || (p.PendingAction == "" && p.Status == "failed") {
		return
	}
	interval := max(p.Settings.OperationIntervalSeconds, s.store.AutoRotationSettings().TeamOperationIntervalSeconds)
	ctx := parent
	// Login retains the shared OAuth per-attempt timeouts while now accepting
	// task cancellation. Other recovery steps keep their existing deadline.
	if p.Stage != "login" || p.PendingAction != "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, 3*time.Minute+time.Duration(interval)*time.Second)
		defer cancel()
	}
	// Existing account and mother locks serialize conflicting manual operations.
	// No Team claims, cycles, progress records, settings, or snapshots are written.
	if p.FreeID != "" {
		v, _ := s.freeLocks.LoadOrStore(p.FreeID, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if !lock.TryLock() {
			_ = s.recoveryWait(&p, "等待该子号当前操作完成", 5)
			return
		}
		defer lock.Unlock()
	}
	v, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		_ = s.recoveryWait(&p, "等待该子号当前登录操作完成", 5)
		return
	}
	defer lock.Unlock()
	if p.PendingAction != "" {
		p, err = s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) {
			v.ManualRemoveMethod = v.PendingAction
			v.PendingAction = ""
			v.ManualRemoveSent = false
			v.Stage, v.Status = "manual_remove", "queued"
			v.Paused = false
			v.Checks = 0
			v.RetryMutation = false
			v.Message = "正在核实成员，准备手动移出"
		})
		if err != nil {
			return
		}
	}
	err = s.executeSeatRecoveryStep(ctx, &p)
	if err != nil {
		if parent.Err() != nil {
			p.Status = "queued"
			p.Message = "服务停止，重启后先核实实际状态"
		} else {
			p.Status = "failed"
			p.Message = redactSensitiveText(err.Error())
		}
		s.recoveryLog(p, p.Message)
		_ = s.recoverySave(&p)
	}
}

func (s *Server) recoveryMember(ctx context.Context, p model.SeatRecoveryTask, c *workflow.Client, token string) (*workflow.TeamMember, error) {
	m, err := c.FindMemberByEmail(ctx, token, p.TeamID, p.Email)
	if err != nil {
		return nil, err
	}
	if m != nil && m.ID != p.UserID {
		return nil, errors.New("远端成员 ID 与扫描时不同，停止自动操作")
	}
	if m != nil && (m.Role == "account-owner" || m.Role == "owner") {
		return nil, errors.New("目标成员是空间所有者，禁止操作")
	}
	if m != nil && !m.Active {
		return nil, nil
	}
	return m, nil
}

func (s *Server) recoveryHeld(ctx context.Context, p model.SeatRecoveryTask, c *workflow.Client, token string) (bool, error) {
	items, err := c.HeldSeatMembers(ctx, token, p.TeamID, p.Email)
	if err != nil {
		return false, err
	}
	for _, m := range items {
		if strings.EqualFold(m.Email, p.Email) {
			if m.ID != p.UserID {
				return false, errors.New("临停成员 ID 已改变")
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) recoveryPending(p *model.SeatRecoveryTask, message string) error {
	p.Checks++
	if p.Checks >= 12 {
		return errors.New(message + "；多次核实仍未确认，请查看日志后重试")
	}
	return s.recoveryWait(p, message, 5)
}

// The normal Team lock is kept through the cooldown, so a Team invitation,
// approval or kick cannot bypass the spacing after a recovery mutation.
func (s *Server) recoveryMutation(ctx context.Context, p *model.SeatRecoveryTask, actor string, fn func(context.Context) (workflow.Response, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.store.SeatRecoveryTask(p.ID); err != nil {
		return err
	}
	s.recoveryLog(*p, "发送请求："+actor)
	ctx = workflow.WithRequestObserver(ctx, func(o workflow.RequestObservation) {
		if o.Phase == "start" {
			s.recoveryLog(*p, fmt.Sprintf("%s %s；传输 %s", o.Method, o.URL, o.Transport))
		} else {
			s.recoveryLog(*p, fmt.Sprintf("请求结束：HTTP %d，耗时 %s", o.StatusCode, o.Duration.Round(time.Millisecond)))
		}
	})
	response, err := fn(ctx)
	message := fmt.Sprintf("%s：HTTP %d", actor, response.StatusCode)
	if err != nil {
		message += "，" + err.Error()
	} else {
		message += "，等待实时成员核实"
	}
	s.recoveryLog(*p, message)
	seconds := max(p.Settings.OperationIntervalSeconds, s.store.AutoRotationSettings().TeamOperationIntervalSeconds)
	if seconds > 0 {
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return err
}

func (s *Server) executeSeatRecoveryStep(ctx context.Context, p *model.SeatRecoveryTask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.Stage == "manual_remove" || p.Stage == "manual_cooldown" {
		return s.executeSeatRecoveryManualRemoval(ctx, p)
	}
	if p.Stage == "cooldown" {
		if time.Now().Before(p.NextAt) {
			return nil
		}
		p.Lane = false
		p.Finished = true
		p.Stage, p.Status, p.Message = "completed", "completed", "5x 临停已解除，成员已离开，普通席位已释放"
		s.recoveryLog(*p, p.Message)
		return s.recoverySave(p)
	}
	if p.Stage == "dwell" && p.DueAt != nil && time.Now().Before(*p.DueAt) {
		p.Status = "waiting"
		p.NextAt = *p.DueAt
		return s.recoverySave(p)
	}
	if p.JoinedAt == nil {
		free, reason := s.recoveryCandidateReason(p.Email)
		if reason != "" {
			return errors.New(reason)
		}
		if free.ID != p.FreeID || free.CycleID != p.FreeCycle {
			return errors.New("子号 Team 流程已改变，恢复任务未继续进入空间")
		}
	}
	if p.Stage == "join" || p.Stage == "confirm" {
		claimed, err := s.store.ClaimSeatRecoveryEntry(*p)
		if err != nil {
			return err
		}
		if !claimed {
			return s.recoveryWait(p, "同母号串行等待：上一子号完成拉回（申请/邀请 → 同意 → 确认进入）", 2)
		}
	}
	admin, c, token, err := s.recoveryAdmin(ctx, p.AdminID, p.TeamID)
	if err != nil {
		return err
	}
	if p.UserID == admin.UserID || strings.EqualFold(p.Email, admin.Email) {
		return errors.New("禁止操作空间所有者")
	}
	if p.Stage == "login" {
		held, err := s.recoveryHeld(ctx, *p, c, token)
		if err != nil {
			return err
		}
		if !held {
			return errors.New("实际临停记录已消失，未重新登录或邀请")
		}
		p.Status, p.Message = "running", "正在检测已保存的 AT，有效则直接使用"
		if err = s.recoverySave(p); err != nil {
			return err
		}
		message, prepareErr := s.prepareSeatRecoveryAT(ctx, *p)
		if prepareErr != nil {
			return prepareErr
		}
		return s.recoveryAdvance(p, "join", message+"，等待串行拉回原空间")
	}
	// Never hold this lock for login or the dwell timer.
	v, _ := s.teamRemoveLocks.LoadOrStore(p.TeamID, &sync.Mutex{})
	teamLock := v.(*sync.Mutex)
	if !teamLock.TryLock() {
		return s.recoveryWait(p, "等待同母号操作串行队列", 2)
	}
	defer teamLock.Unlock()
	m, err := s.recoveryMember(ctx, *p, c, token)
	if err != nil {
		return err
	}
	if m != nil {
		p.SeatType = m.SeatType
	}
	if (p.Stage == "join" || p.Stage == "confirm") && m != nil {
		if m.SeatType != "prolite" {
			return errors.New("子号已入空间，但不是 5x 席位；未开始停留或移出")
		}
		held, err := s.recoveryHeld(ctx, *p, c, token)
		if err != nil {
			return err
		}
		if held {
			return s.recoveryPending(p, "成员已进入，等待临停记录解除")
		}
		now := time.Now()
		due := now.Add(time.Duration(p.Settings.DwellMinutes) * time.Minute)
		p.JoinedAt, p.DueAt = &now, &due
		return s.recoveryAdvance(p, "dwell", "已实时确认回到原母号 5x 席位，开始计算停留时间")
	}
	child := func() (*workflow.Client, string, error) {
		settings := s.store.Settings()
		if strings.TrimSpace(settings.ProxyURL) == "" {
			return nil, "", errors.New("子号全局代理未配置")
		}
		_, creds, err := s.store.MailAccountCredential(p.Email)
		if err != nil {
			return nil, "", err
		}
		if strings.TrimSpace(creds.AccessToken) == "" {
			return nil, "", errors.New("子号已保存的 AT 不存在")
		}
		cl, err := workflow.NewClient(settings)
		return cl, creds.AccessToken, err
	}
	switch p.Stage {
	case "join":
		held, err := s.recoveryHeld(ctx, *p, c, token)
		if err != nil {
			return err
		}
		if !held {
			return errors.New("发送进入请求前临停记录已消失，停止拉回")
		}
		if p.JoinSent && !p.RetryMutation {
			return s.recoveryAdvance(p, "confirm", "进入请求已发送或结果未确认，先核实并完成同意")
		}
		p.JoinSent = true
		p.ConfirmSent = false
		p.RetryMutation = false
		p.Status = "running"
		p.Message = "正在发送进入请求"
		if err = s.recoverySave(p); err != nil {
			return err
		}
		if p.Settings.JoinMethod == "child_request" {
			cl, at, e := child()
			if e != nil {
				return e
			}
			err = s.recoveryMutation(ctx, p, "子号申请（全局代理）", func(ctx context.Context) (workflow.Response, error) { return cl.RequestJoin(ctx, at, p.TeamID) })
		} else {
			err = s.recoveryMutation(ctx, p, "母号邀请 5x（母号专属代理）", func(ctx context.Context) (workflow.Response, error) {
				return c.Invite(ctx, token, p.TeamID, p.Email, "prolite")
			})
		}
		message := "进入请求已发送，等待同意"
		if err != nil {
			message = "进入请求返回错误，继续查询实际成员及申请结果"
		}
		return s.recoveryAdvance(p, "confirm", message)
	case "confirm":
		if p.RetryMutation && p.Settings.JoinMethod == "mother_invite" {
			return s.recoveryAdvance(p, "join", "手动重试：先核实成员，再重新发送邀请")
		}
		if p.ConfirmSent && !p.RetryMutation {
			return s.recoveryPending(p, "已发送同意请求，等待实际 5x 成员确认")
		}
		var fn func(context.Context) (workflow.Response, error)
		actor := "子号同意邀请（全局代理）"
		if p.Settings.JoinMethod == "child_request" {
			invite, e := c.FindInviteByEmail(ctx, token, p.TeamID, p.Email)
			if e != nil {
				s.recoveryLog(*p, e.Error())
				if p.RetryMutation && strings.Contains(e.Error(), "未找到子号") {
					return s.recoveryAdvance(p, "join", "手动重试：未找到申请，重新核实临停后申请")
				}
				return s.recoveryPending(p, "申请尚未确认，继续核实实际成员")
			}
			actor = "母号同意 5x（母号专属代理）"
			fn = func(ctx context.Context) (workflow.Response, error) {
				return c.ApproveInvite(ctx, token, p.TeamID, invite, "prolite")
			}
		} else {
			cl, at, e := child()
			if e != nil {
				return e
			}
			fn = func(ctx context.Context) (workflow.Response, error) { return cl.Accept(ctx, at, p.TeamID, p.UserID) }
		}
		p.ConfirmSent = true
		p.RetryMutation = false
		p.Status = "running"
		p.Message = "正在完成进入同意"
		if err = s.recoverySave(p); err != nil {
			return err
		}
		_ = s.recoveryMutation(ctx, p, actor, fn)
		return s.recoveryPending(p, "同意请求已执行，正在核实实际 5x 成员")
	case "dwell":
		if m == nil || m.SeatType != "prolite" {
			return errors.New("停留结束时成员已不在 5x 席位，未执行切换")
		}
		return s.recoveryAdvance(p, "switch", "停留已达标，等待串行切换到普通席位")
	case "switch":
		if m == nil {
			return errors.New("切换前子号已离开空间，无法确认席位恢复")
		}
		if m.SeatType != "prolite" && m.SeatType != "default" {
			return errors.New("未知实际席位类型，未执行切换")
		}
		if !p.Lane {
			_, err = s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) { v.Lane = true })
			if err != nil {
				if strings.Contains(err.Error(), "UNIQUE constraint") {
					return s.recoveryWait(p, "等待上一子号释放普通席位并完成间隔", 5)
				}
				return err
			}
			p.Lane = true
		}
		if m.SeatType == "default" {
			if !p.SwitchSent {
				return errors.New("发现非本任务切换的普通席位，请先核实，未自动移出")
			}
			return s.recoveryAdvance(p, "leave", "已实时确认普通席位，继续移出")
		}
		if p.SwitchSent && !p.RetryMutation {
			return s.recoveryPending(p, "切换请求已发送，等待实际席位变成普通席位")
		}
		capacity, err := c.RecoverySeatCapacity(ctx, token, p.TeamID)
		if err != nil {
			return err
		}
		p.Capacity = &capacity
		if capacity.Standard.Remaining < 1 {
			return s.recoveryWait(p, "普通席位暂无可用位置，等待空位（不会自动增加席位）", 30)
		}
		p.SwitchSent = true
		p.RetryMutation = false
		p.Status = "running"
		p.Message = "正在切换为普通席位"
		if err = s.recoverySave(p); err != nil {
			return err
		}
		_ = s.recoveryMutation(ctx, p, "切换普通席位（母号专属代理）", func(ctx context.Context) (workflow.Response, error) {
			return c.SwitchRecoverySeat(ctx, token, p.TeamID, p.UserID, p.FlowID, p.MutationID)
		})
		return s.recoveryPending(p, "切换请求已发送，正在核实普通席位")
	case "leave":
		if !p.Lane || !p.SwitchSent {
			return errors.New("缺少本任务普通席位切换记录，未移出")
		}
		if m == nil {
			return s.recoveryAdvance(p, "verify", "成员已离开，等待核实席位恢复与普通空位")
		}
		if m.SeatType != "default" {
			return errors.New("退出前不再是普通席位，未移出以免重新产生 5x 临停")
		}
		if p.LeaveSent && !p.RetryMutation {
			return s.recoveryPending(p, "移出请求已发送，等待实际离开空间")
		}
		var fn func(context.Context) (workflow.Response, error)
		actor := "母号踢出（母号专属代理）"
		if p.Settings.RemoveMethod == "child_leave" {
			cl, at, e := child()
			if e != nil {
				return e
			}
			actor = "子号退出（全局代理）"
			fn = func(ctx context.Context) (workflow.Response, error) { return cl.Leave(ctx, at, p.TeamID, p.UserID) }
		} else {
			fn = func(ctx context.Context) (workflow.Response, error) { return c.Kick(ctx, token, p.TeamID, p.UserID) }
		}
		p.LeaveSent = true
		p.RetryMutation = false
		p.Status = "running"
		p.Message = "正在移出普通席位成员"
		if err = s.recoverySave(p); err != nil {
			return err
		}
		_ = s.recoveryMutation(ctx, p, actor, fn)
		return s.recoveryPending(p, "移出请求已发送，正在核实离开结果")
	case "verify":
		if m != nil {
			return errors.New("成员仍在空间，普通席位串行位置继续保留")
		}
		held, err := s.recoveryHeld(ctx, *p, c, token)
		if err != nil {
			return err
		}
		if held {
			return s.recoveryPending(p, "成员已离开，但仍存在 5x 临停记录，尚未确认恢复")
		}
		capacity, err := c.RecoverySeatCapacity(ctx, token, p.TeamID)
		if err != nil {
			return err
		}
		p.Capacity = &capacity
		if capacity.Standard.Remaining < 1 {
			return s.recoveryWait(p, "成员已离开，等待普通席位实际释放；暂不处理下一子号", 30)
		}
		now := time.Now()
		p.LeftAt = &now
		p.SeatType = "outside"
		p.Stage, p.Status, p.Message = "cooldown", "waiting", fmt.Sprintf("已确认 5x 临停解除；实际 5x 总量 %d、剩余 %d、临停 %d；等待下一个间隔", capacity.Premium.Total, capacity.Premium.Remaining, capacity.Premium.Held)
		p.NextAt = now.Add(time.Duration(p.Settings.NextIntervalSeconds) * time.Second)
		s.recoveryLog(*p, p.Message)
		return s.recoverySave(p)
	default:
		return fmt.Errorf("未知恢复阶段 %q", p.Stage)
	}
}
