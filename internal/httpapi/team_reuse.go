package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

func (s *Server) listTeamVisits(w http.ResponseWriter, r *http.Request) {
	visits, err := s.store.TeamVisits(r.URL.Query().Get("email"), "")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, visits, "")
}

func (s *Server) reviewTeamHistory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirmed bool     `json:"confirmed"`
		TeamIDs   []string `json:"team_ids"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if !input.Confirmed {
		writeAPI(w, 400, nil, "请核对已进入的空间历史后确认")
		return
	}
	id := r.PathValue("id")
	unlock := s.lockFreeAccount(id)
	defer unlock()
	p, _, err := s.store.FreeAccountCredential(id)
	if err == nil {
		err = s.store.ReviewTeamHistory(p, input.TeamIDs)
	}
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	p, _, _ = s.store.FreeAccountCredential(id)
	s.auditAccountEvent(r.Context(), id, "history_review", "history", "manual_single", "", "已人工确认历史空间", map[string]any{"team_ids": input.TeamIDs})
	writeAPI(w, 200, p, "")
}

func reusableAccount(p model.FreeAccountProfile) bool {
	return !p.Dead && !p.HistoryUncertain && p.RemoveStatus == "completed" && p.VisitedTeamCount > 0
}

// Callers hold the account lock; the removal lock also covers emergency manual removal.
func (s *Server) prepareAccountReuse(ctx context.Context, p model.FreeAccountProfile) (model.FreeAccountProfile, error) {
	if p.RemoveStatus != "completed" {
		return p, nil
	}
	if !s.store.AutoRotationSettings().AllowMultiMotherReuse {
		return p, errors.New("尚未开启允许一子多母复用")
	}
	if !reusableAccount(p) {
		return p, errors.New("账号为死号或历史待确认，不能开始下一轮")
	}
	unlock := s.lockFreeAccountRemove(p.ID)
	defer unlock()
	current, credentials, err := s.store.FreeAccountCredential(p.ID)
	if err != nil {
		return p, err
	}
	if current.CycleID != p.CycleID {
		return p, store.ErrStaleCycle
	}
	s.oauthMu.RLock()
	busy := false
	for _, job := range s.oauthJobs {
		if strings.EqualFold(fmt.Sprint(job["email"]), p.Email) && (job["status"] == "queued" || job["status"] == "running") {
			busy = true
			break
		}
	}
	s.oauthMu.RUnlock()
	if busy {
		return p, errors.New("该账号仍有 OAuth 任务执行中，请等待结束")
	}
	if !p.DownstreamCleaned {
		if err = s.deleteLinkedDownstream(ctx, p); err != nil {
			return p, fmt.Errorf("旧轮次下游尚未清理: %w", err)
		}
		p, err = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.DownstreamCleaned = true })
		if err != nil {
			return p, err
		}
	}
	if mail, _, mailErr := s.store.MailAccountCredential(p.Email); mailErr == nil && !eligibleAutoRotationMail(mail) {
		return p, errors.New("邮件管理已将该账号标记为死号")
	}
	at := strings.TrimSpace(credentials.SourceAccessToken)
	needsLogin := at == ""
	if at != "" {
		check := workflow.TestAdminAccount(ctx, at, "", s.store.Settings())
		s.auditAccountEvent(ctx, p.ID, "reuse_at_check", "prepare", "reuse", "", "检查下一轮邀请使用的源 AT", map[string]any{"valid": check.Valid, "http_status": check.HTTPStatus, "message": check.Message})
		if !check.Valid {
			if check.HTTPStatus != http.StatusUnauthorized {
				return p, fmt.Errorf("源 AT 检测暂未通过，未发出邀请: %s", check.Message)
			}
			needsLogin = true
		}
	}
	if needsLogin {
		mail, creds, mailErr := s.store.MailAccountCredential(p.Email)
		if mailErr != nil {
			return p, errors.New("缺少邮件账号登录资料，请补充后手动重试")
		}
		if creds.GptPassword == "" && creds.PickupURL == "" && creds.MailPassword == "" && creds.MailRefreshToken == "" {
			return p, errors.New("缺少密码或邮箱收件凭据，请补充后手动重试")
		}
		s.auditAccountEvent(ctx, p.ID, "reuse_at_login", "prepare", "reuse", "", "源 AT 缺失或失效，执行现有临时 AT 登录", nil)
		result, loginErr := s.executeChatGPTAT(p.Email, func(msg string) { s.auditAccountEvent(ctx, p.ID, "reuse_at_login", "prepare", "reuse", "", msg, nil) }, nil)
		if loginErr != nil {
			return p, loginErr
		}
		if result == nil || result["success"] != true {
			return p, fmt.Errorf("临时 AT 登录失败: %v", result["error"])
		}
		at, _ = result["access_token"].(string)
		if strings.TrimSpace(at) == "" {
			return p, errors.New("临时 AT 登录没有返回 AT")
		}
		info, decodeErr := workflow.DecodeUserInfo(at)
		if decodeErr != nil || (!strings.EqualFold(info.Email, p.Email) && info.UserID != p.UserID) {
			return p, errors.New("临时 AT 账号身份不匹配")
		}
		creds.AccessToken = at
		if _, err = s.store.SaveMailAccount(mail, creds); err != nil {
			return p, err
		}
	}
	if err = ctx.Err(); err != nil {
		return p, err
	}
	next, err := s.store.BeginFreeAccountCycle(p.ID, p.CycleID, at)
	if err == nil {
		s.accountCycles.Store(p.ID, next.CycleID)
		s.auditAccountEvent(ctx, p.ID, "reuse_started", "prepare", "reuse", "", "旧轮次已归档，开始新的空间轮次", map[string]any{"previous_cycle_id": p.CycleID, "cycle_id": next.CycleID})
	}
	return next, err
}

func (s *Server) checkUnusedTeam(p model.FreeAccountProfile, teamID string) error {
	// A retry inside the current cycle is not a second workspace entry.
	if p.TeamAccountID == teamID && p.AcceptStatus == "completed" && p.RemoveStatus != "completed" {
		return nil
	}
	used, err := s.store.HasVisitedTeam(p, teamID)
	if err != nil {
		return err
	}
	if used {
		return errors.New("该子号曾成功进入此空间，不能再次使用同一 Team")
	}
	return nil
}
