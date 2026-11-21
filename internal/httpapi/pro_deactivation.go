package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

type proDeactivationHooks struct {
	Protocol func(context.Context, map[string]any, func(string, map[string]any) (any, error)) (map[string]any, error)
	Login    func(context.Context, string) (map[string]any, error)
}

func (s *Server) performProDisposition(ctx context.Context, email string) (model.MailAccountProfile, error) {
	return s.performProAutomaticFinalStep(ctx, email, s.performProDispositionUnchecked)
}

func (s *Server) performProDispositionUnchecked(ctx context.Context, email string) (model.MailAccountProfile, error) {
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.MergeV2Started() {
		return s.performProMergeV2(ctx, email)
	}
	if p.MerchantStarted() {
		return s.performProAutomaticMerchant(ctx, email)
	}
	if p.DeletionConfirmed() {
		return p, nil
	}
	c, _, _, err := s.store.ProSettings()
	if err != nil {
		return p, err
	}
	// An accepted/uncertain request must be reconciled even after a mode change.
	if p.DeletionSubmitted() || c.AutoDeleteEnabled {
		return s.performProDeactivation(ctx, email)
	}
	if c.AutoMergeV2Enabled {
		return s.performProMergeV2(ctx, email)
	}
	if c.AutoMerchantEnabled {
		return s.performProAutomaticMerchant(ctx, email)
	}
	return s.performProMerge(ctx, email)
}
func (s *Server) startProDispositionJob(email string, unlock func(), quality *proQualityMergeRequest) (model.MailAccountProfile, error) {
	return s.startProEndJob(email, unlock, quality, "automatic")
}
func (s *Server) deactivateProAccount(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	value, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在执行操作，请等待后台流程完成")
		return
	}
	p, err := s.startProEndJob(email, lock.Unlock, nil, "manual_deactivate")
	if err != nil {
		lock.Unlock()
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusAccepted, p, "")
}

func (s *Server) deletionUpdate(email, stage, status, message string) (model.MailAccountProfile, error) {
	return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		now := time.Now()
		if p.ProDeactivation == nil {
			p.ProDeactivation = &model.ProDeactivation{StartedAt: now}
		}
		d := p.ProDeactivation
		if d.Steps == nil {
			d.Steps = map[string]string{}
		}
		d.Stage, d.Status, d.Message, d.UpdatedAt = stage, status, message, now
		d.Steps[stage] = status
		delete(d.Manual, stage)
		d.Error, p.ProLastError = "", ""
	})
}

func (s *Server) runDeletionProtocol(ctx context.Context, payload map[string]any, call func(string, map[string]any) (any, error)) (map[string]any, error) {
	if s.proDeleteHooks != nil && s.proDeleteHooks.Protocol != nil {
		return s.proDeleteHooks.Protocol(ctx, payload, call)
	}
	python := workflow.FindPython()
	if python == "" {
		return nil, errors.New("未找到 Python 运行环境")
	}
	cmd := exec.CommandContext(ctx, python, filepath.Join("internal", "protocol_pro_deactivate.py"))
	cmd.Dir, _ = os.Getwd()
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	return runOAuthChild(ctx, cmd, payload, call, nil)
}

