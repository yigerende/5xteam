package httpapi

import (
	"errors"
	"net/http"
	"sync"
)

type proDeleteResult struct {
	Email   string `json:"email"`
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) deleteProAccount(email string) error {
	// Share the login/automation startup gate and never wait behind an active
	// account operation. The store rechecks durable state under its own lock.
	s.proAutoMu.Lock()
	defer s.proAutoMu.Unlock()
	if s.proAutoJobs[email] != nil || s.proLoginAlreadyRunning(email) {
		return errors.New("账号正在登录或执行全自动任务，请完成或停止后再删除")
	}
	v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return errors.New("账号正在执行操作，请稍后再删除")
	}
	defer lock.Unlock()
	return s.store.DeleteProAccount(email)
}

func (s *Server) deleteProAccounts(w http.ResponseWriter, r *http.Request) {
	emails, ok := decodeProEmails(w, r)
	if !ok {
		return
	}
	items := make([]proDeleteResult, 0, len(emails))
	succeeded := 0
	for _, email := range emails {
		item := proDeleteResult{Email: email}
		err := r.Context().Err()
		if err == nil {
			err = s.deleteProAccount(email)
		}
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Deleted = true
			succeeded++
		}
		items = append(items, item)
	}
	writeAPI(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "succeeded": succeeded, "failed": len(items) - succeeded}, "")
}
