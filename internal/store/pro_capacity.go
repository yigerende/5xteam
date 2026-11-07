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
	CREATE TABLE IF NOT EXISTS pro_card_limits(card_key TEXT PRIMARY KEY,max_accounts INTEGER NOT NULL CHECK(max_accounts BETWEEN 1 AND 10000));
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
func proCardCustomLimit(q proCapacityQuery, key string) (int, error) {
	var limit int
	err := q.QueryRow(`SELECT max_accounts FROM pro_card_limits WHERE card_key=?`, key).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return limit, err
}

func proCardLimit(q proCapacityQuery, key string) (int, error) {
	custom, err := proCardCustomLimit(q, key)
	if err != nil || custom > 0 {
		return custom, err
	}
	var limit int
	kind, e := proCardType(q, key)
	if e != nil {
		return 0, e
	}
	if kind == "nexovcc" {
		e := q.QueryRow(`SELECT coalesce(json_extract(profile,'$.default_max_accounts'),1) FROM nexovcc_settings WHERE id=1`).Scan(&limit)
		if errors.Is(e, sql.ErrNoRows) {
			return 1, nil
		}
		return max(1, limit), e
	}
	if kind == "ejh" {
		e := q.QueryRow(`SELECT coalesce(json_extract(profile,'$.default_max_accounts'),3) FROM ejh_settings WHERE id=1`).Scan(&limit)
		if errors.Is(e, sql.ErrNoRows) {
			return 3, nil
		}
		return max(1, limit), e
	}
	err = q.QueryRow(`SELECT coalesce(json_extract(profile,'$.card_account_limit'),3) FROM gptpay_settings WHERE id=1`).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 3, nil
	}
	if limit < 1 {
		limit = 3
	}
	return limit, err
}
func (s *Store) decorateProCardLocked(card *gptpay.Card, secret gptpay.CardSecret, display ...*ejhCardDisplay) error {
	var batch *ejhCardDisplay
	if len(display) > 0 {
		batch = display[0]
	}
	key := proCardKey(secret.Number)
	var err error
	if batch != nil {
		card.CardType = "ejh"
	} else {
		card.CardType, err = proCardType(s.db, key)
	}
	if err != nil {
		return err
	}
	issuer, e := s.issuerForPhysicalCard(s.db, key)
	if e != nil {
		return e
	}
	card.IssuerCancelStatus = ""
	card.IssuerFrozen = false
	card.IssuerDisabled = false
	card.NormalPending = false
	if issuer != nil {
		card.Platform, card.TemplateID, card.TemplateName = issuer.Platform, issuer.TemplateID, issuer.TemplateName
		card.IssuerDisabled = issuer.IssuerDisabled
		card.NormalPending = issuer.NormalPending
		card.IssuerCancelStatus = issuer.IssuerCancelStatus
		card.IssuerFrozen = issuer.IssuerFrozen
		card.Source, card.IssuerOrderNo, card.IssuerStatus, card.IssuerLocked, card.IssuerEnd = issuer.Source, issuer.IssuerOrderNo, issuer.IssuerStatus, issuer.IssuerLocked, issuer.IssuerEnd
	}
	var limit, custom, opened, pending int
	if batch != nil {
		limit, custom, opened, pending = batch.limit, batch.custom, batch.opened, batch.pending
	} else {
		limit, err = proCardLimit(s.db, key)
		if err != nil {
			return err
		}
		custom, err = proCardCustomLimit(s.db, key)
		if err != nil {
			return err
		}
		opened, pending, err = proCardCounts(s.db, key, "")
		if err != nil {
			return err
		}
	}
	card.MaxAccounts = nil
	if custom > 0 {
		card.MaxAccounts = &custom
	}
	card.AccountLimit, card.OpenedAccounts, card.PendingAccounts = limit, opened, pending
	card.RemainingAccounts = max(0, limit-opened-pending)
	card.ActiveAccounts, card.ActiveEmail, card.SingleActiveAccount = 0, "", false
	card.Occupants = []gptpay.CardOccupant{}
	if batch != nil {
		card.Occupants = batch.occupants
		card.SingleActiveAccount = batch.single
		seen := map[string]bool{}
		for _, o := range card.Occupants {
			if o.Email != "" {
				seen[o.Email] = true
				if card.ActiveEmail == "" || o.Email < card.ActiveEmail {
					card.ActiveEmail = o.Email
				}
			}
		}
		card.ActiveAccounts = len(seen)
	} else if card.CardType == "ejh" {
		card.Occupants, err = ejhCardOccupants(s.db, key)
		if err != nil {
			return err
		}
		card.ActiveAccounts, card.ActiveEmail, err = ejhCardOccupancy(s.db, key, "")
		if err != nil {
			return err
		}
		card.SingleActiveAccount, err = ejhSingleActive(s.db, key)
		if err != nil {
			return err
		}
	}
	if card.CardType == "nexovcc" {
		card.Occupants, err = nexovccOccupants(s.db, key)
		if err != nil {
			return err
		}
		card.ActiveAccounts, card.ActiveEmail, err = nexovccOccupancy(s.db, key, "")
		if err != nil {
			return err
		}
		card.SingleActiveAccount, err = nexovccSingleActive(s.db)
		if err != nil {
			return err
		}
	}
	if batch != nil {
		card.DeadAccount = batch.dead
	} else {
		card.DeadAccount, err = proCardDead(s.db, key)
	}
	if err != nil {
		return err
	}
	card.Available = !card.DeadAccount && card.Enabled && card.RemainingAccounts > 0 && gptpay.ValidateCard(secret, time.Now()) == nil && issuerCardUsable(*card)
	if card.SingleActiveAccount && card.ActiveAccounts > 0 {
		card.Available = false
	}
	return nil
}

