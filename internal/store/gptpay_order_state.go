package store

import (
	"encoding/json"

	"chatgpt-space-merge/internal/gptpay"
)

// Status observation needs only the public order profile. Never decrypt the
// checkout/session/card snapshot while checking historical delivery state.
func (s *Store) GPTPayOrderState(id string) (gptpay.Order, error) {
	return s.gptPayOrderState("id=?", id)
}

func (s *Store) ActiveGPTPayOrderState(email string) (gptpay.Order, error) {
	return s.gptPayOrderState("email=? AND active=1", email)
}

func (s *Store) gptPayOrderState(where, arg string) (gptpay.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var order gptpay.Order
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM gptpay_orders WHERE "+where, arg).Scan(&raw); err != nil {
		return order, err
	}
	err := json.Unmarshal([]byte(raw), &order)
	return order, err
}
