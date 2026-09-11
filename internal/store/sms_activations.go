package store

import (
	"encoding/json"
	"strings"
	"time"

	"chatgpt-space-merge/internal/herosms"
)

func (s *Store) SMSAccountID(email string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	_ = s.db.QueryRow(`SELECT id FROM free_accounts WHERE lower(json_extract(profile,'$.email'))=? LIMIT 1`, strings.ToLower(strings.TrimSpace(email))).Scan(&id)
	return id
}

func (s *Store) HasActiveSMSActivation(email string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sms_activations WHERE email=? AND state IN ('active','cancel_pending','finish_pending')`, email).Scan(&count)
	return count > 0, err
}

type SMSActivation struct {
	ID       string
	Email    string
	State    string
	Attempts int
	Config   herosms.Config
}

func (s *Store) SaveSMSActivation(id, email string, cfg herosms.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	sealed, err := s.encrypt(string(raw))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO sms_activations(id,email,state,encrypted_config,next_attempt_at) VALUES(?,?,'active',?,0)
		ON CONFLICT(id) DO UPDATE SET email=excluded.email,state='active',encrypted_config=excluded.encrypted_config,attempts=0,last_error=''`, id, email, sealed)
	return err
}

func (s *Store) QueueSMSActivation(id string, finish bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := "cancel_pending"
	if finish {
		state = "finish_pending"
	}
	_, err := s.db.Exec(`UPDATE sms_activations SET state=?,next_attempt_at=0 WHERE id=? AND state='active'`, state, id)
	return err
}

func (s *Store) RecoverSMSActivations() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE sms_activations SET state='cancel_pending',next_attempt_at=0 WHERE state='active'`)
	return err
}

func (s *Store) DueSMSActivations(now time.Time) ([]SMSActivation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,email,state,attempts,encrypted_config FROM sms_activations WHERE state IN ('cancel_pending','finish_pending') AND next_attempt_at<=? ORDER BY next_attempt_at LIMIT 50`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SMSActivation{}
	for rows.Next() {
		var item SMSActivation
		var encrypted string
		if err = rows.Scan(&item.ID, &item.Email, &item.State, &item.Attempts, &encrypted); err != nil {
			return nil, err
		}
		plain, e := s.decrypt(encrypted)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(plain), &item.Config); e != nil {
			return nil, e
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateSMSActivation(id, state, lastError string, attempts int, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Terminal history lives in the existing audit log, not in the recovery queue.
	if state == "cancelled" || state == "finished" || state == "replaced" {
		_, err := s.db.Exec(`DELETE FROM sms_activations WHERE id=?`, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE sms_activations SET state=?,last_error=?,attempts=?,next_attempt_at=? WHERE id=?`, state, lastError, attempts, next.Unix(), id)
	return err
}
