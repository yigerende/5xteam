package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

var errMergeV2Unknown = errors.New("导出停用结果待确认；自动恢复按配置移出原团队并核验个人套餐，不直接重复提交")

type mergeV2LoginKey struct{}
type mergeV2LoginOptions struct {
	TeamID                   string
	VerificationHTTPStatuses []int
	Call                     func(string, map[string]any) (any, error)
}

// Queue waits and child login can outlive the mother's AT. Read the current
// credential at each mother request, while keeping the original team pinned.
func (s *Server) mergeV2AdminAccessToken(ctx context.Context, adminID, teamID string) (string, error) {
	admin, creds, err := s.currentAdminCredential(ctx, adminID)
	if err != nil {
		return "", err
	}
	if admin.TeamAccountID != teamID {
		return "", errors.New("原流程团队与母号当前团队不一致")
	}
	return creds.AccessToken, nil
}

func (s *Server) mergeV2ProAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在执行操作")
		return
	}
	p, err := s.startProEndJob(email, lock.Unlock, nil, "manual_merge_v2")
	if err != nil {
		lock.Unlock()
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 202, p, "")
}

func (s *Server) mergeV2Update(email, stage, status, message string) (model.MailAccountProfile, error) {
	return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		m := p.ProMergeV2
		if m == nil {
			return
		}
		if m.Steps == nil {
			m.Steps = map[string]string{}
		}
		m.Stage, m.Status, m.Message, m.Error, m.UpdatedAt = stage, status, message, "", time.Now()
		m.Steps[stage] = status
		delete(m.Manual, stage)
	})
}

func (s *Server) mergeV2Checkpoint(ctx context.Context, email, team string, method string, args map[string]any) (any, error) {
	var checkpointErr error
	_, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		m := p.ProMergeV2
		if m == nil || m.TeamID != team {
			checkpointErr = errors.New("合并空间2.0目标空间已变化")
			return
		}
		// Validate under the same store lock as the durable checkpoint. A concurrent
		// stop or second submit must not slip between a read and this mutation.
		// Late responses still record accepted facts after a stop.
		if method == "merge_v2_ready" || method == "merge_v2_submitting" || method == "merge_v2_accepting" {
			if ctx.Err() != nil || p.ProAuto.StopRequested {
				checkpointErr = context.Canceled
				return
			}
			if m.SubmittedAt != nil || m.VerificationOnly || p.MergeV2Resolved() || p.MergeV2Completed() {
				checkpointErr = errMergeV2Unknown
				return
			}
			if args["team_id"] != team {
				checkpointErr = errors.New("登录会话不属于邀请目标空间")
				return
			}
		}
		if m.Steps == nil {
			m.Steps = map[string]string{}
		}
		switch method {
		case "merge_v2_accepting":
			if m.Steps["invite"] != "completed" {
				checkpointErr = errors.New("尚未成功发送目标团队邀请")
				return
			}
			if m.InviteAcceptAttemptedAt != nil {
				checkpointErr = errors.New("已尝试接受邀请，先核实团队成员关系，禁止重复提交")
				return
			}
			now := time.Now()
			m.InviteAcceptAttemptedAt, m.UpdatedAt = &now, now
		case "merge_v2_invite_accepted":
			if m.InviteAcceptAttemptedAt == nil || args["team_id"] != team {
				checkpointErr = errors.New("缺少目标团队邀请接受检查点")
				return
			}
			now := time.Now()
			m.InviteAcceptedAt, m.UpdatedAt = &now, now
		case "merge_v2_ready":
			userID, _ := args["user_id"].(string)
			if strings.TrimSpace(userID) == "" {
				checkpointErr = errors.New("团队会话缺少子号用户 ID")
				return
			}
			m.UserID = userID
			m.Steps["login"] = "completed"
			m.UpdatedAt = time.Now()
		case "merge_v2_submitting":
			if m.UserID == "" || m.Steps["login"] != "completed" {
				checkpointErr = errors.New("缺少已确认的团队登录检查点")
				return
			}
			now := time.Now()
			m.SubmittedAt = &now
			m.Stage, m.Status, m.Steps["submit"] = "submit", "running", "running"
			m.UpdatedAt = now
		case "merge_v2_accepted":
			if m.SubmittedAt == nil {
				checkpointErr = errors.New("缺少提交检查点")
				return
			}
			if m.AcceptedAt != nil {
				return
			}
			now := time.Now()
			m.AcceptedAt = &now
			m.Steps["submit"], m.Status, m.Error = "completed", "running", ""
			m.Message = "上游已受理导出并停用请求；不代表停用或退款已完成"
			m.UpdatedAt = now
		case "merge_v2_verify":
			if m.SubmittedAt == nil || p.MergeV2Resolved() {
				checkpointErr = errors.New("缺少待核验提交检查点")
				return
			}
			m.VerificationOnly = true
			if m.Recovery == nil {
				m.Recovery = &model.ProMergeV2Recovery{Cycles: 1}
			}
			m.Recovery.SetPhase("removing")
			m.Status, m.Steps["submit"], m.Message = "unknown", "unknown", "提交返回配置的核验状态码，仅核验原单，禁止重复提交"
			m.UpdatedAt = time.Now()
		case "merge_v2_rejected":
			if p.MergeV2Resolved() || m.VerificationOnly {
				checkpointErr = errors.New("已受理请求不可撤销检查点")
				return
			}
			m.SubmittedAt = nil
			m.Steps["submit"], m.Status = "failed", "failed"
			m.UpdatedAt = time.Now()
		default:
			checkpointErr = errors.New("未知合并空间2.0检查点")
		}
	})
	if err = errors.Join(err, checkpointErr); err != nil {
		return nil, err
	}
	return true, nil
}