func (s *Server) performProDeactivation(ctx context.Context, email string) (out model.MailAccountProfile, runErr error) {
	if err := ctx.Err(); err != nil {
		return out, err
	}
	p, _, err := s.store.MailAccountCredential(email)
	if err != nil {
		return p, err
	}
	if p.IsProDead() {
		return p, errProAccountDead
	}
	if !p.ProFinalStepAllowed("deactivate") {
		return p, errors.New("账号不在 Pro 管理中")
	}
	if err = s.endSIWCService(ctx, p); err != nil {
		return p, err
	}
	slot, err := s.proMergeSlot(ctx)
	if err != nil {
		return p, err
	}
	defer slot()
	if !p.DeletionSubmitted() {
		if err = s.checkProQualityMerge(ctx, p, true); err != nil {
			return p, err
		}
	}
	settings, _, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return p, err
	}
	if !p.DeletionSubmitted() {
		if p.ProPostMergeGroups != nil && p.ProPostMergeGroups.Action != "deactivate" {
			p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProPostMergeGroups = nil })
			if err != nil {
				return p, err
			}
		}
		p, err = s.prepareProPostMergeGroups(p, settings)
		if err != nil {
			return p, err
		}
		if p.ProPostMergeGroups != nil && p.ProPostMergeGroups.Action != "deactivate" {
			p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProPostMergeGroups.Action = "deactivate" })
			if err != nil {
				return p, err
			}
		}
	}
	defer func() {
		paused := errors.Is(context.Cause(ctx), errProAutoFinalStepPaused)
		if paused {
			runErr = errProAutoFinalStepPaused
		}
		latest, _, readErr := s.store.MailAccountCredential(email)
		if readErr != nil {
			runErr = errors.Join(runErr, readErr)
			return
		}
		// Accepted requests immediately run the configured downstream cleanup; a
		// later retry never repeats the deletion POST.
		if latest.DeletionAccepted() && ctx.Err() == nil && !latest.ProAuto.StopRequested {
			_, cleanupErr := s.finishProDownstream(ctx, latest)
			runErr = errors.Join(runErr, cleanupErr)
		}
		// A settings save may happen while the upstream request is in flight.
		// Use the latest interval when scheduling the next continuation.
		interval := settings.DeletionCheckInterval()
		if current, _, _, settingsErr := s.store.ProSettings(); settingsErr == nil {
			interval = current.DeletionCheckInterval()
		}
		var saveErr error
		out, saveErr = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if p.ProDeactivation == nil {
				return
			}
			d := p.ProDeactivation
			if d.Steps == nil {
				d.Steps = map[string]string{}
			}
			d.UpdatedAt = time.Now()
			if runErr == nil {
				d.Error = ""
				p.ProLastError = ""
			}
			if runErr != nil {
				d.Error = redactSensitiveText(runErr.Error())
				p.ProLastError = d.Error
				if d.SubmittedAt == nil {
					d.Status = "failed"
					d.Steps[d.Stage] = "failed"
				} else if d.AcceptedAt == nil {
					d.Status = "unknown"
					d.Steps["submit"] = "unknown"
				}
			}
			if paused && !p.DeletionSubmitted() {
				d.Status, d.Steps[d.Stage] = "interrupted", "interrupted"
				d.Error, p.ProLastError, d.Message = "", "", errProAutoFinalStepPaused.Error()
			}
			d.NextCheckAt = nil
			if p.DeletionNeedsContinuation() && !p.ProAuto.StopRequested {
				next := time.Now().Add(interval)
				d.NextCheckAt = &next
			}
		})
		runErr = errors.Join(runErr, saveErr)
		if out.ProDeactivation != nil {
			s.auditProEvent(out, "account_deactivation", out.ProDeactivation.Status, settings.Provider, out.ProDeactivation.Message, map[string]any{"error": out.ProDeactivation.Error})
		}
		if runErr != nil && !out.ProAuto.StopRequested && !paused {
			s.queueNotification("merge_result", "Pro 删除账户未完成", "账号："+email+"\n"+redactSensitiveText(runErr.Error()))
		}
	}()
	if p.DeletionConfirmed() {
		return p, nil
	}
	if !p.DeletionSubmitted() {
		p, err = s.deletionUpdate(email, "credentials", "running", "删除前重新登录获取凭据")
		if err != nil {
			return p, err
		}
		s.queueNotification("merge_start", "Pro 触发删除账户", fmt.Sprintf("账号：%s\n消耗额度（标准）：%s\n用户费用（已用）：%s", email, notificationUSD(p.CostDownstreamSnapshot), notificationUSD(p.UserCostDownstreamSnapshot)))
		if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
			return p, errors.New("删除账户需先配置全局代理")
		}
		if err = s.loginForProDeletion(ctx, p); err != nil {
			return p, err
		}
	}
	if err = ctx.Err(); err != nil {
		return p, err
	}
	p, c, readErr := s.store.MailAccountCredential(email)
	if readErr != nil {
		return p, readErr
	}
	session := map[string]any{}
	_ = json.Unmarshal([]byte(c.ChatGPTSession), &session)
	accountID, userID := "", p.OAuthUserID
	if d := p.ProDeactivation; d != nil {
		accountID = d.AccountID
		if d.UserID != "" {
			userID = d.UserID
		}
	}
	payload := map[string]any{"email": email, "access_token": c.AccessToken, "session": session, "proxy": s.store.Settings().ProxyURL, "check_only": p.DeletionSubmitted(), "account_id": accountID, "user_id": userID}
	if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
		return p, errors.New("删除账户需先配置全局代理")
	}
	result, protocolErr := s.runDeletionProtocol(ctx, payload, func(method string, args map[string]any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		str := func(k string) string { v, _ := args[k].(string); return v }
		switch method {
		case "identity":
			if str("account_id") == "" || str("user_id") == "" {
				return nil, errors.New("缺少已验证的个人账户身份")
			}
			_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				d := p.ProDeactivation
				d.AccountID, d.UserID = str("account_id"), str("user_id")
				d.Steps["credentials"] = "completed"
				delete(d.Manual, "credentials")
			})
		case "stage":
			_, err = s.deletionUpdate(email, "verify", "running", "执行删除专用验证")
		case "submitting":
			if err = s.checkProQualityMerge(ctx, p, true); err != nil {
				return nil, err
			}
			_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				d := p.ProDeactivation
				now := time.Now()
				d.SubmittedAt = &now
				d.Steps["verify"] = "completed"
				delete(d.Manual, "verify")
				delete(d.Manual, "submit")
				d.Stage, d.Status, d.Steps["submit"] = "submit", "running", "running"
			})
		case "accepted":
			_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				d := p.ProDeactivation
				now := time.Now()
				d.AcceptedAt = &now
				d.Status, d.Stage, d.Message = "queued", "confirm", "删除请求已受理，等待上游确认"
				d.Steps["submit"], d.Steps["confirm"] = "completed", "waiting"
				delete(d.Manual, "submit")
				delete(d.Manual, "confirm")
				if p.DailyMergeMatches() {
					p.ProDailyMerge.Completed = true
				}
			})
			if err == nil {
				s.queueNotification("merge_result", "Pro 删除请求已受理", "账号："+email+"\n已提交删除，立即执行所选分组调整；退款仍以卡台流水为准")
			}
		case "rejected":
			_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProDeactivation.SubmittedAt = nil
				p.ProDeactivation.Status = "failed"
				p.ProDeactivation.Steps["submit"] = "failed"
			})
		default:
			return nil, errors.New("未知删除流程事件")
		}
		return true, err
	})
	if protocolErr != nil {
		return p, protocolErr
	}
	state, _ := result["status"].(string)
	message, _ := result["message"].(string)
	if state == "need_login" {
		return p, errors.New("重新登录后凭据仍无效，请重试删除流程")
	}
	if state == "confirmed" {
		// The web-session check precedes a final test of the actual pushed OAuth
		// account. Do not let a failed admin login masquerade as upstream 401.
		p, _, err = s.store.MailAccountCredential(email)
		if err != nil {
			return p, err
		}
		if p.DeletionConfirmed() || !p.DeletionSubmitted() {
			return p, nil
		}
		if p.PushProvider == "sub2" && p.Sub2AccountID > 0 {
			if err = s.confirmProDeletionSub2(ctx, p); err != nil {
				return p, err
			}
			result["confirmation_source"] = "sub2_http_401"
			message = "Sub2 gpt-6.1-sol 测试返回上游 HTTP 401，删除已确认"
		}
	}
	if state == "queued" || state == "confirmed" || state == "pending" {
		if err = ctx.Err(); err != nil {
			return p, err
		}
		return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			if p.DeletionConfirmed() {
				return // A manual confirmation made during the test remains terminal.
			}
			d := p.ProDeactivation
			now := time.Now()
			d.CheckedAt = &now
			d.Message, d.Error = message, ""
			if state == "confirmed" && d.SubmittedAt != nil {
				d.ConfirmedAt = &now
				d.ConfirmationSource = "upstream"
				if source, _ := result["confirmation_source"].(string); source == "http_401" || source == "sub2_http_401" {
					d.ConfirmationSource = source
				}
				delete(d.Manual, "confirm")
				d.Status, d.Stage, d.Steps["confirm"] = "completed", "confirm", "completed"
				// Explicit state for the original account recovers a lost POST response.
				if d.AcceptedAt == nil {
					d.AcceptedAt = &now
					d.Steps["submit"] = "completed"
				}
			}
		})
	}
	return p, errors.New(firstNonEmpty(message, "删除流程未完成"))
}

