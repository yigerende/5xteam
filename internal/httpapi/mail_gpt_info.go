package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

const mailGPTInfoConcurrency = 4

type mailInfoTask struct {
	Email           string              `json:"email"`
	Status          string              `json:"status"`
	PlanType        string              `json:"plan_type,omitempty"`
	CreatedAtOpenAI *time.Time          `json:"created_at_openai,omitempty"`
	Check           *model.GPTInfoCheck `json:"check,omitempty"`
	Error           string              `json:"error,omitempty"`
	accountID       string
}
type mailInfoJob struct {
	ID          string
	Tasks       []*mailInfoTask
	CreatedAt   time.Time
	CompletedAt time.Time
}

func (s *Server) startMailGPTInfo(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Emails []string `json:"emails"`
	}
	if email := r.PathValue("email"); email != "" {
		input.Emails = []string{email}
	} else if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	if len(input.Emails) == 0 || len(input.Emails) > 5000 {
		writeAPI(w, 400, nil, "请选择 1 至 5000 个邮件账号")
		return
	}
	settings := s.store.Settings()
	if strings.TrimSpace(settings.ProxyURL) == "" {
		writeAPI(w, 400, nil, "请先配置全局代理，GPT 信息查询禁止直连")
		return
	}
	if _, err := workflow.ValidateProxyURL(settings.ProxyURL); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	candidates := make([]*mailInfoTask, 0, len(input.Emails))
	seen := map[string]bool{}
	for _, raw := range input.Emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if !strings.Contains(email, "@") {
			writeAPI(w, 400, nil, "邮箱地址无效")
			return
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		item := &mailInfoTask{Email: email, Status: "queued"}
		profile, _, err := s.store.MailAccountCredential(email)
		if err != nil {
			item.Status, item.Error = "failed", "邮箱账号不存在"
		} else if profile.ManagementScope == "pro" {
			item.Status, item.Error = "failed", "账号已移至 Pro 管理"
		} else {
			item.accountID = profile.ID
		}
		candidates = append(candidates, item)
	}
	s.mailInfoMu.Lock()
	if s.mailInfoClosed {
		s.mailInfoMu.Unlock()
		writeAPI(w, 503, nil, "服务正在停止，请稍后重试")
		return
	}
	if s.mailInfoJobs == nil {
		s.mailInfoJobs = map[string]*mailInfoJob{}
		s.mailInfoActive = map[string]*mailInfoTask{}
		s.mailInfoSlots = make(chan struct{}, mailGPTInfoConcurrency)
	}
	s.pruneMailInfoJobsLocked()
	if len(s.mailInfoJobs) >= 100 {
		s.mailInfoMu.Unlock()
		writeAPI(w, 429, nil, "刷新任务过多，请稍后重试")
		return
	}
	job := &mailInfoJob{ID: randomRegistrationID(), CreatedAt: time.Now()}
	work := make([]*mailInfoTask, 0, len(candidates))
	for _, task := range candidates {
		if active := s.mailInfoActive[task.Email]; active != nil && active.accountID == task.accountID {
			job.Tasks = append(job.Tasks, active)
			continue
		}
		job.Tasks = append(job.Tasks, task)
		if task.Status == "queued" {
			s.mailInfoActive[task.Email] = task
			work = append(work, task)
		}
	}
	s.mailInfoJobs[job.ID] = job
	s.mailInfoWG.Add(1)
	s.mailInfoMu.Unlock()
	go func() { defer s.mailInfoWG.Done(); s.runMailInfoTasks(work, settings) }()
	writeAPI(w, http.StatusAccepted, map[string]any{"success": true, "job_id": job.ID}, "")
}

func (s *Server) pruneMailInfoJobsLocked() {
	now := time.Now()
	for id, job := range s.mailInfoJobs {
		finished := true
		for _, task := range job.Tasks {
			if task.Status == "queued" || task.Status == "running" {
				finished = false
				break
			}
		}
		if finished && job.CompletedAt.IsZero() {
			job.CompletedAt = now
		}
		if !job.CompletedAt.IsZero() && now.Sub(job.CompletedAt) > 15*time.Minute {
			delete(s.mailInfoJobs, id)
		}
	}
}

