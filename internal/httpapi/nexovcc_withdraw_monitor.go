package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"chatgpt-space-merge/internal/nexovcc"
)

// Independent of sync_state: opening a page, a partial finance failure or a
// restart must not reset the withdrawal clock. Money operations still use the
// same issuer lock, request pacing and durable per-cycle admission checks.
type nexovccWithdrawState struct {
	LastAttempt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccess time.Time `json:"last_success_at,omitempty"`
	RetryAfter  time.Time `json:"retry_after,omitempty"`
	NextCheck   time.Time `json:"next_check_at,omitempty"`
	Failures    int       `json:"failures"`
	Blocked     int       `json:"blocked"`
	Error       string    `json:"error,omitempty"`
}

func nexovccWithdrawAttentionCount(ops []nexovcc.Operation) int {
	count := 0
	for _, op := range ops {
		if op.AutoWithdraw && op.Status == "unknown" {
			count++
		}
	}
	return count
}

func (s *Server) nexovccWithdrawState(cfg nexovcc.Settings, snapshot ...[]nexovcc.Operation) (nexovccWithdrawState, error) {
	var state nexovccWithdrawState
	if err := s.store.NexovccCached("withdraw_state", &state); err != nil {
		return state, err
	}
	// A resolved operation must disappear immediately, without waiting for the
	// next withdrawal sweep. Definite historical failures are not unknown results.
	var ops []nexovcc.Operation
	if len(snapshot) > 0 {
		ops = snapshot[0]
	} else {
		var err error
		if ops, err = s.store.NexovccOperations(); err != nil {
			return state, err
		}
	}
	state.Blocked = nexovccWithdrawAttentionCount(ops)
	seconds := cfg.AutoWithdrawIntervalSeconds
	if seconds == 0 {
		seconds = 60
	}
	state.RetryAfter = time.Time{}
	if state.Error == context.Canceled.Error() {
		state.Error = ""
	}
	last := state.LastSuccess
	if state.LastAttempt.After(last) {
		last = state.LastAttempt
	}
	state.NextCheck = last.Add(time.Duration(seconds) * time.Second)
	return state, nil
}

func (s *Server) startNexovccWithdrawMonitor(ctx context.Context) {
	s.nexovcc.wg.Add(1)
	go func() {
		defer s.nexovcc.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.runNexovccWithdrawCheck(ctx); err != nil {
					slog.Warn("环球自动提现检查失败", "error", err)
				}
			}
		}
	}()
}

// A busy issuer does not consume this interval: retry on the next local tick.
// Do not cancel an in-flight supplier request just to run another query.
func (s *Server) runNexovccWithdrawCheck(ctx context.Context) (bool, error) {
	bound, finishOperation, gateErr := s.nexovccOperationContext(ctx)
	if gateErr != nil {
		return false, nil
	}
	defer finishOperation()
	ctx = bound

	if ctx.Err() != nil || !s.tryMaintenanceCardLock(&s.proCardAdmissionMu) {
		return false, nil
	}
	defer s.proCardAdmissionMu.Unlock()
	if !s.nexovcc.mu.TryLock() {
		return false, nil
	}
	defer s.nexovcc.mu.Unlock()
	cfg, _, err := s.store.NexovccSettings()
	if err != nil || !cfg.Enabled || !cfg.AutoWithdrawEnabled {
		return false, err
	}
	state, err := s.nexovccWithdrawState(cfg)
	if err != nil {
		return false, err
	}
	if time.Now().Before(state.NextCheck) {
		return false, nil
	}
	state.LastAttempt = time.Now()
	// Persist the attempt so a restart still respects the configured interval.
	state.RetryAfter = time.Time{}
	if err = s.store.NexovccCache("withdraw_state", state); err != nil {
		return false, err
	}
	s.nexovcc.jobMu.Lock()
	if s.nexovcc.closed {
		s.nexovcc.jobMu.Unlock()
		return false, nil
	}
	s.nexovcc.job = nexovccJob{ID: randomRegistrationID(), Status: "running", Message: "自动检查提现留 1U", Total: 3}
	s.nexovcc.jobMu.Unlock()
	workCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err = s.autoWithdrawNexovccLocked(workCtx)
	cancel()
	state.Blocked = 0
	if ops, e := s.store.NexovccOperations(); e == nil {
		state.Blocked = nexovccWithdrawAttentionCount(ops)
	} else if err == nil {
		err = e
	}
	message, status := "自动提现检查完成", "completed"
	if state.Blocked > 0 {
		message += fmt.Sprintf(" · %d 笔自动提现结果待核实，见操作记录", state.Blocked)
	}
	if err == nil {
		state.LastSuccess = time.Now()
		state.Error, state.Failures, state.RetryAfter = "", 0, time.Time{}
	} else if errors.Is(workCtx.Err(), context.Canceled) && ctx.Err() != nil {
		state.Error = ""
		message, status = "本轮自动提现检查已停止", "completed"
	} else {
		state.Failures++
		state.Error = err.Error()
		message, status = "自动提现待重试："+state.Error, "failed"
	}
	if saveErr := s.store.NexovccCache("withdraw_state", state); saveErr != nil && err == nil {
		err = saveErr
		message, status = "保存提现检查状态失败", "failed"
	}
	s.nexovcc.jobMu.Lock()
	s.nexovcc.job.Status, s.nexovcc.job.Message = status, message
	s.nexovcc.job.Done = s.nexovcc.job.Total
	s.nexovcc.jobMu.Unlock()
	return true, err
}
