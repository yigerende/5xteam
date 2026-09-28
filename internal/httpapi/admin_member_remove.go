package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

type adminMemberRemoveInput struct {
	Email  string `json:"email"`
	UserID string `json:"user_id"`
	TeamID string `json:"team_account_id"`
	Method string `json:"method"`
}

func (s *Server) removeAdminMember(w http.ResponseWriter, r *http.Request) {
	var input adminMemberRemoveInput
	if decodeJSON(w, r, &input, 4096) != nil {
		return
	}
	input.Email, input.UserID, input.TeamID = strings.TrimSpace(input.Email), strings.TrimSpace(input.UserID), strings.TrimSpace(input.TeamID)
	if input.Email == "" || input.UserID == "" || input.TeamID == "" || (input.Method != "mother_kick" && input.Method != "child_leave") {
		writeAPI(w, 400, nil, "成员、空间或移出方式无效")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	var streamMu sync.Mutex
	send := func(event any) {
		streamMu.Lock()
		defer streamMu.Unlock()
		_ = json.NewEncoder(w).Encode(event)
		_ = http.NewResponseController(w).Flush()
	}
	stopHeartbeat, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-r.Context().Done():
				return
			case <-ticker.C:
				send(map[string]any{"type": "heartbeat"})
			}
		}
	}()
	defer func() { close(stopHeartbeat); <-heartbeatDone }()
	progress := func(stage, message string) {
		send(map[string]any{"type": "progress", "stage": stage, "message": message})
	}
	err := s.performAdminMemberRemoval(r.Context(), r.PathValue("id"), input, progress)
	if err != nil {
		send(map[string]any{"type": "error", "error": err.Error()})
		return
	}
	send(map[string]any{"type": "done", "message": "操作完成，成员已不在该空间"})
}

func protectedAdminMember(admin model.AdminAccountProfile, member workflow.TeamMember) bool {
	return strings.EqualFold(strings.TrimSpace(member.Email), strings.TrimSpace(admin.Email)) ||
		member.Role == "account-owner" || member.Role == "owner"
}

