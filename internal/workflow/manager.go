package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"chatgpt-space-merge/internal/model"
)

type HistoryStore interface {
	AddHistory(model.HistoryEntry) error
}

type tokenUser struct {
	token string
	info  model.UserInfo
	err   error
}

type jobRecord struct {
	job    model.Job
	cancel context.CancelFunc
}

type Manager struct {
	mu      sync.RWMutex
	jobs    map[string]*jobRecord
	counter atomic.Uint64
	history HistoryStore
}

type StartInput struct {
	AdminToken        string
	UserTokens        []string
	TeamOverride      string
	Settings          model.Settings
	RefreshAdminToken func(context.Context) (string, error)
}

func NewManager(history HistoryStore) *Manager {
	return &Manager{jobs: make(map[string]*jobRecord), history: history}
}

func (m *Manager) Start(input StartInput) (model.Job, error) {
	admin, err := DecodeUserInfo(input.AdminToken)
	if err != nil {
		return model.Job{}, fmt.Errorf("母号 AT 无效: %w", err)
	}
	teamID := input.TeamOverride
	if teamID == "" {
		teamID = admin.AccountID
	}
	if teamID == "" {
		return model.Job{}, errors.New("无法从母号 AT 读取团队 ID，请手动填写")
	}
	client, err := NewClient(input.Settings)
	if err != nil {
		return model.Job{}, err
	}
	if len(input.UserTokens) == 0 {
		return model.Job{}, errors.New("至少需要一个子号 AT")
	}

	users := make([]tokenUser, 0, len(input.UserTokens))
	results := make([]model.AccountResult, 0, len(input.UserTokens))
	seenUsers := make(map[string]int)
	for index, token := range input.UserTokens {
		info, parseErr := DecodeUserInfo(token)
		if parseErr == nil {
			if firstLine, exists := seenUsers[info.UserID]; exists {
				parseErr = fmt.Errorf("重复子号（与第 %d 行相同）", firstLine)
			} else {
				seenUsers[info.UserID] = index + 1
			}
		}
		users = append(users, tokenUser{token: token, info: info, err: parseErr})
		result := model.AccountResult{Index: index + 1, User: info, Status: "queued", Steps: newSteps()}
		if parseErr != nil {
			result.Status, result.Error = "failed", parseErr.Error()
			result.User.Email = fmt.Sprintf("第 %d 行", index+1)
		}
		results = append(results, result)
	}
	id := strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + strconv.FormatUint(m.counter.Add(1), 36)
	ctx, cancel := context.WithCancel(context.Background())
	job := model.Job{ID: id, Status: "queued", TeamAccountID: teamID, Admin: admin, Total: len(users), Results: results, CreatedAt: time.Now()}
	record := &jobRecord{job: job, cancel: cancel}
	m.mu.Lock()
	m.jobs[id] = record
	m.pruneLocked()
	m.mu.Unlock()
	go m.run(ctx, record, client, input, users)
	return cloneJob(job), nil
}

func newSteps() []model.Step {
	return []model.Step{
		{Key: "invite", Name: "邀请加入团队", Status: "pending"},
		{Key: "accept", Name: "接受邀请", Status: "pending"},
		{Key: "transfer", Name: "合并个人空间", Status: "pending"},
		{Key: "kick", Name: "移出团队", Status: "pending"},
	}
}

