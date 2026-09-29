package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

type seatRecoveryRuntime struct {
	mu        sync.Mutex
	active    map[string]bool
	preparing map[string]bool
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
	login     func(string, func(string), func(protocolOAuthDiagnostic)) (map[string]any, error)
}

type recoveryCandidate struct {
	workflow.HeldSeatMember
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

func recoveryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func validateRecoverySettings(v model.SeatRecoverySettings) error {
	if v.JoinMethod != "mother_invite" && v.JoinMethod != "child_request" {
		return errors.New("进入方式无效")
	}
	if v.RemoveMethod != "mother_kick" && v.RemoveMethod != "child_leave" {
		return errors.New("移出方式无效")
	}
	if v.DwellMinutes < 0 || v.DwellMinutes > 10080 || v.OperationIntervalSeconds < 0 || v.OperationIntervalSeconds > 3600 || v.NextIntervalSeconds < 0 || v.NextIntervalSeconds > 3600 || v.Concurrency < 1 {
		return errors.New("停留分钟须为 0–10080；间隔须为 0–3600 秒；AT 并发数须为正整数")
	}
	return nil
}

func (s *Server) getSeatRecoverySettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.SeatRecoverySettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) saveSeatRecoverySettings(w http.ResponseWriter, r *http.Request) {
	var v model.SeatRecoverySettings
	if err := decodeJSON(w, r, &v, 1<<16); err != nil {
		return
	}
	if err := validateRecoverySettings(v); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if err := s.store.SaveSeatRecoverySettings(v); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}

func (s *Server) recoveryAdmin(ctx context.Context, id, teamID string) (model.AdminAccountProfile, *workflow.Client, string, error) {
	p, c, err := s.currentAdminCredential(ctx, id)
	if err != nil {
		return p, nil, "", err
	}
	if p.TeamAccountID == "" || (teamID != "" && teamID != p.TeamAccountID) {
		return p, nil, "", errors.New("母号空间已更换，恢复任务不会自动转移到新空间")
	}
	settings, err := s.settingsForAdmin(s.store.Settings(), p)
	if err != nil {
		return p, nil, "", err
	}
	settings.Role = "standard-user"
	client, err := workflow.NewClient(settings)
	return p, client, c.AccessToken, err
}

func (s *Server) recoveryCandidateReason(email string) (model.FreeAccountProfile, string) {
	free, err := s.store.FreeAccountForEmail(email)
	if err != nil {
		return free, err.Error()
	}
	ok, err := s.store.SeatRecoveryMailEligible(email)
	if err != nil {
		return free, err.Error()
	}
	if !ok {
		return free, "未匹配到邮件管理的未进入空间或已使用过账号（死号、在空间内及 Pro 账号不参与）"
	}
	if store.SeatRecoveryTeamInFlight(free) {
		return free, "子号已有 Team 申请或邀请在途，恢复任务不会抢占"
	}
	if free.ID != "" && s.store.FreeAccountClaim(free.ID) != "" {
		return free, "子号已被 Team 轮转预定，恢复任务不会抢占"
	}
	return free, ""
}

func (s *Server) scanSeatRecovery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	admin, c, token, err := s.recoveryAdmin(ctx, r.PathValue("id"), "")
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	members, err := c.HeldSeatMembers(ctx, token, admin.TeamAccountID, "")
	if err != nil {
		writeAPI(w, 502, nil, redactSensitiveText(err.Error()))
		return
	}
	active, _, err := s.store.SeatRecoveryTasks(true, 1000, 0)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	claimed := map[string]bool{}
	for _, p := range active {
		claimed[p.Email] = true
	}
	out := []recoveryCandidate{}
	for _, member := range members {
		_, reason := s.recoveryCandidateReason(member.Email)
		if claimed[strings.ToLower(member.Email)] {
			reason = "已有未结束的恢复任务"
		}
		if member.ID == admin.UserID || strings.EqualFold(member.Email, admin.Email) {
			reason = "不能操作母号本人"
		}
		out = append(out, recoveryCandidate{HeldSeatMember: member, Eligible: reason == "", Reason: reason})
	}
	writeAPI(w, 200, map[string]any{"items": out, "team_id": admin.TeamAccountID, "server_now": time.Now()}, "")
}

