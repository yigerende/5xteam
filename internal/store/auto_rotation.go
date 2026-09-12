package store

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"chatgpt-space-merge/internal/model"
)

var autoRotationEventSequence atomic.Uint64

func nextAutoRotationEventID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(autoRotationEventSequence.Add(1), 36)
}

func (s *Store) AutoRotationSettings() model.AutoRotationSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := model.DefaultAutoRotationSettings()
	var raw string
	if s.db.QueryRow("SELECT payload FROM auto_rotation_settings WHERE id=1").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &settings)
	}
	if settings.ThresholdPercent <= 0 || settings.ThresholdPercent > 100 {
		settings.ThresholdPercent = 50
	}
	if settings.IntervalSeconds < 10 {
		settings.IntervalSeconds = 300
	}
	if settings.TeamOperationIntervalSeconds < 0 {
		settings.TeamOperationIntervalSeconds = 10
	}
	if settings.TeamOperationIntervalSeconds > 120 {
		settings.TeamOperationIntervalSeconds = 120
	}
	if settings.Concurrency < 1 {
		settings.Concurrency = 2
	}
	if settings.Concurrency > 20 {
		settings.Concurrency = 20
	}
	if settings.MaxPerRun < 0 {
		settings.MaxPerRun = 0
	}
	if settings.RetryCount < 0 {
		settings.RetryCount = 0
	}
	if settings.RemoveMethod != "child_leave" {
		settings.RemoveMethod = "mother_kick"
	}
	if settings.OAuthLoginMode != "password_totp" {
		settings.OAuthLoginMode = "email_otp"
	}
	return settings
}

func (s *Store) SaveAutoRotationSettings(settings model.AutoRotationSettings) (model.AutoRotationSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if settings.ThresholdPercent <= 0 || settings.ThresholdPercent > 100 {
		return settings, errors.New("额度阈值必须在 1 到 100 之间")
	}
	if settings.IntervalSeconds < 10 || settings.IntervalSeconds > 86400 {
		return settings, errors.New("检查间隔必须在 10 到 86400 秒之间")
	}
	if settings.TeamOperationIntervalSeconds < 0 || settings.TeamOperationIntervalSeconds > 120 {
		return settings, errors.New("同母号操作间隔必须在 0 到 120 秒之间")
	}
	if settings.Concurrency < 1 || settings.Concurrency > 20 {
		return settings, errors.New("自动轮转并发数必须在 1 到 20 之间")
	}
	if settings.MaxPerRun < 0 || settings.MaxPerRun > 500 {
		return settings, errors.New("每轮最大补充数必须在 0 到 500 之间")
	}
	if settings.RetryCount < 0 || settings.RetryCount > 10 {
		return settings, errors.New("重试次数必须在 0 到 10 之间")
	}
	if settings.RemoveMethod == "" {
		settings.RemoveMethod = "mother_kick"
	}
	if settings.RemoveMethod != "mother_kick" && settings.RemoveMethod != "child_leave" {
		return settings, errors.New("移出方式无效")
	}
	if settings.OAuthLoginMode == "" {
		settings.OAuthLoginMode = "email_otp"
	}
	if settings.OAuthLoginMode != "email_otp" && settings.OAuthLoginMode != "password_totp" {
		return settings, errors.New("OAuth 登录方式无效")
	}
	b, _ := json.Marshal(settings)
	_, err := s.db.Exec("INSERT INTO auto_rotation_settings(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", string(b))
	return settings, err
}

func (s *Store) SaveAutoRotationRun(run model.AutoRotationRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT OR REPLACE INTO auto_rotation_runs(id,payload,started_at) VALUES(?,?,?)", run.ID, string(b), formatTime(run.StartedAt))
	return err
}
func (s *Store) UpdateAutoRotationRun(run model.AutoRotationRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE auto_rotation_runs SET payload=? WHERE id=?", string(b), run.ID)
	return err
}
func (s *Store) AutoRotationRuns() []model.AutoRotationRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT payload FROM auto_rotation_runs ORDER BY started_at DESC LIMIT 100")
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := []model.AutoRotationRun{}
	for rows.Next() {
		var raw string
		var item model.AutoRotationRun
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			result = append(result, item)
		}
	}
	return result
}

