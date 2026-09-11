package store

import (
	"strings"
	"time"
)

// OAuthSMSPhone selects and marks a phone atomically. Secrets stay inside the
// server-to-child pipe; the public SMSPhone API continues to mask card codes.
func (s *Store) OAuthSMSPhone(email, provider string, excluded map[string]bool) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	rows, err := s.db.Query(`SELECT p.id,p.provider,p.phone_number,p.card_code,p.api_url,
		EXISTS(SELECT 1 FROM sms_phone_bindings b WHERE b.phone_id=p.id AND b.gpt_email=?) AS own_binding
		FROM sms_phones p
		WHERE p.status='active' AND (p.lease_expires_at='' OR p.lease_expires_at>?)
		AND (?='' OR p.provider=?)
		AND (EXISTS(SELECT 1 FROM sms_phone_bindings b WHERE b.phone_id=p.id AND b.gpt_email=?) OR (
		 (SELECT COUNT(*) FROM sms_phone_bindings b WHERE b.phone_id=p.id)<p.max_bindings
		 AND (p.last_attempt_at='' OR p.last_attempt_at<?)
		 AND NOT EXISTS(SELECT 1 FROM sms_phone_bindings b WHERE b.phone_id=p.id AND b.bound_at>?)))
		ORDER BY own_binding DESC,p.last_attempt_at,p.created_at,p.id`,
		strings.ToLower(email), formatTime(now), provider, provider, strings.ToLower(email),
		formatTime(now.Add(-30*time.Second)), formatTime(now.Add(-5*time.Minute)))
	if err != nil {
		return nil, err
	}
	var selected map[string]any
	for rows.Next() {
		var id, prov, phone, card, apiURL string
		var bound int
		if err = rows.Scan(&id, &prov, &phone, &card, &apiURL, &bound); err != nil {
			break
		}
		if excluded[id] {
			continue
		}
		selected = map[string]any{"id": id, "provider": prov, "phone_number": phone, "card_code": card, "api_url": apiURL, "own_binding": bound == 1}
		break
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || selected == nil {
		return nil, err
	}
	_, err = s.db.Exec("UPDATE sms_phones SET last_attempt_at=? WHERE id=?", formatTime(now), selected["id"])
	return selected, err
}

func (s *Store) RejectOAuthSMSPhone(id, reason string, disable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE sms_phones SET last_error=?,last_attempt_at=?,updated_at=?,
		status=CASE WHEN ? THEN 'disabled' ELSE status END WHERE id=?`,
		reason, formatTime(time.Now()), formatTime(time.Now()), disable, id)
	return err
}

func (s *Store) OAuthSMSBound(id, email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM sms_phone_bindings WHERE phone_id=? AND gpt_email=?", id, strings.ToLower(strings.TrimSpace(email))).Scan(&count)
	return count > 0
}
