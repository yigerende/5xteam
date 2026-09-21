package store

import (
	"strings"
)

type ProActivationCard struct {
	CardName  string `json:"card_name"`
	CardLast4 string `json:"card_last4"`
}

// Query only the listed emails, using one indexed lookup per account inside
// each batch. Read the successful order snapshot, never mutable card details.
func (s *Store) ProActivationCards(emails []string) (map[string]ProActivationCard, error) {
	result := make(map[string]ProActivationCard)
	if len(emails) == 0 {
		return result, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for start := 0; start < len(emails); start += 400 {
		end := min(start+400, len(emails))
		values := make([]string, 0, end-start)
		args := make([]any, 0, end-start)
		for _, email := range emails[start:end] {
			values = append(values, "(?)")
			args = append(args, strings.ToLower(strings.TrimSpace(email)))
		}
		rows, err := s.db.Query(`WITH targets(email) AS (VALUES `+strings.Join(values, ",")+`)
			SELECT targets.email, COALESCE(json_extract(o.profile,'$.card_name'),''), COALESCE(json_extract(o.profile,'$.card_last4'),'')
			FROM targets JOIN gptpay_orders o ON o.id=(
				SELECT id FROM gptpay_orders WHERE email=targets.email AND json_extract(profile,'$.status')='success'
				ORDER BY created_at DESC,id DESC LIMIT 1)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var email string
			var card ProActivationCard
			if err = rows.Scan(&email, &card.CardName, &card.CardLast4); err != nil {
				rows.Close()
				return nil, err
			}
			result[email] = card
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