func (m *Manager) run(ctx context.Context, record *jobRecord, client *Client, input StartInput, users []tokenUser) {
	now := time.Now()
	m.update(record, func(job *model.Job) { job.Status, job.StartedAt = "running", &now })
	// The upstream account workflow is stateful per user. Keep the entire
	// invite -> accept -> transfer -> kick sequence serialized so the next
	// child cannot start until the previous child has fully finished.
	adminToken := input.AdminToken
	for index, user := range users {
		if user.err != nil {
			m.finishInvalid(record, index)
			continue
		}
		if ctx.Err() != nil {
			m.markCancelled(record, index)
			continue
		}
		failed := m.runUser(ctx, record, client, input, index, user, &adminToken)
		if failed && input.Settings.StopOnFirstFailure {
			record.cancel()
		}
		if index < len(users)-1 && ctx.Err() == nil && input.Settings.AccountIntervalSeconds > 0 {
			_ = sleepContext(ctx, time.Duration(input.Settings.AccountIntervalSeconds)*time.Second)
		}
	}
	completed := time.Now()
	m.update(record, func(job *model.Job) {
		job.CompletedAt = &completed
		switch {
		case job.Status == "cancelling" || ctx.Err() != nil && job.Completed < job.Total:
			job.Status = "cancelled"
		case job.Succeeded == job.Total:
			job.Status = "completed"
		case job.Succeeded > 0:
			job.Status = "partial"
		default:
			job.Status = "failed"
		}
	})
	job := m.snapshot(record)
	entry := model.HistoryEntry{ID: job.ID, Status: job.Status, TeamAccountID: job.TeamAccountID, AdminEmail: job.Admin.Email, Total: job.Total, Succeeded: job.Succeeded, Failed: job.Failed, CreatedAt: job.CreatedAt, CompletedAt: completed}
	for _, result := range job.Results {
		entry.Results = append(entry.Results, model.HistoryResult{Email: result.User.Email, Status: result.Status, Error: result.Error})
	}
	_ = m.history.AddHistory(entry)
}

func (m *Manager) finishInvalid(record *jobRecord, index int) {
	now := time.Now()
	m.update(record, func(job *model.Job) {
		job.Results[index].CompletedAt = &now
		job.Completed++
		job.Failed++
	})
}

func (m *Manager) runUser(ctx context.Context, record *jobRecord, client *Client, input StartInput, index int, user tokenUser, adminToken *string) bool {
	started := time.Now()
	m.update(record, func(job *model.Job) { job.Results[index].Status, job.Results[index].StartedAt = "running", &started })
	invited := false
	steps := []struct {
		key   string
		delay int
		call  func(context.Context) (Response, error)
	}{
		{"invite", input.Settings.InviteDelaySeconds, func(c context.Context) (Response, error) {
			return m.callAdmin(c, input, adminToken, func(token string) (Response, error) {
				return client.Invite(c, token, record.job.TeamAccountID, user.info.Email)
			})
		}},
		{"accept", input.Settings.AcceptDelaySeconds, func(c context.Context) (Response, error) {
			return client.Accept(c, user.token, record.job.TeamAccountID, user.info.UserID)
		}},
		{"transfer", input.Settings.TransferDelaySeconds, func(c context.Context) (Response, error) {
			return client.Transfer(c, user.token, record.job.TeamAccountID)
		}},
		{"kick", 0, func(c context.Context) (Response, error) {
			return m.callAdmin(c, input, adminToken, func(token string) (Response, error) {
				return client.Kick(c, token, record.job.TeamAccountID, user.info.UserID)
			})
		}},
	}
	for stepIndex, item := range steps {
		if ctx.Err() != nil {
			m.markCancelled(record, index)
			return true
		}
		m.startStep(record, index, stepIndex)
		response, err := item.call(ctx)
		if err != nil {
			if ctx.Err() != nil {
				m.markCancelled(record, index)
				return true
			}
			m.failStep(record, index, stepIndex, response.StatusCode, err.Error())
			if invited && input.Settings.AutoCleanup && item.key != "kick" && ctx.Err() == nil {
				m.cleanupAfterFailure(ctx, record, client, input, adminToken, index, user.info.UserID)
			}
			m.finishUser(record, index, false, err.Error())
			return true
		}
		if item.key == "invite" {
			invited = true
		}
		m.completeStep(record, index, stepIndex, response)
		if item.delay > 0 && !sleepContext(ctx, time.Duration(item.delay)*time.Second) {
			m.markCancelled(record, index)
			return true
		}
	}
	m.finishUser(record, index, true, "")
	return false
}

func (m *Manager) callAdmin(ctx context.Context, input StartInput, adminToken *string, call func(string) (Response, error)) (Response, error) {
	response, err := call(*adminToken)
	if err == nil || response.StatusCode != http.StatusUnauthorized || input.RefreshAdminToken == nil {
		return response, err
	}
	refreshedToken, refreshErr := input.RefreshAdminToken(ctx)
	if refreshErr != nil {
		return response, fmt.Errorf("%v；母号自动续期失败: %w", err, refreshErr)
	}
	*adminToken = refreshedToken
	return call(*adminToken)
}

