package store

import "chatgpt-space-merge/internal/gptpay"

// Request-local display projection only. Reservation/admission still uses the
// serialized per-card checks. Reuse the exact occupied-order SQL predicate.
type ejhCardDisplay struct {
	custom, limit, opened, pending int
	dead, single                   bool
	occupants                      []gptpay.CardOccupant
}

func (s *Store) ejhCardDisplayStats() (map[string]*ejhCardDisplay, error) {
	rows, err := s.db.Query(`SELECT t.card_key,COALESCE(l.max_accounts,0),
	MAX(1,COALESCE((SELECT json_extract(profile,'$.default_max_accounts') FROM ejh_settings WHERE id=1),3)),
	COALESCE((SELECT json_extract(profile,'$.single_active_account') FROM ejh_settings WHERE id=1),1),
	EXISTS(SELECT 1 FROM pro_dead_cards d WHERE d.card_key=t.card_key),
	(SELECT count(DISTINCT lower(email)) FROM pro_card_usage WHERE card_key=t.card_key AND status='success' AND email<>''),
	(SELECT count(*) FROM (
	 SELECT lower(email) email FROM pro_card_usage WHERE card_key=t.card_key AND status NOT IN ('failed','success')
	 UNION SELECT lower(email) FROM pro_card_reservations WHERE card_key=t.card_key
	) p WHERE email<>'' AND NOT EXISTS(SELECT 1 FROM pro_card_usage u WHERE u.card_key=t.card_key AND u.email=p.email AND u.status='success'))
	FROM pro_card_types t LEFT JOIN pro_card_limits l ON l.card_key=t.card_key WHERE t.card_type='ejh'`)
	if err != nil {
		return nil, err
	}
	out := map[string]*ejhCardDisplay{}
	for rows.Next() {
		var key string
		v := &ejhCardDisplay{occupants: []gptpay.CardOccupant{}}
		if err = rows.Scan(&key, &v.custom, &v.limit, &v.single, &v.dead, &v.opened, &v.pending); err != nil {
			break
		}
		if v.custom > 0 {
			v.limit = v.custom
		}
		out[key] = v
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT a.card_key,a.email,a.order_id,a.status,m.email IS NULL FROM (
	SELECT card_key,lower(email) email,'' order_id,'reserved' status FROM pro_card_reservations
	UNION ALL SELECT u.card_key,lower(u.email),u.order_id,u.status FROM pro_card_usage u WHERE ` + ejhOrderOccupiedSQL + `
	) a JOIN pro_card_types t ON t.card_key=a.card_key AND t.card_type='ejh'
	LEFT JOIN mail_accounts m ON m.email=a.email ORDER BY a.email,a.order_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var item gptpay.CardOccupant
		if err = rows.Scan(&key, &item.Email, &item.OrderID, &item.Status, &item.Deleted); err != nil {
			return nil, err
		}
		out[key].occupants = append(out[key].occupants, item)
	}
	return out, rows.Err()
}

// Batch classification avoids one SQL query per issuer record. The result is
// request-local and never returned by the HTTP API.
func (s *Store) EJHCardNumbers(numbers []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT card_key FROM pro_card_types WHERE card_type='ejh'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, n := range numbers {
		out[n] = keys[proCardKey(n)]
	}
	return out, nil
}
