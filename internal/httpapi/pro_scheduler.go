package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"
)

type proScheduledContextKey struct{}

func (s *Server) proScheduleStatus() (model.ProScheduleStatus, error) {
	v, err := s.store.ProCapacity()
	if err != nil {
		return v, err
	}
	cfg, _, _, err := s.store.ProSettings()
	if err != nil {
		return v, err
	}
	v.Enabled, v.Maximum = cfg.ScheduledEnabled, cfg.MaxUnmerged
	v.Needed = max(0, v.Maximum-v.OpenedUnmerged-v.Pending)
	cards, _, err := s.store.GPTPayCards(10000, 0, true)
	if err != nil {
		return v, err
	}
	// Duplicate rows for the same physical card must not inflate capacity.
	seen := map[string]bool{}
	for _, card := range cards {
		_, secret, e := s.store.GPTPayCard(card.ID)
		if e != nil {
			return v, e
		}
		if seen[secret.Number] {
			continue
		}
		seen[secret.Number] = true
		v.AvailableCards++
		v.CardCapacity += card.RemainingAccounts
	}
	v.NextCheckAt, err = s.store.ProScheduleNext()
	if err != nil {
		return v, err
	}
	v.Active, err = s.store.ActiveProScheduleRun()
	return v, err
}
func (s *Server) getProSchedule(w http.ResponseWriter, r *http.Request) {
	v, err := s.proScheduleStatus()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) listProScheduleRuns(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	page = max(1, page)
	if size < 1 {
		size = 10
	}
	size = min(size, 100)
	v, total, err := s.store.ProScheduleRuns(size, (page-1)*size)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"items": v, "total": total}, "")
}
func (s *Server) runProScheduleNow(w http.ResponseWriter, r *http.Request) {
	s.tickProSchedule(true)
	s.getProSchedule(w, r)
}

func (s *Server) reconcileProSchedule(run *model.ProScheduleRun) bool {
	pending, failed := 0, 0
	for i := range run.Tasks {
		t := &run.Tasks[i]
		if t.Status == "failed" || t.Status == "completed" {
			if t.Status == "failed" {
				failed++
			}
			continue
		}
		p, _, err := s.store.MailAccountCredential(t.Email)
		if err != nil {
			t.Status, t.Error = "failed", "账号已不存在"
			failed++
			continue
		}
		t.Stage, t.Steps, t.Error = p.ProAuto.Stage, p.ProAuto.Steps, p.ProAuto.Error
		if p.ProAuto.ProvisionedAt != nil {
			t.Status = "completed"
			t.Error = ""
			continue
		}
		if s.proAutomationRunning(t.Email) || p.ProAuto.Status == "waiting_quota" {
			t.Status = "running"
			pending++
			continue
		}
		t.Status = "failed"
		failed++
		if t.Error == "" {
			t.Error = "任务已中断，请检查账号日志后手动续跑；不会重新付款"
		}
	}
	if pending == 0 {
		now := time.Now()
		run.FinishedAt = &now
		run.Status = "completed"
		if failed > 0 {
			run.Status = "failed"
		}
		run.Message = fmt.Sprintf("本批 %d 个账号，%d 个已推送并成功刷新额度，%d 个失败", len(run.Tasks), len(run.Tasks)-failed, failed)
	}
	if err := s.store.SaveProScheduleRun(*run); err != nil {
		return false
	}
	return pending == 0
}

// A tick only claims and queues bounded work; it never waits for login/payment.
// The active persisted batch blocks the next one until its initial quota reads
// succeeded, even while each account sleeps between quota retries.
func (s *Server) tickProSchedule(force bool) {
	if !s.proScheduleMu.TryLock() {
		return
	}
	defer s.proScheduleMu.Unlock()
	s.proAutoMu.Lock()
	closed := s.proAutoClosed
	s.proAutoMu.Unlock()
	if closed {
		return
	}
	cfg, password, _, err := s.store.ProSettings()
	if err != nil {
		return
	}
	active, err := s.store.ActiveProScheduleRun()
	if err != nil {
		return
	}
	if active != nil && !s.reconcileProSchedule(active) {
		return
	}
	if !cfg.ScheduledEnabled && !force {
		return
	}
	next, err := s.store.ProScheduleNext()
	if err != nil {
		return
	}
	if !force && next != nil && next.After(time.Now()) {
		return
	}
	if err = s.store.SetProScheduleNext(time.Now().Add(time.Duration(cfg.ScheduleIntervalSeconds) * time.Second)); err != nil {
		return
	}
	run := model.ProScheduleRun{ID: randomRegistrationID(), Status: "running", Trigger: "timer", StartedAt: time.Now(), Tasks: []model.ProScheduleTask{}}
	if force {
		run.Trigger = "manual"
	}
	finish := func(status, message string) {
		now := time.Now()
		run.Status, run.Message, run.FinishedAt = status, message, &now
		_ = s.store.SaveProScheduleRun(run)
	}
	pay, key, err := s.store.GPTPaySettings()
	if err != nil || key == "" || cfg.Provider != "sub2" || cfg.Sub2.URL == "" || cfg.Sub2.Email == "" || password == "" || len(cfg.Sub2.GroupIDs) == 0 || cfg.TargetAdminID == "" {
		finish("failed", "开通配置不完整，请检查 GPTPay、Sub2 推送和目标母号")
		return
	}
	capacity, err := s.store.ProCapacity()
	if err != nil {
		finish("failed", "读取开通容量失败")
		return
	}
	needed := min(cfg.Concurrency, max(0, cfg.MaxUnmerged-capacity.OpenedUnmerged-capacity.Pending))
	if needed == 0 {
		finish("skipped", "已开通未合并及在途数量达到上限，无需补充")
		return
	}
	emails, err := s.store.ProScheduleCandidates(needed)
	if err != nil {
		finish("failed", "读取待开通账号失败")
		return
	}
	if len(emails) == 0 {
		finish("skipped", "Pro 管理中没有可自动开通的新账号")
		return
	}
	if err = s.store.SaveProScheduleRun(run); err != nil {
		return
	}
	for _, email := range emails {
		// Recheck after every startup, including manual orders competing for cards.
		cards, _, e := s.store.GPTPayCards(1, 0, true)
		if e != nil || len(cards) == 0 {
			run.Message = "银行卡无剩余可用名额"
			break
		}
		card := cards[0]
		task := model.ProScheduleTask{Email: email, CardID: card.ID, Status: "queued"}
		run.Tasks = append(run.Tasks, task)
		if e = s.store.SaveProScheduleRun(run); e != nil {
			return
		}
		body, _ := json.Marshal(map[string]string{"card_id": card.ID, "plan_code": pay.PlanCode})
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))).WithContext(context.WithValue(context.Background(), proScheduledContextKey{}, true))
		r.SetPathValue("email", email)
		w := httptest.NewRecorder()
		s.startProAutomation(w, r)
		last := &run.Tasks[len(run.Tasks)-1]
		if w.Code != http.StatusAccepted {
			var reply struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &reply)
			last.Status = "failed"
			last.Error = firstNonEmpty(reply.Error, "账号未启动，请检查配置或账号占用情况")
		} else {
			last.Status = "running"
		}
		if e = s.store.SaveProScheduleRun(run); e != nil {
			return
		}
	}
	if len(run.Tasks) == 0 {
		finish("skipped", firstNonEmpty(run.Message, "无可执行账号"))
		return
	}
	s.reconcileProSchedule(&run)
}
