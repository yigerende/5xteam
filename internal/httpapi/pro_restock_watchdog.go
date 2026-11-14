package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"log/slog"
	"time"
)

func (s *Server) startProRestockWatchdog(ctx context.Context) {
	s.proAutoWG.Add(1)
	go func() {
		defer s.proAutoWG.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.tickProRestockWatchdog(now)
			}
		}
	}()
}

func (s *Server) tickProRestockWatchdog(now time.Time) {
	s.proAutoMu.Lock()
	s.proMergeMu.Lock()
	results, err := s.store.SweepProRestockWatchdog(now)
	if err == nil {
		for _, item := range results {
			if item.CancelEmail != "" {
				for _, cancel := range []context.CancelFunc{s.proAutoJobs[item.CancelEmail], s.proMergeJobs[item.CancelEmail], s.proRestockStartJobs[item.Task.ID]} {
					if cancel != nil {
						cancel()
					}
				}
			}
		}
	}
	s.proMergeMu.Unlock()
	s.proAutoMu.Unlock()
	if err != nil {
		slog.Warn("补货独立超时检查失败", "error", err)
		return
	}
	for _, item := range results {
		s.enqueueAuditEvent(model.AutoRotationEvent{Email: item.Task.Email, Type: "automatic_stop", Source: "pro_restock", Operation: "watchdog_finish", ToStatus: item.Task.Status, Message: item.Task.Message, Details: map[string]any{"task_id": item.Task.ID, "order_id": item.Task.OrderID, "timeout": item.Task.StoppedFailure}})
	}
}
