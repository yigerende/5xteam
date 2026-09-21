package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func proCardKey(number string) string {
	h := sha256.Sum256([]byte(number))
	return hex.EncodeToString(h[:])
}

func (s *Store) initializeProScheduler() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS pro_card_usage(order_id TEXT PRIMARY KEY, card_key TEXT NOT NULL, email TEXT NOT NULL COLLATE NOCASE, status TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS pro_card_usage_card ON pro_card_usage(card_key,status,email);
	CREATE INDEX IF NOT EXISTS pro_card_usage_email ON pro_card_usage(email,status);
	CREATE INDEX IF NOT EXISTS pro_schedule_accounts ON mail_accounts(coalesce(json_extract(profile,'$.space_merged_once'),0),json_extract(profile,'$.created_at'),email) WHERE json_extract(profile,'$.management_scope')='pro';
	CREATE TABLE IF NOT EXISTS pro_card_reservations(email TEXT PRIMARY KEY COLLATE NOCASE,card_key TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS pro_schedule_runs(id TEXT PRIMARY KEY,status TEXT NOT NULL,started_at TEXT NOT NULL,payload TEXT NOT NULL);
	CREATE UNIQUE INDEX IF NOT EXISTS pro_schedule_active ON pro_schedule_runs(status) WHERE status='running';
	CREATE TABLE IF NOT EXISTS pro_schedule_clock(id INTEGER PRIMARY KEY CHECK(id=1),next_at TEXT NOT NULL);
	`)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT o.id,o.email,o.encrypted_snapshot,json_extract(o.profile,'$.status') FROM gptpay_orders o LEFT JOIN pro_card_usage u ON u.order_id=o.id WHERE u.order_id IS NULL`)
	if err != nil {
		return err
	}
	type record struct{ id, email, key, status string }
	updates := []record{}
	for rows.Next() {
		var r record
		var sealed string
		if err = rows.Scan(&r.id, &r.email, &sealed, &r.status); err != nil {
			break
		}
		var plain string
		plain, err = s.decrypt(sealed)
		if err != nil {
			break
		}
		var snap gptpay.Snapshot
		if err = json.Unmarshal([]byte(plain), &snap); err != nil {
			break
		}
		r.key = proCardKey(snap.Input.Number)
		updates = append(updates, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range updates {
		if _, err = s.db.Exec(`INSERT INTO pro_card_usage VALUES(?,?,?,?)`, r.id, r.key, r.email, r.status); err != nil {
			return err
		}
	}
	return nil
}

type proCapacityQuery interface{ QueryRow(string, ...any) *sql.Row }

func proCardCounts(q proCapacityQuery, key, exceptEmail string) (opened, pending int, err error) {
	err = q.QueryRow(`SELECT count(*) FROM (SELECT DISTINCT lower(email) FROM pro_card_usage WHERE card_key=? AND status='success' AND email<>? COLLATE NOCASE)`, key, exceptEmail).Scan(&opened)
	if err != nil {
		return
	}
	err = q.QueryRow(`SELECT count(*) FROM (SELECT lower(email) email FROM pro_card_usage WHERE card_key=? AND status NOT IN ('failed','success') UNION SELECT lower(email) FROM pro_card_reservations WHERE card_key=?) p WHERE email<>? COLLATE NOCASE AND NOT EXISTS(SELECT 1 FROM pro_card_usage u WHERE u.card_key=? AND u.email=p.email AND u.status='success')`, key, key, exceptEmail, key).Scan(&pending)
	return
}
func proCardLimit(q proCapacityQuery) (int, error) {
	var limit int
	err := q.QueryRow(`SELECT coalesce(json_extract(profile,'$.card_account_limit'),3) FROM gptpay_settings WHERE id=1`).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 3, nil
	}
	if limit < 1 {
		limit = 3
	}
	return limit, err
}
func (s *Store) decorateProCardLocked(card *gptpay.Card, secret gptpay.CardSecret) error {
	limit, err := proCardLimit(s.db)
	if err != nil {
		return err
	}
	opened, pending, err := proCardCounts(s.db, proCardKey(secret.Number), "")
	if err != nil {
		return err
	}
	card.AccountLimit, card.OpenedAccounts, card.PendingAccounts = limit, opened, pending
	card.RemainingAccounts = max(0, limit-opened-pending)
	card.Available = card.Enabled && card.RemainingAccounts > 0 && gptpay.ValidateCard(secret, time.Now()) == nil
	return nil
}

// Both manual purchases and automation reservations use the same serialized
// database check; a UI selection never constitutes a capacity reservation.
func (s *Store) ReserveProCard(email, cardID string, enforceMaximum bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, sealed string
	if err = tx.QueryRow(`SELECT profile,encrypted_secret FROM gptpay_cards WHERE id=?`, cardID).Scan(&raw, &sealed); err != nil {
		return errors.New("银行卡不存在")
	}
	var card gptpay.Card
	var secret gptpay.CardSecret
	if err = json.Unmarshal([]byte(raw), &card); err != nil {
		return err
	}
	plain, err := s.decrypt(sealed)
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(plain), &secret); err != nil {
		return err
	}
	if !card.Enabled {
		return errors.New("银行卡已禁用")
	}
	if err = gptpay.ValidateCard(secret, time.Now()); err != nil {
		return err
	}
	key := proCardKey(secret.Number)
	limit, err := proCardLimit(tx)
	if err != nil {
		return err
	}
	opened, pending, err := proCardCounts(tx, key, email)
	if err != nil {
		return err
	}
	if opened+pending >= limit {
		return fmt.Errorf("银行卡已达开通上限 %d（含在途占位）", limit)
	}
	if enforceMaximum {
		var maximum int
		if err = tx.QueryRow(`SELECT coalesce(json_extract(profile,'$.max_unmerged'),5) FROM pro_settings WHERE id=1`).Scan(&maximum); err != nil {
			return err
		}
		opened, pending, err = proPopulation(tx)
		if err != nil {
			return err
		}
		if opened+pending >= maximum {
			return errors.New("已开通和在途账号已达到最大未合并数")
		}
	}
	if _, err = tx.Exec(`INSERT INTO pro_card_reservations(email,card_key) VALUES(?,?)`, email, key); err != nil {
		return errors.New("该账号已有开通占位")
	}
	return tx.Commit()
}
func (s *Store) ReleaseProCard(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM pro_card_reservations WHERE email=?`, email)
	return err
}

const proOpenedCondition = `(EXISTS(SELECT 1 FROM pro_card_usage u WHERE u.email=m.email AND u.status='success') OR coalesce(json_extract(m.profile,'$.pro_auto.steps.recharge'),'')='completed' OR coalesce(json_extract(m.profile,'$.pro_manual_stages.recharge.status'),'')='completed' OR lower(coalesce(json_extract(m.profile,'$.subscription_plan'),'')) LIKE '%pro%' OR lower(coalesce(json_extract(m.profile,'$.current_plan_type'),'')) LIKE '%pro%')`

func proPopulation(q proCapacityQuery) (opened, pending int, err error) {
	err = q.QueryRow(`SELECT count(*) FROM mail_accounts m WHERE json_extract(m.profile,'$.management_scope')='pro' AND coalesce(json_extract(m.profile,'$.space_merged_once'),0)=0 AND ` + proOpenedCondition).Scan(&opened)
	if err != nil {
		return
	}
	err = q.QueryRow(`SELECT count(*) FROM (SELECT lower(email) email FROM pro_card_reservations UNION SELECT lower(email) FROM pro_card_usage WHERE status NOT IN ('success','failed')) a WHERE NOT EXISTS(SELECT 1 FROM mail_accounts m WHERE m.email=a.email AND ` + proOpenedCondition + `)`).Scan(&pending)
	return
}
func (s *Store) ProCapacity() (model.ProScheduleStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := model.ProScheduleStatus{ServerTime: time.Now()}
	var err error
	v.OpenedUnmerged, v.Pending, err = proPopulation(s.db)
	return v, err
}