func (s *Server) performProMergeV2Attempt(ctx context.Context, email string) (out model.MailAccountProfile, runErr error) {
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.IsProDead() {
		return p, errProAccountDead
	}
	if !p.ProFinalStepAllowed("merge_v2") {
		return p, errors.New("账号不在 Pro 管理中")
	}
	if p.MergeV2Completed() {
		return p, nil
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		latest, _, err := s.store.MailAccountCredential(email)
		if err == nil && latest.ProAuto.StopRequested {
			return context.Canceled
		}
		return err
	}
	if err = check(); err != nil {
		return p, err
	}
	if p.ProMergeV2 != nil && (p.ProMergeV2.SubmittedAt != nil || p.ProMergeV2.VerificationOnly) && !p.MergeV2Resolved() {
		return p, errMergeV2Unknown
	}
	settings, _, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return p, err
	}
	// Consume the one scheduled continuation before credential/network work.
	// A new ordinary failure must not turn into a retry on every monitor tick.
	if p.ProMergeV2 != nil && p.ProMergeV2.AutoResumeAt != nil {
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProMergeV2.AutoResumeAt = nil })
		if err != nil {
			return p, err
		}
	}
	defer func() {
		if !errors.Is(context.Cause(ctx), errProAutoFinalStepPaused) {
			return
		}
		var saveErr error
		out, saveErr = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			m := p.ProMergeV2
			if m == nil || m.CompletedAt != nil || (m.SubmittedAt != nil && !p.MergeV2Resolved()) {
				return
			}
			m.Status, m.Error, p.ProLastError = "interrupted", "", ""
			m.Message = errProAutoFinalStepPaused.Error()
			if m.Steps[m.Stage] != "completed" {
				if m.Steps == nil {
					m.Steps = map[string]string{}
				}
				m.Steps[m.Stage] = "interrupted"
			}
			interval := settings.QuotaCheckIntervalSeconds
			if p.ProAuto.QuotaIntervalSeconds > 0 {
				interval = p.ProAuto.QuotaIntervalSeconds
			}
			next := time.Now().Add(time.Duration(max(10, interval)) * time.Second)
			m.AutoResumeAt = &next
		})
		runErr = errors.Join(runErr, saveErr)
	}()
	if !p.MergeV2Started() {
		if err = s.checkProQualityMerge(ctx, p, true); err != nil {
			return p, err
		}
	} else {
		settings.TargetAdminID = p.ProMergeV2.AdminID
		settings.TargetSeatType = p.ProMergeV2.SeatType
	}
	admin, _, err := s.currentAdminCredential(ctx, settings.TargetAdminID)
	if err != nil {
		return p, err
	}
	if admin.TeamAccountID == "" {
		return p, errors.New("目标母号缺少团队 ID")
	}
	if p.MergeV2Started() && p.ProMergeV2.TeamID != admin.TeamAccountID {
		return p, errors.New("原流程团队与母号当前团队不一致")
	}
	value, _ := s.autoAdminLocks.LoadOrStore(admin.TeamAccountID, &sync.Mutex{})
	unlock, err := lockProMutex(ctx, value.(*sync.Mutex))
	if err != nil {
		return p, err
	}
	unlock = sync.OnceFunc(unlock)
	defer unlock()
	release, err := s.proMergeSlot(ctx)
	if err != nil {
		return p, err
	}
	defer release()
	if err = check(); err != nil {
		return p, err
	}
	adminSettings, err := s.settingsForAdmin(s.store.Settings(), admin)
	if err != nil {
		return p, err
	}
	client, err := workflow.NewClient(adminSettings)
	if err != nil {
		return p, err
	}
	if !p.MergeV2Started() {
		p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			now := time.Now()
			p.ProMergeV2 = &model.ProMergeV2{Status: "running", Stage: "invite", Steps: map[string]string{}, AdminID: admin.ID, TeamID: admin.TeamAccountID, SeatType: settings.TargetSeatType, StartedAt: now, UpdatedAt: now}
			p.ProPostMergeGroups = nil
		})
		if err != nil {
			return p, err
		}
	}
	p, err = s.prepareProPostMergeGroups(p, settings)
	if err != nil {
		return p, err
	}
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProWorkflowRunning = true
		p.ProLastError = ""
		p.ProMergeV2.AutoResumeAt = nil
		if p.ProPostMergeGroups != nil {
			p.ProPostMergeGroups.Action = "merge_v2"
		}
	})
	if err != nil {
		return p, err
	}
	defer func() {
		out, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProWorkflowRunning = false
			m := p.ProMergeV2
			m.UpdatedAt = time.Now()
			if runErr != nil {
				m.Error = redactSensitiveText(runErr.Error())
				p.ProLastError = m.Error
				m.Status = "failed"
				if m.SubmittedAt != nil && !p.MergeV2Resolved() {
					m.Status = "unknown"
					m.Error = errMergeV2Unknown.Error()
				}
				if m.Steps[m.Stage] != "completed" {
					m.Steps[m.Stage] = m.Status
				}
			} else {
				m.AutoResumeAt = nil
				if m.Recovery != nil {
					m.Recovery.NextAt = nil
					m.Recovery.Error = ""
				}
				m.Status = "completed"
				m.CompletedAt = &m.UpdatedAt
				m.Error = ""
				if p.DailyMergeMatches() {
					p.ProDailyMerge.Completed = true
				}
			}
		})
		runErr = errors.Join(runErr, err)
		if out.ProMergeV2 != nil {
			s.auditProEvent(out, "merge_v2", out.ProMergeV2.Status, settings.Provider, out.ProMergeV2.Message, map[string]any{"error": out.ProMergeV2.Error, "team_id": out.ProMergeV2.TeamID})
		}
	}()
	if err = s.endSIWCService(ctx, p); err != nil {
		return p, err
	}
	if p.ProMergeV2.Steps["invite"] != "completed" {
		if _, err = s.mergeV2Update(email, "invite", "running", "邀请子号进入目标团队"); err != nil {
			return p, err
		}
		p, err = s.claimMergeV2Stage(email, "invite")
		if err != nil {
			return p, err
		}
		token, e := s.mergeV2AdminAccessToken(ctx, admin.ID, admin.TeamAccountID)
		if e == nil {
			var present bool
			present, e = client.MergeV2TargetPresent(ctx, token, admin.TeamAccountID, email)
			if e == nil && !present {
				_, e = client.Invite(ctx, token, admin.TeamAccountID, email, settings.TargetSeatType)
			}
		}
		if e != nil {
			return p, &mergeV2RetryError{cause: e}
		}

		if err != nil {
			return p, err
		}
		p, err = s.mergeV2Update(email, "invite", "completed", "邀请成功")
		if err != nil {
			return p, err
		}
	}
	if !p.MergeV2Resolved() {
		if _, err = s.mergeV2Update(email, "login", "running", "重新登录并选择受邀团队"); err != nil {
			return p, err
		}
		p, err = s.claimMergeV2Stage(email, "login")
		if err != nil {
			return p, err
		}
		statuses, err := settings.MergeV2RecoveryConfig().VerificationStatuses()
		if err != nil {
			return p, err
		}
		opts := &mergeV2LoginOptions{TeamID: admin.TeamAccountID, VerificationHTTPStatuses: statuses, Call: func(method string, args map[string]any) (any, error) {
			return s.mergeV2Checkpoint(ctx, email, admin.TeamAccountID, method, args)
		}}
		loginCtx := context.WithValue(ctx, mergeV2LoginKey{}, opts)
		result, loginErr := s.loginProMergeV2(loginCtx, p, settings.Provider, waitProAuto)
		if (loginErr != nil || result["success"] != true) && isDeadOAuthResult(result, oauthErrorText(result, loginErr)) {
			dead, deadErr := s.markProDead(email, "merge_v2_login", oauthErrorText(result, loginErr))
			if deadErr != nil {
				return dead, deadErr
			}
			if dead.IsProDead() {
				return dead, errProAccountDead
			}
		}
		if loginErr != nil {
			return p, loginErr
		}
		if result["success"] != true {
			return p, fmt.Errorf("合并空间2.0：%s", oauthErrorText(result, nil))
		}
		p, _, err = s.store.MailAccountCredential(email)
		if err != nil {
			return p, err
		}
		if !p.MergeV2Resolved() {
			return p, errMergeV2Unknown
		}
	}
	if err = check(); err != nil {
		return p, err
	}
	if p.ProMergeV2.Steps["remove"] != "completed" {
		if p.ProMergeV2.UserID == "" {
			return p, errors.New("缺少已确认团队会话的用户 ID，禁止移出其他成员")
		}
		if _, err = s.mergeV2Update(email, "remove", "running", "请求已受理，移出该子号"); err != nil {
			return p, err
		}
		value, _ := s.teamRemoveLocks.LoadOrStore(admin.TeamAccountID, &sync.Mutex{})
		unlockRemove, e := lockProMutex(ctx, value.(*sync.Mutex))
		if e != nil {
			return p, e
		}
		if err = check(); err == nil {
			var token string
			token, err = s.mergeV2AdminAccessToken(ctx, admin.ID, admin.TeamAccountID)
			if err == nil {
				err = client.RemoveMergeV2Target(ctx, token, admin.TeamAccountID, email, p.ProMergeV2.UserID, false)
			}
		}
		unlockRemove()
		if err != nil {
			return p, err
		}
		p, err = s.mergeV2Update(email, "remove", "completed", "子号已移出团队")
		if err != nil {
			return p, err
		}
	}
	if err = check(); err != nil {
		return p, err
	}
	unlock() // Downstream grouping cannot hold the mother lock.
	if _, err = s.mergeV2Update(email, "groups", "running", "处理合并移出后分组"); err != nil {
		return p, err
	}
	p, err = s.finishProDownstream(ctx, p)
	if err != nil {
		return p, err
	}
	return s.mergeV2Update(email, "groups", "completed", "合并空间2.0流程完成；上游停用与退款结果另行核实")
}