// PurgeAutoRotationHistory removes diagnostic and batch records older than
// the supplied retention window. Auto-rotation history is intentionally
// bounded so the SQLite file and the history endpoint remain fast over time.
func (s *Store) PurgeAutoRotationHistory(olderThan time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := formatTime(olderThan)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"DELETE FROM auto_rotation_events WHERE created_at < ?",
		"DELETE FROM auto_rotation_tasks WHERE updated_at < ?",
		"DELETE FROM auto_rotation_runs WHERE started_at < ?",
	} {
		if _, err := tx.Exec(statement, cutoff); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) SaveAutoRotationTask(task model.AutoRotationTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT OR REPLACE INTO auto_rotation_tasks(id,run_id,account_id,payload,updated_at) VALUES(?,?,?,?,?)", task.ID, task.RunID, task.AccountID, string(b), formatTime(time.Now()))
	return err
}
func (s *Store) UpdateAutoRotationTask(task model.AutoRotationTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE auto_rotation_tasks SET payload=?,updated_at=? WHERE id=?", string(b), formatTime(time.Now()), task.ID)
	return err
}
func (s *Store) AutoRotationTasks(runID string) []model.AutoRotationTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, args := "SELECT payload FROM auto_rotation_tasks ORDER BY updated_at DESC LIMIT 1000", []any{}
	if strings.TrimSpace(runID) != "" {
		q = "SELECT payload FROM auto_rotation_tasks WHERE run_id=? ORDER BY updated_at DESC"
		args = []any{runID}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := []model.AutoRotationTask{}
	for rows.Next() {
		var raw string
		var item model.AutoRotationTask
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			result = append(result, item)
		}
	}
	return result
}

func (s *Store) AddAutoRotationEvent(event model.AutoRotationEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ID == "" {
		event.ID = nextAutoRotationEventID()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO auto_rotation_events(id,run_id,task_id,account_id,payload,created_at) VALUES(?,?,?,?,?,?)", event.ID, event.RunID, event.TaskID, event.AccountID, string(b), formatTime(event.CreatedAt))
	return err
}

// AddAutoRotationEvents persists a group of audit events in one transaction.
// The HTTP layer uses this from its asynchronous audit writer so diagnostic
// logging never waits on a database commit in the business goroutine.
func (s *Store) AddAutoRotationEvents(events []model.AutoRotationEvent) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO auto_rotation_events(id,run_id,task_id,account_id,payload,created_at) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, event := range events {
		if event.ID == "" {
			event.ID = nextAutoRotationEventID()
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now()
		}
		b, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			continue
		}
		if _, err := stmt.Exec(event.ID, event.RunID, event.TaskID, event.AccountID, string(b), formatTime(event.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EnsureFreeAccountLifecycleTask(account model.FreeAccountProfile) (model.AutoRotationTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT payload FROM auto_rotation_tasks WHERE account_id=?", account.ID)
	if err != nil {
		return model.AutoRotationTask{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var task model.AutoRotationTask
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &task) == nil && task.Lifecycle {
			return task, nil
		}
	}
	if err := rows.Err(); err != nil {
		return model.AutoRotationTask{}, err
	}
	started := account.ImportedAt
	if started.IsZero() {
		started = account.CreatedAt
	}
	if started.IsZero() {
		started = time.Now()
	}
	task := model.AutoRotationTask{
		ID: "lifecycle-" + account.ID, AccountID: account.ID, Email: account.Email,
		Source: "lifecycle", Status: "lifecycle", Lifecycle: true, StartedAt: started,
		Steps: []model.AutoRotationStep{
			{Key: "invite", Name: "邀请并进入空间", Status: account.InviteStatus},
			{Key: "oauth", Name: "获取 Codex OAuth", Status: account.OAuthStatus},
			{Key: "push", Name: "推送当前下游", Status: account.PushStatus},
			{Key: "quota", Name: "查询额度", Status: account.QuotaStatus},
			{Key: "remove", Name: "移出空间", Status: account.RemoveStatus},
		},
	}
	b, err := json.Marshal(task)
	if err != nil {
		return model.AutoRotationTask{}, err
	}
	_, err = s.db.Exec("INSERT INTO auto_rotation_tasks(id,run_id,account_id,payload,updated_at) VALUES(?,?,?,?,?)", task.ID, "", account.ID, string(b), formatTime(time.Now()))
	return task, err
}

func (s *Store) AutoRotationEventsByAccount(accountID string) []model.AutoRotationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT payload FROM auto_rotation_events WHERE account_id=? ORDER BY created_at DESC", accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make([]model.AutoRotationEvent, 0)
	for rows.Next() {
		var raw string
		var event model.AutoRotationEvent
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &event) == nil {
			result = append(result, event)
		}
	}
	return result
}

func (s *Store) AutoRotationEvents(runID, taskID string) []model.AutoRotationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := "SELECT payload FROM auto_rotation_events"
	args := []any{}
	conditions := []string{}
	if strings.TrimSpace(runID) != "" {
		conditions = append(conditions, "run_id=?")
		args = append(args, runID)
	}
	if strings.TrimSpace(taskID) != "" {
		conditions = append(conditions, "task_id=?")
		args = append(args, taskID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT 5000"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := []model.AutoRotationEvent{}
	for rows.Next() {
		var raw string
		var item model.AutoRotationEvent
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			result = append(result, item)
		}
	}
	return result
}

// ClaimAutoRotationAccount atomically marks an account as being handled by a task.
func (s *Store) ClaimAutoRotationAccount(accountID, taskID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing string
	if err := s.db.QueryRow("SELECT task_id FROM auto_rotation_claims WHERE account_id=?", accountID).Scan(&existing); err == nil {
		return false, nil
	}
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM free_accounts WHERE id=?", accountID).Scan(&raw); err != nil {
		return false, err
	}
	var p model.FreeAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false, err
	}
	if p.InviteStatus == "running" || p.AcceptStatus == "running" || (p.TeamAccountID != "" && p.InviteStatus == "completed") {
		return false, nil
	}
	// Keep the user-visible pipeline error untouched while the task is claimed.
	p.UpdatedAt = time.Now()
	b, _ := json.Marshal(p)
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO auto_rotation_claims(account_id,task_id,created_at) VALUES(?,?,?)", accountID, taskID, formatTime(time.Now())); err != nil {
		return false, err
	}
	if _, err = tx.Exec("UPDATE free_accounts SET profile=?,updated_at=? WHERE id=?", string(b), formatTime(p.UpdatedAt), accountID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ReleaseAutoRotationClaim(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM auto_rotation_claims WHERE account_id=?", accountID)
	return err
}

// RecoverAutoRotationClaims removes process-local account claims. Task and
// seat-reservation records remain durable, so an account that already entered
// a team still retains its seat until the normal remove flow releases it.
func (s *Store) RecoverAutoRotationClaims() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM auto_rotation_claims")
	return err
}

