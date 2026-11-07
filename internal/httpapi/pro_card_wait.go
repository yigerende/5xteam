package httpapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Returned only before this attempt issues or pays. It keeps the same account
// queued; unknown financial operations continue to require reconciliation.
type proCardPreparationWait struct {
	message string
	busy    bool
}

// A retry of a rejected admission is bound to the original account progress.
// Stopping or advancing the account invalidates it before any issuer request.
type proCardPreparationRetry struct {
	FlowID        string `json:"flow_id"`
	OrderID       string `json:"order_id"`
	Status        string `json:"status"`
	StopVersion   uint64 `json:"stop_version"`
	StopRequested bool   `json:"stop_requested"`
}

func cardPreparationProgress(state model.ProAutoState) proCardPreparationRetry {
	return proCardPreparationRetry{state.ID, state.OrderID, state.Status, state.StopVersion, state.StopRequested}
}

func (e *proCardPreparationWait) Error() string { return e.message }
func cardPreparationWait(format string, args ...any) error {
	return &proCardPreparationWait{message: fmt.Sprintf(format, args...)}
}

// Manual admission takes precedence over new maintenance and scheduled card
// work. Keep a short lease across the UI's 3-second retry gap; abandoned tabs
// cannot indefinitely starve maintenance. Already submitted work finishes.
func (s *Server) beginManualCardPreparation() func(bool) {
	s.proCardPriorityActive.Add(1)
	var finished sync.Once
	return func(busy bool) {
		finished.Do(func() {
			if busy {
				until := time.Now().Add(10 * time.Second).UnixNano()
				for old := s.proCardPriorityUntil.Load(); old < until; old = s.proCardPriorityUntil.Load() {
					if s.proCardPriorityUntil.CompareAndSwap(old, until) {
						break
					}
				}
			}
			s.proCardPriorityActive.Add(-1)
		})
	}
}

func (s *Server) manualCardPreparationPending() bool {
	return s.proCardPriorityActive.Load() > 0 || time.Now().UnixNano() < s.proCardPriorityUntil.Load()
}

func (s *Server) tryBackgroundCardLock(lock *sync.Mutex) bool {
	if s.manualCardPreparationPending() || !lock.TryLock() {
		return false
	}
	if s.manualCardPreparationPending() {
		lock.Unlock()
		return false
	}
	return true
}

func (s *Server) lockBackgroundCard(ctx context.Context, lock *sync.Mutex) (func(), error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.tryBackgroundCardLock(lock) {
			return lock.Unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Bound admission waits without weakening serialization or spawning a waiter
// which could later acquire an abandoned mutex.
func lockProCardPreparation(ctx context.Context, lock *sync.Mutex, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lock.TryLock() {
		return nil
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return &proCardPreparationWait{message: message, busy: true}
		case <-ticker.C:
			if lock.TryLock() {
				return nil
			}
		}
	}
}
