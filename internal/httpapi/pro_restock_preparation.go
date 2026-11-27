package httpapi

import (
	"sync"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

// A failed login to the issuer is not an uncertain card issuance. Only release
// an orphaned preparation after proving there is no payment or issuance intent.
func (s *Server) finishUnsubmittedRestockPreparation(t *restock.Task, p model.MailAccountProfile) (bool, error) {
	if (t.CardProvider != "nexovcc" && t.CardProvider != "ejh") || t.Status == "reserved" || p.ProRestockTaskID != t.ID || p.ProAuto.ID != "" || t.OrderID != "" || t.CardID != "" || p.PushStatus == "completed" || p.DispositionStarted() {
		return false, nil
	}
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	running := s.proRestockStartJobs[t.ID] != nil || s.proAutoJobs[p.Email] != nil
	if running || s.proLoginAlreadyRunning(p.Email) {
		return false, nil
	}
	value, _ := s.proLocks.LoadOrStore("account:"+p.Email, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return false, nil
	}
	defer lock.Unlock()
	finished, err := s.store.FinishUnsubmittedRestockPreparation(t)
	if finished {
		s.enqueueAuditEvent(model.AutoRotationEvent{Email: t.Email, Type: "restock_recovery", Source: "pro_restock", Operation: "card_preparation", ToStatus: t.Status, Message: t.Message, Details: map[string]any{"task_id": t.ID, "card_provider": t.CardProvider, "no_payment_or_issuance": true}})
	}
	return finished, err
}