func (s *Server) mailGPTInfoStatus(w http.ResponseWriter, r *http.Request) {
	s.mailInfoMu.Lock()
	s.pruneMailInfoJobsLocked()
	job := s.mailInfoJobs[r.URL.Query().Get("job_id")]
	if job == nil {
		s.mailInfoMu.Unlock()
		writeAPI(w, 404, nil, "刷新任务已过期或服务已重启，已保存的信息不会丢失")
		return
	}
	total, done, okCount, fail, partial := len(job.Tasks), 0, 0, 0, 0
	// Progress is paginated; large batches do not resend thousands of rows on
	// every poll. Counters still describe the entire selected batch.
	page := s.parsePagination(r)
	start, end := min(page.Offset, total), min(page.Offset+page.Limit, total)
	items := make([]mailInfoTask, 0, end-start)
	for i, task := range job.Tasks {
		switch task.Status {
		case "success":
			done++
			okCount++
		case "partial":
			done++
			partial++
		case "failed":
			done++
			fail++
		}
		if i >= start && i < end {
			items = append(items, *task)
		}
	}
	status := "running"
	if done == total {
		status = "done"
	}
	s.mailInfoMu.Unlock()
	writeAPI(w, 200, map[string]any{"job": map[string]any{
		"job_id": job.ID, "total": total, "done": done, "ok": okCount, "fail": fail, "partial": partial,
		"status": status, "results": items,
	}}, "")
}

func (s *Server) runMailInfoTasks(tasks []*mailInfoTask, settings model.Settings) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.auditStop:
			cancel()
		}
	}()
	var wg sync.WaitGroup
	queue := make(chan *mailInfoTask)
	for i := 0; i < min(mailGPTInfoConcurrency, len(tasks)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range queue {
				select {
				case s.mailInfoSlots <- struct{}{}:
					s.refreshMailInfoTask(ctx, task, settings)
					<-s.mailInfoSlots
				case <-ctx.Done():
					s.completeMailInfoTask(task, model.MailAccountProfile{}, model.MailGPTInfoResult{}, fmt.Errorf("服务已停止，查询已中断"))
				}
			}
		}()
	}
	for _, task := range tasks {
		queue <- task
	}
	close(queue)
	wg.Wait()
	s.mailInfoMu.Lock()
	s.pruneMailInfoJobsLocked()
	s.mailInfoMu.Unlock()
}

func (s *Server) refreshMailInfoTask(parent context.Context, task *mailInfoTask, settings model.Settings) {
	s.mailInfoMu.Lock()
	task.Status = "running"
	s.mailInfoMu.Unlock()
	profile, credentials, err := s.store.MailAccountCredential(task.Email)
	var result model.MailGPTInfoResult
	if err == nil && (profile.ID != task.accountID || profile.ManagementScope == "pro") {
		err = fmt.Errorf("账号已删除、重新创建或移至 Pro 管理")
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
		defer cancel()
		switch {
		case strings.TrimSpace(credentials.AccessToken) == "":
			err = fmt.Errorf("请先获取 AT")
		default:
			if claims, decodeErr := workflow.DecodeUserInfo(credentials.AccessToken); decodeErr == nil && claims.Email != "" && !strings.EqualFold(claims.Email, task.Email) {
				err = fmt.Errorf("保存的 AT 与邮件账号不匹配")
				break
			}
			// Reuse Pro's shared 400-700 ms launch pacing across all batches.
			if err = s.waitForProPlanCheckSlot(ctx); err != nil {
				break
			}
			if s.mailInfoRefresh != nil {
				result = s.mailInfoRefresh(ctx, settings, credentials.AccessToken)
			} else {
				var client *workflow.Client
				client, err = workflow.NewClient(settings)
				if err == nil {
					result = client.RefreshAccountInfo(ctx, credentials.AccessToken)
				}
			}
		}
		if err != nil {
			result.Check = model.GPTInfoCheck{Status: "failed", CheckedAt: time.Now().UTC(), Error: err.Error(),
				Plan: model.GPTInfoRequestStatus{Error: err.Error()}, Me: model.GPTInfoRequestStatus{Error: err.Error()}}
		}
		var saveErr error
		profile, saveErr = s.store.SaveMailGPTInfo(task.Email, task.accountID, credentials.AccessToken, result)
		if saveErr != nil {
			err = saveErr
		}
	}
	s.completeMailInfoTask(task, profile, result, err)
}

func (s *Server) completeMailInfoTask(task *mailInfoTask, profile model.MailAccountProfile, result model.MailGPTInfoResult, err error) {
	s.mailInfoMu.Lock()
	defer s.mailInfoMu.Unlock()
	task.Status, task.Check = result.Check.Status, &result.Check
	task.PlanType, task.CreatedAtOpenAI = profile.CurrentPlanType, profile.CreatedAtOpenAI
	task.Error = result.Check.Error
	if err != nil {
		task.Status, task.Error = "failed", err.Error()
	}
	if task.Status == "" {
		task.Status = "failed"
	}
	if s.mailInfoActive[task.Email] == task {
		delete(s.mailInfoActive, task.Email)
	}
}

func (s *Server) stopMailInfoJobs() {
	s.mailInfoMu.Lock()
	s.mailInfoClosed = true
	s.mailInfoMu.Unlock()
	s.mailInfoWG.Wait()
}