func (s *Server) startSeatRecovery(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emails []string `json:"emails"`
		TeamID string   `json:"team_id"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if len(input.Emails) == 0 || len(input.Emails) > 1000 || input.TeamID == "" {
		writeAPI(w, 400, nil, "请先扫描并选择 1–1000 个子号")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	admin, c, token, err := s.recoveryAdmin(ctx, r.PathValue("id"), input.TeamID)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	settings, err := s.store.SeatRecoverySettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if err = validateRecoverySettings(settings); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	if strings.TrimSpace(s.store.Settings().ProxyURL) == "" {
		writeAPI(w, 400, nil, "请先设置子号使用的全局代理")
		return
	}
	_, total, err := s.store.SeatRecoveryTasks(true, 1, 0)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	if total+len(input.Emails) > 1000 {
		writeAPI(w, 400, nil, "同时未结束的恢复任务最多 1000 个")
		return
	}
	members, err := c.HeldSeatMembers(ctx, token, input.TeamID, "")
	if err != nil {
		writeAPI(w, 502, nil, redactSensitiveText(err.Error()))
		return
	}
	byEmail := map[string]workflow.HeldSeatMember{}
	for _, m := range members {
		byEmail[strings.ToLower(m.Email)] = m
	}
	created := []model.SeatRecoveryTask{}
	failures := map[string]string{}
	seen := map[string]bool{}
	for _, email := range input.Emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if seen[email] {
			continue
		}
		seen[email] = true
		member, ok := byEmail[email]
		if !ok {
			failures[email] = "实时临停列表已无此账号"
			continue
		}
		free, reason := s.recoveryCandidateReason(email)
		if reason != "" {
			failures[email] = reason
			continue
		}
		if member.ID == admin.UserID || strings.EqualFold(email, admin.Email) {
			failures[email] = "不能操作母号本人"
			continue
		}
		now := time.Now()
		p := model.SeatRecoveryTask{ID: recoveryID(), AdminID: admin.ID, AdminLabel: admin.Label, TeamID: input.TeamID, Email: email, UserID: member.ID, Settings: settings, Stage: "login", Status: "queued", Message: "等待检测 AT，失效时再登录", SeatType: "held_prolite", FreeID: free.ID, FreeCycle: free.CycleID, CreatedAt: now, UpdatedAt: now, NextAt: now, FlowID: recoveryID(), MutationID: recoveryID()}
		if err = s.createSeatRecoveryTask(p); err != nil {
			failures[email] = err.Error()
			continue
		}
		s.recoveryLog(p, "创建独立恢复任务；设置已固定；不修改 Team 六步状态或下游账号")
		created = append(created, p)
	}
	writeAPI(w, 202, map[string]any{"items": created, "failures": failures}, "")
}

func (s *Server) createSeatRecoveryTask(p model.SeatRecoveryTask) error {
	// Manual Team joins hold this account lock before checking reservations.
	// Use the same lock during admission, then retain the durable reservation.
	if p.FreeID != "" {
		v, _ := s.freeLocks.LoadOrStore(p.FreeID, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if !lock.TryLock() {
			return errors.New("子号正在执行其他操作，请完成后再恢复")
		}
		defer lock.Unlock()
	}
	return s.store.CreateSeatRecoveryTask(p)
}

func (s *Server) listSeatRecovery(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	items, total, err := s.store.SeatRecoveryTasks(r.URL.Query().Get("active") == "true", 50, (page-1)*50)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	s.recovery.mu.Lock()
	running := map[string]bool{}
	for id, v := range s.recovery.active {
		running[id] = v
	}
	s.recovery.mu.Unlock()
	writeAPI(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 50, "running": running, "server_now": time.Now()}, "")
}

func (s *Server) controlSeatRecovery(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(w, r, &input, 4096); err != nil {
		return
	}
	id := r.PathValue("id")
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	p, err := s.store.SeatRecoveryTask(id)
	if err != nil {
		writeAPI(w, 404, nil, "任务不存在")
		return
	}
	if p.Finished {
		writeAPI(w, 409, nil, "任务已结束")
		return
	}
	if input.Action != "pause" && input.Action != "resume" && input.Action != "retry" && input.Action != "cancel" {
		writeAPI(w, 400, nil, "操作无效")
		return
	}
	if input.Action == "retry" && (p.Status != "failed" || s.recovery.active[id]) {
		writeAPI(w, 409, nil, "仅可重试已停止的失败任务")
		return
	}
	if input.Action == "cancel" && (p.Stage != "login" || s.recovery.active[id]) {
		writeAPI(w, 409, nil, "仅尚未登录、未发送进入请求的任务可取消；已进入的任务请继续完成清理")
		return
	}
	p, err = s.store.UpdateSeatRecoveryTask(id, func(p *model.SeatRecoveryTask) {
		switch input.Action {
		case "pause":
			p.Paused = true
		case "resume":
			p.Paused = false
		case "retry":
			p.Paused = false
			p.Status = "queued"
			p.Message = "等待核实远端状态后续跑"
			p.NextAt = time.Now()
			p.Checks = 0
			p.RetryMutation = true
			// Keep successful/ambiguous mutation flags: reconciliation runs first.
		case "cancel":
			p.Finished = true
			p.Status = "cancelled"
			p.Message = "已取消，未执行进入空间"
		}
	})
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	s.recoveryLog(p, "操作："+input.Action)
	writeAPI(w, 200, p, "")
}

func (s *Server) seatRecoveryLogs(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	items, err := s.store.SeatRecoveryLogs(r.PathValue("id"), before)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"items": items}, "")
}

func (s *Server) recoveryLog(p model.SeatRecoveryTask, message string) {
	message = redactSensitiveText(message)
	if len(message) > 3000 {
		message = message[:3000]
	}
	_ = s.store.AppendSeatRecoveryLog(p.ID, p.Stage, message)
}

func (s *Server) startSeatRecoveryMonitor(parent context.Context) {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	if s.recovery.closed || s.recovery.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.recovery.cancel = cancel
	s.recovery.active = map[string]bool{}
	s.recovery.wg.Add(1)
	go func() {
		defer s.recovery.wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			s.dispatchSeatRecovery(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *Server) stopSeatRecovery() {
	s.recovery.mu.Lock()
	s.recovery.closed = true
	if s.recovery.cancel != nil {
		s.recovery.cancel()
	}
	s.recovery.mu.Unlock()
	s.recovery.wg.Wait()
}
func (s *Server) dispatchSeatRecovery(ctx context.Context) {
	items, _, err := s.store.SeatRecoveryTasks(true, 1000, 0)
	if err != nil {
		return
	}
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	if s.recovery.closed || ctx.Err() != nil {
		return
	}
	busyTeams := map[string]bool{}
	for _, p := range items {
		if s.recovery.active[p.ID] && !s.recovery.preparing[p.ID] {
			busyTeams[p.TeamID] = true
		}
	}
	for _, p := range items {
		if s.recovery.active[p.ID] || p.Status == "failed" || p.NextAt.After(time.Now()) || (p.Paused && !p.Lane) {
			continue
		}
		if s.recovery.active == nil {
			s.recovery.active = map[string]bool{}
		}
		if s.recovery.preparing == nil {
			s.recovery.preparing = map[string]bool{}
		}
		// Preparation has its own concurrency budget. Slow logins must not hold
		// up confirmations, ordinary-seat cleanup, or another mother's work.
		if p.Stage == "login" {
			if len(s.recovery.preparing) >= max(1, p.Settings.Concurrency) {
				continue
			}
			s.recovery.preparing[p.ID] = true
		} else {
			if busyTeams[p.TeamID] {
				continue
			}
			busyTeams[p.TeamID] = true
		}
		s.recovery.active[p.ID] = true
		s.recovery.wg.Add(1)
		go func(p model.SeatRecoveryTask) {
			defer s.recovery.wg.Done()
			defer func() {
				s.recovery.mu.Lock()
				delete(s.recovery.active, p.ID)
				delete(s.recovery.preparing, p.ID)
				s.recovery.mu.Unlock()
			}()
			s.runSeatRecoveryStep(ctx, p.ID)
		}(p)
	}
}

func (s *Server) recoverySave(p *model.SeatRecoveryTask) error {
	saved, err := s.store.UpdateSeatRecoveryTask(p.ID, func(latest *model.SeatRecoveryTask) {
		paused, finished := latest.Paused, latest.Finished
		*latest = *p
		latest.Paused = paused
		latest.Finished = finished || p.Finished
	})
	if err == nil {
		*p = saved
	}
	return err
}
func (s *Server) recoveryWait(p *model.SeatRecoveryTask, message string, seconds int) error {
	changed := p.Message != message || p.Status != "waiting"
	p.Status, p.Message = "waiting", message
	p.NextAt = time.Now().Add(time.Duration(seconds) * time.Second)
	if changed {
		s.recoveryLog(*p, message)
	}
	return s.recoverySave(p)
}
func (s *Server) recoveryAdvance(p *model.SeatRecoveryTask, stage, message string) error {
	p.Stage, p.Status, p.Message = stage, "queued", message
	p.Checks = 0
	p.NextAt = time.Now()
	s.recoveryLog(*p, message)
	return s.recoverySave(p)
}

func (s *Server) recoveryLogin(p model.SeatRecoveryTask) error {
	login := s.executeChatGPTAT
	if s.recovery.login != nil {
		login = s.recovery.login
	}
	result, err := login(p.Email, func(message string) { s.recoveryLog(p, message) }, func(event protocolOAuthDiagnostic) {
		s.recoveryLog(p, event.Stage+" / "+event.Event+": "+event.Message)
	})
	if err != nil {
		return err
	}
	if result == nil || result["success"] != true {
		message, _ := result["error"].(string)
		if message == "" {
			message = "临时 AT 登录失败"
		}
		return errors.New(message)
	}
	at, _ := result["access_token"].(string)
	if strings.TrimSpace(at) == "" {
		return errors.New("临时 AT 登录未返回 access_token")
	}
	session, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.store.SaveSeatRecoveryLogin(p.Email, at, string(session))
}