// RecoverAutoRotationTasks closes tasks left in queued/running state by a
// previous server process. Those goroutines no longer exist after a restart,
// so keeping them active would incorrectly consume invitation capacity forever.
// The account's durable Team status remains authoritative; a task that had
// already entered the space is therefore still counted through free_accounts,
// while the stale task itself is no longer treated as in-flight.
func (s *Store) RecoverAutoRotationTasks() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	rows, err := s.db.Query("SELECT id, payload FROM auto_rotation_tasks WHERE json_extract(payload, '$.status') IN ('queued','running')")
	if err != nil {
		return err
	}
	defer rows.Close()
	type recoveredTask struct {
		id  string
		raw []byte
	}
	var tasks []recoveredTask
	for rows.Next() {
		var id, raw string
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		tasks = append(tasks, recoveredTask{id: id, raw: []byte(raw)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range tasks {
		var task model.AutoRotationTask
		if json.Unmarshal(item.raw, &task) != nil {
			continue
		}
		task.Status = "failed"
		task.Error = "服务重启，上一轮自动轮转任务已中断"
		task.CurrentStep = ""
		task.CompletedAt = &now
		for i := range task.Steps {
			if task.Steps[i].Status == "running" || task.Steps[i].Status == "pending" {
				task.Steps[i].Status = "failed"
				task.Steps[i].Message = task.Error
				task.Steps[i].CompletedAt = &now
			}
		}
		rawJSON, marshalErr := json.Marshal(task)
		if marshalErr != nil {
			continue
		}
		if _, err := s.db.Exec("UPDATE auto_rotation_tasks SET payload=?, updated_at=? WHERE id=?", string(rawJSON), formatTime(now), item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateSeatReservation(adminID, accountID, seatType string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM auto_rotation_seat_reservations WHERE active=1 AND admin_account_id=? AND seat_type=?", adminID, seatType).Scan(&n); err != nil {
		return "", err
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	_, err := s.db.Exec("INSERT INTO auto_rotation_seat_reservations(id,admin_account_id,account_id,seat_type,active,created_at) VALUES(?,?,?,?,1,?)", id, adminID, accountID, seatType, formatTime(time.Now()))
	return id, err
}
func (s *Store) ReleaseSeatReservation(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE auto_rotation_seat_reservations SET active=0 WHERE id=?", id)
	return err
}

func (s *Store) ReleaseSeatReservationByAccount(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE auto_rotation_seat_reservations SET active=0 WHERE account_id=?", accountID)
	return err
}
func (s *Store) ActiveSeatReservations(adminID, seatType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM auto_rotation_seat_reservations WHERE active=1 AND admin_account_id=? AND seat_type=?", adminID, seatType).Scan(&n)
	return n
}
