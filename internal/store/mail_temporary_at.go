package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"chatgpt-space-merge/internal/model"
)

// Terminal temporary-AT failures follow the explicit local mailbox deletion
// policy, including Pro mailboxes. Preserve payment/audit/workspace history.
// Compare the attempt's snapshot under the write lock so a late failed attempt
// cannot delete a newer login, credential update or re-import of the same email.
func (s *Store) DeleteMailAccountAfterTemporaryATFailure(expected model.MailAccountProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	email := strings.ToLower(strings.TrimSpace(expected.Email))
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // Already deleted; do not recreate it to finish the task.
		}
		return err
	}
	var current model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return err
	}
	if expected.ID == "" || expected.UpdatedAt.IsZero() || current.ID != expected.ID || !current.UpdatedAt.Equal(expected.UpdatedAt) {
		return errors.New("账号已被更新或开始新的操作，旧登录任务不删除新记录")
	}
	return s.deleteMailAccountRecordsLocked(email, false, false)
}