// Explicit member actions use the same Team lock, cooldown, Kick/Leave protocol,
// final cost query and cycle cleanup as step 6. The selected method is local to
// this operation; it does not alter automatic rotation settings.
func (s *Server) performAdminMemberRemoval(requestCtx context.Context, adminID string, input adminMemberRemoveInput, progress func(string, string)) (resultErr error) {
	ctx, cancel := context.WithTimeout(requestCtx, 8*time.Minute)
	defer cancel()
	progress("waiting", "等待该成员及母号前序操作完成")
	p, err := s.store.FreeAccountForEmail(input.Email)
	if err != nil {
		return err
	}
	lockedAccountID := p.ID
	if p.ID != "" {
		unlock := s.lockFreeAccountRemove(p.ID)
		defer unlock()
	}
	unlockTeam := s.lockTeamAccountRemove(input.TeamID)
	defer unlockTeam()
	if err := ctx.Err(); err != nil {
		return err
	}
	progress("verifying", "正在通过母号专属代理核实实际成员")
	admin, credentials, err := s.currentAdminCredential(ctx, adminID)
	if err != nil {
		return err
	}
	if admin.TeamAccountID != input.TeamID {
		return errors.New("母号空间配置已改变，请刷新成员列表后重试")
	}
	settings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return err
	}
	client, err := workflow.NewClient(settings)
	if err != nil {
		return err
	}
	member, err := client.FindMemberByEmail(ctx, credentials.AccessToken, input.TeamID, input.Email)
	if err != nil {
		return fmt.Errorf("实际成员核实失败: %w", err)
	}
	if member != nil && (member.ID != input.UserID || protectedAdminMember(admin, *member)) {
		return errors.New("成员身份已改变或目标是空间所有者，请刷新成员列表")
	}
	// Re-read after queueing. Never close a different Team's cycle, a new waiting
	// cycle, or a historical removed cycle just because its email matches.
	p, err = s.store.FreeAccountForEmail(input.Email)
	if err != nil {
		return err
	}
	if p.ID != lockedAccountID {
		return errors.New("本地账号记录已改变，请刷新成员列表后重试")
	}
	linked := p.ID != "" && p.TeamAccountID == input.TeamID && p.RemoveStatus != "completed" && !p.ReusePending && (p.UserID == "" || p.UserID == input.UserID)
	accountID := ""
	if linked {
		accountID = p.ID
	}
	details := map[string]any{"team_account_id": input.TeamID, "user_id": input.UserID, "remove_method": input.Method, "source": "admin_members"}
	log := func(stage, message string, logErr error) {
		d := make(map[string]any, len(details)+1)
		for k, v := range details {
			d[k] = v
		}
		level := "info"
		if logErr != nil {
			d["error"] = logErr.Error()
			level = "error"
		}
		s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: accountID, AdminAccountID: admin.ID, Email: input.Email, Type: "remove_trace", Operation: "remove", Source: "admin_members", Stage: stage, Message: message, Level: level, Details: d})
	}
	defer func() {
		if resultErr != nil {
			log("member_remove_error", "成员弹窗移出处理失败", resultErr)
		}
	}()
	if member == nil {
		if linked && p.RemoteRemovedAt != nil {
			progress("syncing", "成员已移出，继续完成轮转记录清理")
			_, err = s.finishRemovedCycle(ctx, p)
			return err
		}
		if linked {
			return errors.New("成员已不在空间，但本地尚无移出成功记录，请核对 Team 流程状态")
		}
		log("member_remove_absent", "实时核实成员已不在空间，未重复发送移出请求", nil)
		return nil
	}
	removeClient, token := client, credentials.AccessToken
	proxy := s.adminProxyLogDetails(settings, admin)
	if input.Method == "child_leave" {
		_, mailCredentials, mailErr := s.store.MailAccountCredential(input.Email)
		if mailErr != nil || strings.TrimSpace(mailCredentials.AccessToken) == "" {
			return errors.New("邮件管理中没有该子号的可用 AT，请先获取 AT，或使用母号踢出")
		}
		if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
			return errors.New("未配置全局代理，无法执行子号自行退出")
		}
		removeClient, err = workflow.NewClient(s.store.Settings())
		if err != nil {
			return err
		}
		token = strings.TrimSpace(mailCredentials.AccessToken)
		proxy = s.proxyLogDetails(s.store.Settings(), "global")
	}
	details["proxy"] = proxy
	if linked {
		progress("cost", "正在获取移出前最后一次消耗记录")
		p = s.refreshSub2CostBeforeRemoval(ctx, p)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Once mutation starts, browser disconnect must not abandon persistence or
	// release the Team lock before its cooldown. Bound the remaining work.
	mutationCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer finish()
	ctx = mutationCtx
	label := "母号踢出（母号专属代理）"
	if input.Method == "child_leave" {
		label = "子号退出（全局代理）"
	}
	progress("removing", "正在执行"+label)
	if linked {
		p, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(v *model.FreeAccountProfile) { v.Status, v.RemoveStatus, v.LastError = "removing", "running", "" })
		if err != nil {
			return err
		}
	}
	log("member_remove_start", "成员弹窗开始"+label, nil)
	var response workflow.Response
	if input.Method == "child_leave" {
		response, err = removeClient.Leave(ctx, token, input.TeamID, member.ID)
	} else {
		response, err = retryTeamRequest(ctx, s.store.Settings(), func() (workflow.Response, error) { return removeClient.Kick(ctx, token, input.TeamID, member.ID) })
	}
	details["http_status"] = response.StatusCode
	if err != nil {
		if linked {
			_, _ = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(v *model.FreeAccountProfile) {
				v.Status, v.RemoveStatus, v.LastError = "remove_failed", "failed", err.Error()
			})
		}
		return err
	}
	log("member_remove_success", label+"成功", nil)
	// Always apply the shared cooldown, even if saving the result fails.
	defer func() {
		progress("cooldown", "OpenAI 已移出成员，等待母号操作间隔结束")
		s.waitTeamOperationInterval(ctx)
	}()
	if linked {
		progress("syncing", "OpenAI 已移出成员，正在同步 Team 最后一步和清理下游")
		now := time.Now()
		p, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(v *model.FreeAccountProfile) {
			v.Status, v.RemoveStatus, v.LastError = "cleanup_pending", "cleanup_pending", ""
			v.RemoveMethod = input.Method
			v.AutoRemove, v.RemoteRemovedAt, v.RemovedAt = false, &now, &now
		})
		if err != nil {
			return fmt.Errorf("OpenAI 已移出成员，但本地保存失败: %w", err)
		}
		_ = s.store.ReleaseSeatReservationByAccount(p.ID)
		if _, err = s.finishRemovedCycle(ctx, p); err != nil {
			return fmt.Errorf("OpenAI 已移出成员，但下游清理未完成: %w", err)
		}
	}
	return nil
}
