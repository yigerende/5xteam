package httpapi

import (
	"fmt"
	"sort"
	"sync"
)

// Share the same per-account lock as payment submission, status polling and
// manual reconciliation. Never wait behind a live payment to delete its record.
func (s *Server) lockCaseDeletion(emails []string) (func(), error) {
	sort.Strings(emails)
	locks := []*sync.Mutex{}
	unlock := func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
	previous := ""
	for _, email := range emails {
		if email == previous {
			continue
		}
		previous = email
		v, _ := s.proLocks.LoadOrStore("account:"+email, &sync.Mutex{})
		lock := v.(*sync.Mutex)
		if !lock.TryLock() {
			unlock()
			return nil, fmt.Errorf("账号 %s 正在执行或查询，本次未删除，请稍后重试", email)
		}
		locks = append(locks, lock)
	}
	return unlock, nil
}