// Both manual purchases and automation reservations use the same serialized
// database check; a UI selection never constitutes a capacity reservation.
func (s *Store) ReserveProCard(email, cardID string, enforceMaximum bool, restockIDs ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockProCardAdmission(tx); err != nil {
		return err
	}
	var owner string
	if err = tx.QueryRow(`SELECT coalesce(json_extract(profile,'$.pro_restock_task_id'),'') FROM mail_accounts WHERE email=?`, email).Scan(&owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if owner != "" && (len(restockIDs) == 0 || restockIDs[0] != owner) {
		return errors.New("账号已由 Pro 自动补货领取")
	}
	var raw, sealed string
	if err = tx.QueryRow(`SELECT profile,encrypted_secret FROM gptpay_cards WHERE id=?`, cardID).Scan(&raw, &sealed); err != nil {
		return errors.New("银行卡不存在")
	}
	var card gptpay.Card
	var secret gptpay.CardSecret
	if err = json.Unmarshal([]byte(raw), &card); err != nil {
		return err
	}
	if owner != "" {
		task, e := restockDispatchTask(tx, owner)
		if e != nil {
			return e
		}
		if task.PurchaseRoute != nil {
			matches, e := s.proTemplateCards(tx, task.CardProvider, task.PurchaseRoute.TemplateID, task.PurchaseRoute.PlanCode)
			if e != nil {
				return e
			}
			if !matches[card.ID] {
				return errors.New("银行卡不属于本次套餐的开通模板")
			}
		}
		if !RestockCardMatches(card, task.CardProvider, task.StrictCardProvider) {
			return errors.New("银行卡不属于本次补货指定卡台")
		}
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
	if !issuerCardUsable(card) {
		return errors.New("卡台卡片未激活、冻结、过期或状态不可用")
	}
	if err = gptpay.ValidateCard(secret, time.Now()); err != nil {
		return err
	}
	key := proCardKey(secret.Number)
	if err = s.validateIssuerCard(tx, key); err != nil {
		return err
	}
	if err = validateEJHOccupancy(tx, key, email); err != nil {
		return err
	}
	limit, err := proCardLimit(tx, key)
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
	// Only release the login/pre-payment reservation. Order creation atomically
	// replaces it with persistent usage; paid occupancy survives worker cleanup.
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM pro_card_reservations WHERE email=?`, email)
	return err
}

const proOpenedCondition = `(EXISTS(SELECT 1 FROM pro_card_usage u WHERE u.email=m.email AND u.status='success') OR coalesce(json_extract(m.profile,'$.pro_auto.steps.recharge'),'')='completed' OR coalesce(json_extract(m.profile,'$.pro_manual_stages.recharge.status'),'')='completed' OR lower(coalesce(json_extract(m.profile,'$.subscription_plan'),'')) LIKE '%pro%' OR lower(coalesce(json_extract(m.profile,'$.current_plan_type'),'')) LIKE '%pro%')`

func proPopulation(q proCapacityQuery) (opened, pending int, err error) {
	err = q.QueryRow(`SELECT count(*) FROM mail_accounts m WHERE json_extract(m.profile,'$.management_scope')='pro' AND json_extract(m.profile,'$.pro_dead') IS NULL AND coalesce(json_extract(m.profile,'$.space_merged_once'),0)=0 AND json_extract(m.profile,'$.pro_deactivation.accepted_at') IS NULL AND json_extract(m.profile,'$.pro_merge_v2.completed_at') IS NULL AND json_extract(m.profile,'$.pro_merchant.completed_at') IS NULL AND ` + proOpenedCondition).Scan(&opened)
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
