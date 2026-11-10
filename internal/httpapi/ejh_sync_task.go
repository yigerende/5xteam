package httpapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type ejhSyncResult struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type ejhSyncTask struct {
	result     ejhSyncResult // all reads/writes guarded by syncMu
	key        string
	start, end time.Time
	done       chan struct{}
	cancel     context.CancelFunc
}

func ejhRecentSyncKey(now time.Time) string {
	return "recent90/" + now.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
}

func (s *Server) requestEJHSync(key string, start, end time.Time) (*ejhSyncTask, error) {
	s.ejh.lifecycle.Lock()
	defer s.ejh.lifecycle.Unlock()
	if s.ejh.syncClosed {
		return nil, fmt.Errorf("服务正在停止，请稍后刷新")
	}
	s.ejh.syncMu.Lock()
	defer s.ejh.syncMu.Unlock()
	if s.ejh.syncJobs == nil {
		s.ejh.syncJobs = make(map[string]*ejhSyncTask)
	}
	active := 0
	for id, task := range s.ejh.syncJobs {
		if task.result.FinishedAt == nil {
			if task.key == key {
				return task, nil
			}
			active++
		} else if time.Since(*task.result.FinishedAt) > 10*time.Minute {
			delete(s.ejh.syncJobs, id)
		}
	}
	// Bound both queued work and retained results. Different date ranges must
	// never be reported as completed by an unrelated synchronization.
	if active >= 4 {
		return nil, fmt.Errorf("已有多个不同日期范围的同步任务，请等待完成后刷新")
	}
	for len(s.ejh.syncJobs) >= 32 {
		oldest := ""
		for id, task := range s.ejh.syncJobs {
			if task.result.FinishedAt != nil && (oldest == "" || task.result.FinishedAt.Before(*s.ejh.syncJobs[oldest].result.FinishedAt)) {
				oldest = id
			}
		}
		if oldest == "" {
			break
		}
		delete(s.ejh.syncJobs, oldest)
	}
	parent := s.ejh.ctx
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return nil, fmt.Errorf("服务正在停止，请稍后刷新")
	}
	ctx, cancel := context.WithCancel(parent)
	task := &ejhSyncTask{result: ejhSyncResult{ID: rand.Text(), Status: "queued", QueuedAt: time.Now()}, key: key, start: start, end: end, done: make(chan struct{}), cancel: cancel}
	s.ejh.syncJobs[task.result.ID] = task
	s.ejh.wg.Add(1)
	go func() {
		defer s.ejh.wg.Done()
		defer cancel()
		err := s.performEJHSyncTask(ctx, task, 90*time.Second, 90*time.Second)
		s.ejh.syncMu.Lock()
		finished := time.Now()
		task.result.Status = "completed"
		if err != nil {
			task.result.Status, task.result.Error = "failed", err.Error()
		}
		task.result.FinishedAt = &finished
		result := task.result
		close(task.done)
		s.ejh.syncMu.Unlock()
		slog.Info("EJH sync finished", "task_id", result.ID, "status", result.Status, "duration_ms", time.Since(result.QueuedAt).Milliseconds(), "error", result.Error)
	}()
	return task, nil
}

func (s *Server) performEJHSyncTask(ctx context.Context, task *ejhSyncTask, waitLimit, runLimit time.Duration) error {
	wait, cancel := context.WithTimeout(ctx, waitLimit)
	finishRun, err := s.lockBackgroundCard(wait, &s.ejh.syncRun)
	if err != nil {
		cancel()
		return fmt.Errorf("等待其他同步结束超时，本次未开始同步：%w", err)
	}
	defer finishRun()
	unlock, err := s.lockBackgroundCard(wait, &s.ejh.mu)
	cancel()
	if err != nil {
		return fmt.Errorf("等待其他卡台操作结束超时或服务停止，本次未开始同步；已有数据保留：%w", err)
	}
	// Release between pages/cards, never in the middle of a card read/commit.
	// All synchronization tasks remain serialized by syncRun.
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	s.ejh.syncMu.Lock()
	now := time.Now()
	task.result.Status, task.result.StartedAt = "running", &now
	s.ejh.syncMu.Unlock()
	slog.Info("EJH sync started", "task_id", task.result.ID, "wait_ms", now.Sub(task.result.QueuedAt).Milliseconds())
	job, done := context.WithTimeout(ctx, runLimit)
	defer done()
	yield := func() error {
		unlock()
		unlock = nil
		next, err := s.lockBackgroundCard(job, &s.ejh.mu)
		if err != nil {
			return err
		}
		unlock = next
		return nil
	}
	return s.syncEJHWithYield(job, task.start, task.end, yield)
}

func (s *Server) ejhSyncSnapshot(task *ejhSyncTask) ejhSyncResult {
	s.ejh.syncMu.Lock()
	defer s.ejh.syncMu.Unlock()
	return task.result
}

func (s *Server) getEJHSyncTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.ejh.syncMu.Lock()
	task := s.ejh.syncJobs[r.PathValue("id")]
	var result ejhSyncResult
	if task != nil {
		result = task.result
	}
	s.ejh.syncMu.Unlock()
	if task == nil {
		writeAPI(w, 404, nil, "同步任务已过期或服务已重启，请重新刷新；已保存数据保留")
		return
	}
	writeAPI(w, 200, result, "")
}
