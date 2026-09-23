package store

import (
	"database/sql"
	"errors"
)

// Pending Sub2 creates survive a timeout/restart. Their OAuth payload is encrypted
// with the same local master key as the account's credentials.
func (s *Store) Sub2PushAttempt(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var encrypted string
	err := s.db.QueryRow("SELECT encrypted_payload FROM sub2_push_attempts WHERE id=?", id).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.decrypt(encrypted)
}

func (s *Store) SaveSub2PushAttempt(id, payload string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	encrypted, err := s.encrypt(payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO sub2_push_attempts(id,encrypted_payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET encrypted_payload=excluded.encrypted_payload`, id, encrypted)
	return err
}

func (s *Store) DeleteSub2PushAttempt(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM sub2_push_attempts WHERE id=?", id)
	return err
}