func (s *Server) confirmProDeletionSub2(ctx context.Context, p model.MailAccountProfile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.ProAuto.StopRequested {
		return context.Canceled
	}
	cfg, password, _, err := s.proAccountPushSettings(p)
	if err != nil {
		return err
	}
	if cfg.Provider != "sub2" || p.ProPushDownstream != nil && !p.ProPushDownstream.Matches(model.ProDownstream(cfg)) {
		return errors.New("删除待确认：Sub2 连接与该账号原推送位置不一致")
	}
	if err = proImportedDownstreamOK(p, cfg); err != nil {
		return err
	}
	if _, err = s.deletionUpdate(p.Email, "confirm", "waiting", "正在用 Sub2 gpt-6.1-sol 测试核验删除结果"); err != nil {
		return err
	}
	probe, err := s.sub2.ProbeDeletion(ctx, cfg.Sub2, password, p.Sub2AccountID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, saveErr := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		if !p.DeletionConfirmed() && p.ProDeactivation != nil {
			now := time.Now()
			p.ProDeactivation.CheckedAt = &now
		}
	}); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return fmt.Errorf("删除待确认：Sub2 测试请求失败：%w", err)
	}
	if probe.HTTPStatus == http.StatusUnauthorized {
		return nil
	}
	if probe.HTTPStatus == http.StatusOK {
		return errors.New("删除待确认：Sub2 gpt-6.1-sol 仍可正常使用")
	}
	if probe.HTTPStatus > 0 {
		return fmt.Errorf("删除待确认：Sub2 测试返回 HTTP %d，需等待上游 HTTP 401", probe.HTTPStatus)
	}
	return errors.New("删除待确认：Sub2 测试未返回明确的上游 HTTP 401")
}