func (m *Manager) cleanupAfterFailure(ctx context.Context, record *jobRecord, client *Client, input StartInput, adminToken *string, index int, userID string) {
	stepIndex := 3
	m.startStep(record, index, stepIndex)
	response, err := m.callAdmin(ctx, input, adminToken, func(token string) (Response, error) {
		return client.Kick(ctx, token, record.job.TeamAccountID, userID)
	})
	if err != nil {
		m.failStep(record, index, stepIndex, response.StatusCode, "自动清理失败: "+err.Error())
		return
	}
	response.Message = "前序步骤失败，已自动移出团队"
	m.completeStep(record, index, stepIndex, response)
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *Manager) startStep(record *jobRecord, resultIndex, stepIndex int) {
	now := time.Now()
	m.update(record, func(job *model.Job) {
		result := &job.Results[resultIndex]
		result.CurrentStep = result.Steps[stepIndex].Key
		result.Steps[stepIndex].Status, result.Steps[stepIndex].StartedAt = "running", &now
	})
}

func (m *Manager) completeStep(record *jobRecord, resultIndex, stepIndex int, response Response) {
	now := time.Now()
	m.update(record, func(job *model.Job) {
		step := &job.Results[resultIndex].Steps[stepIndex]
		step.Status, step.HTTPStatus, step.Message, step.CompletedAt = "completed", response.StatusCode, response.Message, &now
	})
}

func (m *Manager) failStep(record *jobRecord, resultIndex, stepIndex, httpStatus int, message string) {
	now := time.Now()
	m.update(record, func(job *model.Job) {
		step := &job.Results[resultIndex].Steps[stepIndex]
		step.Status, step.HTTPStatus, step.Message, step.CompletedAt = "failed", httpStatus, message, &now
	})
}

func (m *Manager) finishUser(record *jobRecord, index int, success bool, message string) {
	now := time.Now()
	m.update(record, func(job *model.Job) {
		result := &job.Results[index]
		result.CompletedAt, result.CurrentStep, result.Error = &now, "", message
		job.Completed++
		if success {
			result.Status = "completed"
			job.Succeeded++
		} else {
			result.Status = "failed"
			job.Failed++
		}
	})
}

func (m *Manager) markCancelled(record *jobRecord, index int) {
	now := time.Now()
	m.update(record, func(job *model.Job) {
		result := &job.Results[index]
		if result.Status == "completed" || result.Status == "failed" || result.Status == "cancelled" {
			return
		}
		result.Status, result.Error, result.CompletedAt = "cancelled", "任务已停止", &now
		for stepIndex := range result.Steps {
			if result.Steps[stepIndex].Status == "running" {
				result.Steps[stepIndex].Status, result.Steps[stepIndex].CompletedAt = "cancelled", &now
			}
		}
		job.Completed++
		job.Failed++
	})
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.jobs[id]
	if !ok {
		return errors.New("任务不存在")
	}
	if record.job.Status != "queued" && record.job.Status != "running" {
		return errors.New("任务已经结束")
	}
	record.job.Status = "cancelling"
	record.cancel()
	return nil
}

func (m *Manager) Get(id string) (model.Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.jobs[id]
	if !ok {
		return model.Job{}, false
	}
	return cloneJob(record.job), true
}

func (m *Manager) update(record *jobRecord, action func(*model.Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	action(&record.job)
}

func (m *Manager) snapshot(record *jobRecord) model.Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneJob(record.job)
}

func (m *Manager) pruneLocked() {
	if len(m.jobs) <= 100 {
		return
	}
	for id, record := range m.jobs {
		if record.job.Status != "running" && record.job.Status != "queued" && record.job.Status != "cancelling" {
			delete(m.jobs, id)
			return
		}
	}
}

func cloneJob(job model.Job) model.Job {
	data, _ := json.Marshal(job)
	var cloned model.Job
	_ = json.Unmarshal(data, &cloned)
	return cloned
}
