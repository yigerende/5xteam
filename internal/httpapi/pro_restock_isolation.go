package httpapi

import (
	"sync"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func (s *Server) saveRestockObservation(t *restock.Task) error {
	if err := s.store.SaveProRestockObservation(t); err != nil {
		return err
	}
	return s.isolateRestockFailure(t)
}

func (s *Server) isolateRestockFailure(t *restock.Task) error {
	if t.Status != "attention" && t.Status != "unknown" {
		return nil
	}
	// A preparation that provably never issued a card or submitted payment
	// keeps its existing safe-failure path and releases the unused budget.
	if profile, _, err := s.store.MailAccountCredential(t.Email); err == nil {
		if finished, err := s.finishUnsubmittedRestockPreparation(t, profile); err != nil || finished {
			return err
		}
	}
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	s.proMergeMu.Lock()
	defer s.proMergeMu.Unlock()
	if s.proAutoJobs[t.Email] != nil || s.proMergeJobs[t.Email] != nil || s.proRestockStartJobs[t.ID] != nil || s.proLoginAlreadyRunning(t.Email) {
		return nil
	}
	v, _ := s.proLocks.LoadOrStore("account:"+t.Email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	isolated, err := s.store.IsolateProRestockTask(t)
	if isolated {
		s.enqueueAuditEvent(model.AutoRotationEvent{Email: t.Email, Type: "restock_recovery", Source: "pro_restock", Operation: "isolate_failure", ToStatus: t.Status, Message: t.Message, Details: map[string]any{"task_id": t.ID, "order_id": t.OrderID, "auto_isolated": true}})
	}
	return err
}