// Every new deletion attempt obtains a fresh web session. Confirmation and
// downstream-only retries keep the submitted request and never log in again.
func (s *Server) loginForProDeletion(ctx context.Context, p model.MailAccountProfile) error {
	var login map[string]any
	var err error
	if s.proDeleteHooks != nil && s.proDeleteHooks.Login != nil {
		login, err = s.proDeleteHooks.Login(ctx, p.Email)
	} else {
		ctx, err = s.proOAIDeletionContext(ctx, p.Email)
		if err != nil {
			return err
		}
		login, err = s.executeOpenAILoginContext(ctx, p.Email, "chatgpt_at", func(msg string) {
			s.auditProEvent(p, "account_deactivation_login", "running", "", redactSensitiveText(msg), nil)
		}, nil)
	}
	if err != nil {
		return err
	}
	if login["success"] != true {
		return errors.New(firstNonEmpty(oauthErrorText(login, nil), "删除账户重新登录失败"))
	}
	token, _ := login["accessToken"].(string)
	if strings.TrimSpace(token) == "" {
		token, _ = login["access_token"].(string)
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("重新登录未返回有效 AT，未提交删除")
	}
	raw, err := json.Marshal(login)
	if err != nil {
		return err
	}
	return s.store.SaveProDeletionSession(p.Email, string(raw))
}

func (s *Server) tickProDeactivations(ctx context.Context) {
	if s.checkProAutoFinalStepAllowed() != nil {
		return
	}
	items, err := s.store.DueProDeactivations(time.Now())
	if err != nil {
		return
	}
	for _, p := range items {
		if ctx.Err() != nil {
			return
		}
		v, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if !lock.TryLock() {
			continue
		}
		if _, err = s.startProEndJob(p.Email, lock.Unlock, nil, "deactivate"); err != nil {
			lock.Unlock()
		}
	}
}
