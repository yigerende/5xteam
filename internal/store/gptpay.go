package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

func (s *Store) GPTPaySettings() (gptpay.Settings, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := gptpay.NormalizeSettings(gptpay.Settings{})
	var raw, sealed string
	err := s.db.QueryRow("SELECT profile,encrypted_key FROM gptpay_settings WHERE id=1").Scan(&raw, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return v, "", nil
	}
	if err != nil {
		return v, "", err
	}
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return v, "", err
	}
	v, err = gptpay.NormalizeSettings(v)
	if err != nil {
		return v, "", err
	}
	key, err := s.decryptOptional(sealed)
	v.KeyPresent = key != ""
	return v, key, err
}
func (s *Store) SaveGPTPaySettings(v gptpay.Settings, key string) (gptpay.Settings, error) {
	var err error
	v, err = gptpay.NormalizeSettings(v)
	if err != nil {
		return v, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sealed string
	err = s.db.QueryRow("SELECT encrypted_key FROM gptpay_settings WHERE id=1").Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if strings.TrimSpace(key) != "" {
		sealed, err = s.encrypt(strings.TrimSpace(key))
		if err != nil {
			return v, err
		}
	}
	v.KeyPresent = sealed != ""
	raw, _ := json.Marshal(v)
	_, err = s.db.Exec("INSERT INTO gptpay_settings(id,profile,encrypted_key) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_key=excluded.encrypted_key", string(raw), sealed)
	return v, err
}
func (s *Store) GPTPayCard(id string) (gptpay.Card, gptpay.CardSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v gptpay.Card
	var secret gptpay.CardSecret
	var raw, sealed string
	if err := s.db.QueryRow("SELECT profile,encrypted_secret FROM gptpay_cards WHERE id=?", id).Scan(&raw, &sealed); err != nil {
		return v, secret, fmt.Errorf("银行卡不存在")
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, secret, err
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return v, secret, err
	}
	err = json.Unmarshal([]byte(plain), &secret)
	if err == nil {
		err = s.decorateProCardLocked(&v, secret)
	}
	return v, secret, err
}
func (s *Store) SaveGPTPayCard(v gptpay.Card, secret gptpay.CardSecret) (gptpay.Card, error) {
	// Defensive validation also applies to callers outside the HTTP handlers.
	if len(secret.Number) < 12 || len(secret.Number) > 19 {
		return v, fmt.Errorf("银行卡号长度无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.ID == "" {
		return v, fmt.Errorf("银行卡 ID 不能为空")
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		v.Name = "银行卡 " + secret.Number[len(secret.Number)-4:]
	}
	if len(v.Name) > 120 {
		return v, fmt.Errorf("请填写银行卡名称（不超过 120 字节）")
	}
	v.Last4 = secret.Number[len(secret.Number)-4:]
	v.ExpMonth = secret.Month
	v.ExpYear = secret.Year
	v.UpdatedAt = time.Now()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = v.UpdatedAt
	}
	raw, _ := json.Marshal(secret)
	sealed, err := s.encrypt(string(raw))
	if err != nil {
		return v, err
	}
	raw, _ = json.Marshal(v)
	_, err = s.db.Exec("INSERT INTO gptpay_cards(id,profile,encrypted_secret,enabled,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_secret=excluded.encrypted_secret,enabled=excluded.enabled", v.ID, string(raw), sealed, v.Enabled, formatTime(v.CreatedAt))
	return v, err
}
func (s *Store) DeleteGPTPayCard(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("DELETE FROM gptpay_cards WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("银行卡不存在")
	}
	return nil
}
func (s *Store) GPTPayCards(limit, offset int, enabledOnly bool) ([]gptpay.Card, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT profile,encrypted_secret FROM gptpay_cards ORDER BY created_at DESC,id DESC")
	if err != nil {
		return nil, 0, err
	}
	type row struct {
		card   gptpay.Card
		secret gptpay.CardSecret
	}
	loaded := []row{}
	for rows.Next() {
		var raw, sealed string
		var v row
		if err = rows.Scan(&raw, &sealed); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &v.card); err != nil {
			break
		}
		var plain string
		plain, err = s.decrypt(sealed)
		if err != nil {
			break
		}
		if err = json.Unmarshal([]byte(plain), &v.secret); err != nil {
			break
		}
		loaded = append(loaded, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	items := []gptpay.Card{}
	for _, v := range loaded {
		if err = s.decorateProCardLocked(&v.card, v.secret); err != nil {
			return nil, 0, err
		}
		if !enabledOnly || v.card.Available {
			items = append(items, v.card)
		}
	}
	total := len(items)
	offset = min(max(0, offset), total)
	return items[offset:min(total, offset+max(0, limit))], total, nil
}
func (s *Store) GPTPayOrder(id string) (gptpay.Order, gptpay.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gptPayOrderLocked("id=?", id)
}
func (s *Store) ActiveGPTPayOrder(email string) (gptpay.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _, err := s.gptPayOrderLocked("email=? AND active=1", email)
	return v, err
}
func (s *Store) gptPayOrderLocked(where, arg string) (gptpay.Order, gptpay.Snapshot, error) {
	var v gptpay.Order
	var snapshot gptpay.Snapshot
	var raw, sealed string
	if err := s.db.QueryRow("SELECT profile,encrypted_snapshot FROM gptpay_orders WHERE "+where, arg).Scan(&raw, &sealed); err != nil {
		return v, snapshot, err
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, snapshot, err
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return v, snapshot, err
	}
	err = json.Unmarshal([]byte(plain), &snapshot)
	return v, snapshot, err
}
func (s *Store) CreateGPTPayOrder(v gptpay.Order, snapshot gptpay.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := proCardKey(snapshot.Input.Number)
	limit, err := proCardLimit(tx)
	if err != nil {
		return err
	}
	opened, pending, err := proCardCounts(tx, key, v.Email)
	if err != nil {
		return err
	}
	if opened+pending >= limit {
		return fmt.Errorf("银行卡已达开通上限 %d（含在途占位）", limit)
	}
	raw, _ := json.Marshal(snapshot)
	sealed, err := s.encrypt(string(raw))
	if err != nil {
		return err
	}
	raw, _ = json.Marshal(v)
	_, err = tx.Exec("INSERT INTO gptpay_orders(id,email,profile,encrypted_snapshot,active,created_at) VALUES(?,?,?,?,?,?)", v.ID, v.Email, string(raw), sealed, v.Active(), formatTime(v.CreatedAt))
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO pro_card_usage VALUES(?,?,?,?)`, v.ID, key, v.Email, v.Status); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM pro_card_reservations WHERE email=?`, v.Email); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) UpdateGPTPayOrder(v gptpay.Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.UpdatedAt = time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, _ := json.Marshal(v)
	_, err = tx.Exec("UPDATE gptpay_orders SET profile=?,active=? WHERE id=?", string(raw), v.Active(), v.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE pro_card_usage SET status=? WHERE order_id=?`, v.Status, v.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) GPTPayOrders(limit, offset int, email string) ([]gptpay.Order, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	args := []any{}
	if email != "" {
		where = " WHERE email=?"
		args = append(args, strings.ToLower(strings.TrimSpace(email)))
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM gptpay_orders"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.db.Query("SELECT profile FROM gptpay_orders"+where+" ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []gptpay.Order{}
	for rows.Next() {
		var raw string
		var v gptpay.Order
		if err = rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, 0, err
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}

// Only bounded, due orders with a supplier ID; never resubmit an uncertain purchase.
func (s *Store) GPTPayOrdersDue(now time.Time, limit int) ([]gptpay.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit = min(20, max(1, limit))
	rows, err := s.db.Query(`SELECT o.profile FROM gptpay_orders o
		WHERE COALESCE(json_extract(o.profile,'$.remote.id'),'')<>''
		AND (o.active=1 OR json_extract(o.profile,'$.remote.cancellationStatus') IN ('waiting','pending'))
		AND COALESCE(json_extract(o.profile,'$.next_status_check_at'),'')<=?
		AND NOT EXISTS (SELECT 1 FROM mail_accounts m WHERE m.email=o.email AND json_extract(m.profile,'$.pro_auto.status')='running')
		ORDER BY COALESCE(json_extract(o.profile,'$.next_status_check_at'),''),o.created_at LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []gptpay.Order
	for rows.Next() {
		var raw string
		var o gptpay.Order
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}
