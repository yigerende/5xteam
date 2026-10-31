package store

import (
	"chatgpt-space-merge/internal/cardplatform"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

func (s *Store) GPTPaySettings() (gptpay.Settings, string, error) {
	return s.GPTPayProviderSettings("")
}

// An empty provider reads the active configuration; an explicit one does not
// change the active supplier and can never inherit another supplier's key.
func (s *Store) GPTPayProviderSettings(provider string) (gptpay.Settings, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := gptpay.NormalizeSettings(gptpay.Settings{Provider: provider})
	if err != nil {
		return v, "", err
	}
	var raw, sealed string
	err = s.db.QueryRow("SELECT profile,encrypted_key FROM gptpay_settings WHERE id=1").Scan(&raw, &sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, "", err
	}
	if provider != "" {
		var active gptpay.Settings
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &active); err != nil {
				return v, "", err
			}
		}
		if raw == "" || gptpay.Provider(active.Provider) != provider {
			raw, sealed = "", ""
			err = s.db.QueryRow("SELECT profile,encrypted_key FROM gptpay_provider_settings WHERE provider=?", provider).Scan(&raw, &sealed)
		}
	}
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
	tx, err := s.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if err = s.saveGPTPaySettingsTx(tx, &v, key); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (s *Store) saveGPTPaySettingsTx(tx *sql.Tx, v *gptpay.Settings, key string) error {
	// Archive the active profile first, including legacy installations which only
	// have the singleton. Both rows are updated in the caller's transaction.
	var raw, oldKey string
	err := tx.QueryRow("SELECT profile,encrypted_key FROM gptpay_settings WHERE id=1").Scan(&raw, &oldKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if raw != "" {
		var old gptpay.Settings
		if err = json.Unmarshal([]byte(raw), &old); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO gptpay_provider_settings(provider,profile,encrypted_key) VALUES(?,?,?) ON CONFLICT(provider) DO UPDATE SET profile=excluded.profile,encrypted_key=excluded.encrypted_key`, gptpay.Provider(old.Provider), raw, oldKey); err != nil {
			return err
		}
	}
	if err = s.saveGPTPayProviderSettingsTx(tx, v, key); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO gptpay_settings(id,profile,encrypted_key) SELECT 1,profile,encrypted_key FROM gptpay_provider_settings WHERE provider=? ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_key=excluded.encrypted_key`, v.Provider)
	return err
}

// Update an inactive supplier without switching the active configuration.
func (s *Store) saveGPTPayProviderSettingsTx(tx *sql.Tx, v *gptpay.Settings, key string) error {
	var sealed string
	err := tx.QueryRow("SELECT encrypted_key FROM gptpay_provider_settings WHERE provider=?", v.Provider).Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if strings.TrimSpace(key) != "" {
		sealed, err = s.encrypt(strings.TrimSpace(key))
		if err != nil {
			return err
		}
	}
	v.KeyPresent = sealed != ""
	_, err = tx.Exec(`INSERT INTO gptpay_provider_settings(provider,profile,encrypted_key) VALUES(?,?,?) ON CONFLICT(provider) DO UPDATE SET profile=excluded.profile,encrypted_key=excluded.encrypted_key`, v.Provider, mustJSON(v), sealed)
	return err
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
	if v.MaxAccounts != nil && (*v.MaxAccounts < 0 || *v.MaxAccounts > 10000) {
		return v, fmt.Errorf("最大开通数须为 1～10000，或填 0 沿用全局上限")
	}
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
	tx, err := s.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if v.MaxAccounts != nil {
		key := proCardKey(secret.Number)
		if *v.MaxAccounts == 0 {
			_, err = tx.Exec(`DELETE FROM pro_card_limits WHERE card_key=?`, key)
		} else {
			_, err = tx.Exec(`INSERT INTO pro_card_limits(card_key,max_accounts) VALUES(?,?) ON CONFLICT(card_key) DO UPDATE SET max_accounts=excluded.max_accounts`, key, *v.MaxAccounts)
		}
		if err != nil {
			return v, err
		}
	}
	if err = lockProCardAdmission(tx); err != nil {
		return v, err
	}
	if err = saveProCardTypeTx(tx, v.ID, proCardKey(secret.Number), v.CardType); err != nil {
		return v, err
	}
	// The override belongs to the physical card, independently of duplicate rows.
	stored := v
	stored.MaxAccounts = nil
	// Derived from the current issuer settings, never a persistent card switch.
	stored.IssuerDisabled = false
	stored.NormalPending = false
	raw, _ = json.Marshal(stored)
	if _, err = tx.Exec("INSERT INTO gptpay_cards(id,profile,encrypted_secret,enabled,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,encrypted_secret=excluded.encrypted_secret,enabled=excluded.enabled", v.ID, string(raw), sealed, v.Enabled, formatTime(v.CreatedAt)); err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return v, err
	}
	err = s.decorateProCardLocked(&v, secret)
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
func (s *Store) GPTPayCards(limit, offset int, enabledOnly bool, oaiOnly ...bool) ([]gptpay.Card, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gptPayCardsLocked(limit, offset, enabledOnly, "", oaiOnly...)
}

// Display only the requested physical card type. Filtering before decoration
// avoids decrypting and calculating occupancy for another issuer's entire pool.
func (s *Store) GPTPayCardsByType(cardType string) ([]gptpay.Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cardType != "ejh" && cardType != "nexovcc" && cardType != "other" {
		return nil, fmt.Errorf("银行卡类型无效")
	}
	cards, _, err := s.gptPayCardsLocked(1000000, 0, false, cardType)
	return cards, err
}

func (s *Store) gptPayCardsLocked(limit, offset int, enabledOnly bool, cardType string, oaiOnly ...bool) ([]gptpay.Card, int, error) {
	query := "SELECT c.profile,c.encrypted_secret FROM gptpay_cards c"
	args := []any{}
	if cardType != "" {
		query += " LEFT JOIN pro_card_identities i ON i.card_id=c.id LEFT JOIN pro_card_types t ON t.card_key=i.card_key WHERE COALESCE(t.card_type,'other')=?"
		args = append(args, cardType)
	}
	rows, err := s.db.Query(query+" ORDER BY c.created_at DESC,c.id DESC", args...)
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
		if len(oaiOnly) > 0 && oaiOnly[0] && !cardplatform.IsOAI(v.card.Platform) {
			continue
		}
		if !enabledOnly || v.card.Available {
			items = append(items, v.card)
		}
	}
	// Selection uses the oldest eligible card first, before pagination. The
	// management list remains newest first; refreshes never reset CreatedAt.
	if enabledOnly {
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if a.CreatedAt.IsZero() != b.CreatedAt.IsZero() {
				return !a.CreatedAt.IsZero()
			}
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return a.ID < b.ID
		})
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
	retainGPTPaySuccessTime(&v, gptpay.Order{})
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockProCardAdmission(tx); err != nil {
		return err
	}
	key := proCardKey(snapshot.Input.Number)
	if err = s.validateIssuerCard(tx, key); err != nil {
		return err
	}
	limit, err := proCardLimit(tx, key)
	if err != nil {
		return err
	}
	opened, pending, err := proCardCounts(tx, key, v.Email)
	if err != nil {
		return err
	}
	var reserved bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pro_card_reservations WHERE email=? AND card_key=?)`, v.Email, key).Scan(&reserved); err != nil {
		return err
	}
	// Switching the EJH policy on blocks new admissions, not work already
	// admitted while it was off. Conversion preserves that durable reservation.
	if !reserved {
		if err = validateEJHOccupancy(tx, key, v.Email); err != nil {
			return err
		}
	}
	// Lowering a limit only blocks new work; an admitted automatic login keeps
	// its existing place when converting the reservation into a payment order.
	if !reserved && opened+pending >= limit {
		return fmt.Errorf("银行卡已达开通上限 %d（含在途占位）", limit)
	}
	if err = captureProOrderTimeout(tx, v.Email); err != nil {
		return err
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
	if err = ejhSaveCaseTx(tx, v); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) UpdateGPTPayOrder(v gptpay.Order) error {
	return s.updateGPTPayOrder(v, nil)
}

// Call only for an order created by the current account-locked worker before
// it enters the supplier submission call. Never use this to sweep old orders.
func (s *Store) FailUnsubmittedGPTPayOrder(expected gptpay.Order) (gptpay.Order, error) {
	v := expected
	if expected.Status != "submitting" || expected.RequestID != "" || mustJSON(expected.Remote) != mustJSON(gptpay.RemoteOrder{}) || expected.SucceededAt != nil || expected.OccupancyResolution != nil {
		return v, fmt.Errorf("订单不是本轮尚未提交的初始订单")
	}
	v.Status = "failed"
	v.Error = "本地订单在提交付款前已中止，未向供应商发起开通请求"
	return v, s.updateGPTPayOrder(v, &expected)
}

func (s *Store) updateGPTPayOrder(v gptpay.Order, unsubmitted *gptpay.Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.UpdatedAt = time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockProCardAdmission(tx); err != nil {
		return err
	}
	var currentRaw string
	if err = tx.QueryRow(`SELECT profile FROM gptpay_orders WHERE id=?`, v.ID).Scan(&currentRaw); err != nil {
		return err
	}
	var current gptpay.Order
	if err = json.Unmarshal([]byte(currentRaw), &current); err != nil {
		return err
	}
	if current.Provider == gptpay.XUPay && current.Remote.PaymentState == "succeeded" &&
		(v.Remote.PaymentState != "succeeded" || v.Status == "failed" || v.Remote.ID != current.Remote.ID) {
		return fmt.Errorf("序付已确认付款成功，拒绝将原订单回退为未付款或失败")
	}
	v.SubmissionStarted = v.SubmissionStarted || current.SubmissionStarted
	// Compare the whole initial record inside the transaction. Any intervening
	// response, reconciliation or status write makes automatic cleanup unsafe.
	if unsubmitted != nil && mustJSON(current) != mustJSON(*unsubmitted) {
		return fmt.Errorf("订单已变化，保留原订单占用")
	}
	if current.OccupancyResolution != nil {
		if v.OccupancyResolution == nil || v.Status != current.Status || !v.OccupancyResolution.At.Equal(current.OccupancyResolution.At) {
			return fmt.Errorf("该订单已人工核实解除占用，请刷新后读取最新结果")
		}
		v.OccupancyResolution = current.OccupancyResolution
	} else if v.OccupancyResolution != nil {
		return fmt.Errorf("请使用核实解除入口记录订单处理结果")
	}
	retainGPTPaySuccessTime(&v, current)
	raw, _ := json.Marshal(v)
	_, err = tx.Exec("UPDATE gptpay_orders SET profile=?,active=? WHERE id=?", string(raw), v.Active(), v.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE pro_card_usage SET status=? WHERE order_id=?`, v.Status, v.ID); err != nil {
		return err
	}
	if err = holdFailedProAutoCard(tx, current, v); err != nil {
		return err
	}
	if err = ejhSaveCaseTx(tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

// Cancellation/status polling must not turn an older successful activation
// into the newest one. Preserve the first saved success across stale callers.
func retainGPTPaySuccessTime(v *gptpay.Order, previous gptpay.Order) {
	if previous.SucceededAt != nil && !previous.SucceededAt.IsZero() {
		v.SucceededAt = previous.SucceededAt
		return
	}
	if v.Status != "success" {
		v.SucceededAt = nil
		return
	}
	if v.SucceededAt != nil && !v.SucceededAt.IsZero() {
		return
	}
	at := v.UpdatedAt
	if previous.Status == "success" && !previous.UpdatedAt.IsZero() {
		at = previous.UpdatedAt
	}
	if at.IsZero() {
		at = v.CreatedAt
	}
	if at.IsZero() {
		at = time.Now()
	}
	v.SucceededAt = &at
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
		WHERE ((COALESCE(json_extract(o.profile,'$.remote.id'),'')<>''
		AND (COALESCE(json_extract(o.profile,'$.status'),'')<>'manual_review' OR json_extract(o.profile,'$.remote.automaticRunning')=1 OR json_extract(o.profile,'$.check_pending')=1)
		AND (o.active=1 OR json_extract(o.profile,'$.remote.cancellationStatus') IN ('waiting','pending') OR json_extract(o.profile,'$.remote.automaticRunning')=1 OR json_extract(o.profile,'$.check_pending')=1))
		OR (json_extract(o.profile,'$.provider')='xupay' AND json_extract(o.profile,'$.rate_limited')=1
		AND COALESCE(json_extract(o.profile,'$.remote.id'),'')='' AND json_extract(o.profile,'$.status')='submission_unknown'
		AND COALESCE(json_extract(o.profile,'$.rate_limit_retries'),0)<3 AND json_extract(o.profile,'$.next_status_check_at') IS NOT NULL))
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
