package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

// Manual removal deliberately skips dwell and seat switching. It only closes
// this recovery task; it never invokes Team cleanup or deletes downstream data.
func (s *Server) executeSeatRecoveryManualRemoval(ctx context.Context, p *model.SeatRecoveryTask) error {
	if p.Stage == "manual_cooldown" {
		if time.Now().Before(p.NextAt) {
			return nil
		}
		p.Lane, p.Finished = false, true
		p.Stage, p.Status = "manual_done", "completed"
		p.Message += "；手动移出任务已结束"
		s.recoveryLog(*p, p.Message)
		return s.recoverySave(p)
	}
	if p.ManualRemoveMethod != "mother_kick" && p.ManualRemoveMethod != "child_leave" {
		return errors.New("手动移出方式无效")
	}
	admin, c, token, err := s.recoveryAdmin(ctx, p.AdminID, p.TeamID)
	if err != nil {
		return err
	}
	if p.UserID == admin.UserID || strings.EqualFold(p.Email, admin.Email) {
		return errors.New("禁止操作空间所有者")
	}
	v, _ := s.teamRemoveLocks.LoadOrStore(p.TeamID, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return s.recoveryWait(p, "等待同母号串行队列，准备手动移出", 2)
	}
	defer lock.Unlock()
	m, err := s.recoveryMember(ctx, *p, c, token)
	if err != nil {
		return err
	}
	if m == nil {
		held, err := s.recoveryHeld(ctx, *p, c, token)
		if err != nil {
			return err
		}
		if p.Lane {
			capacity, err := c.RecoverySeatCapacity(ctx, token, p.TeamID)
			if err != nil {
				return err
			}
			p.Capacity = &capacity
			if capacity.Standard.Remaining < 1 {
				return s.recoveryWait(p, "成员已离开，等待普通席位释放后结束手动移出", 30)
			}
		}
		now := time.Now()
		p.LeftAt, p.SeatType = &now, "outside"
		p.Stage, p.Status = "manual_cooldown", "waiting"
		p.Message = "已实时确认成员不在空间；已停止本次自动恢复"
		if held {
			p.Message += "；仍有 5x 临停记录，未标记席位恢复成功"
		}
		p.NextAt = now.Add(time.Duration(p.Settings.NextIntervalSeconds) * time.Second)
		s.recoveryLog(*p, p.Message)
		return s.recoverySave(p)
	}
	p.SeatType = m.SeatType
	if p.ManualRemoveSent && !p.RetryMutation {
		return s.recoveryPending(p, "手动移出请求已发送，等待实际成员离开")
	}
	removeClient, at := c, token
	actor := "手动母号踢出（母号专属代理）"
	if p.ManualRemoveMethod == "child_leave" {
		settings := s.store.Settings()
		if strings.TrimSpace(settings.ProxyURL) == "" {
			return errors.New("未配置子号全局代理")
		}
		_, creds, err := s.store.MailAccountCredential(p.Email)
		if err != nil || strings.TrimSpace(creds.AccessToken) == "" {
			return errors.New("子号没有已保存的 AT，请先获取 AT 或选择母号踢出")
		}
		removeClient, err = workflow.NewClient(settings)
		if err != nil {
			return err
		}
		at, actor = creds.AccessToken, "手动子号退出（全局代理）"
	}
	p.ManualRemoveSent, p.RetryMutation = true, false
	p.Status, p.Message = "running", actor+"执行中"
	if err = s.recoverySave(p); err != nil {
		return err
	}
	_ = s.recoveryMutation(ctx, p, actor, func(ctx context.Context) (workflow.Response, error) {
		if p.ManualRemoveMethod == "child_leave" {
			return removeClient.Leave(ctx, at, p.TeamID, p.UserID)
		}
		return removeClient.Kick(ctx, at, p.TeamID, p.UserID)
	})
	return s.recoveryPending(p, "手动移出请求已执行，正在核实实际成员；不会继续自动换席")
}
