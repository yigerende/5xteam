package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func (s *Store) initializeEJH() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS card_payment_modes(provider TEXT NOT NULL,card_id TEXT NOT NULL,mode TEXT NOT NULL,PRIMARY KEY(provider,card_id));
 CREATE TABLE IF NOT EXISTS ejh_settings(id INTEGER PRIMARY KEY CHECK(id=1),profile TEXT NOT NULL,secret TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS ejh_records(kind TEXT NOT NULL,id TEXT NOT NULL,sealed TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(kind,id));
 CREATE TABLE IF NOT EXISTS ejh_card_links(card_key TEXT PRIMARY KEY,card_id TEXT NOT NULL,order_no TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS ejh_cases(id TEXT PRIMARY KEY,email TEXT NOT NULL,card_id TEXT NOT NULL,profile TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS ejh_cases_email ON ejh_cases(email);
 CREATE INDEX IF NOT EXISTS ejh_cases_card ON ejh_cases(card_id);
 CREATE TABLE IF NOT EXISTS ejh_reversal_fee_rates(order_id TEXT PRIMARY KEY,percent REAL NOT NULL,locked_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS ejh_case_states(order_id TEXT PRIMARY KEY,state TEXT NOT NULL,reason TEXT NOT NULL,updated_at TEXT NOT NULL,revision INTEGER NOT NULL);`)
	if err == nil {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS ejh_return_links(trade_id TEXT PRIMARY KEY,order_id TEXT NOT NULL); CREATE INDEX IF NOT EXISTS ejh_return_links_order ON ejh_return_links(order_id);
 CREATE TABLE IF NOT EXISTS ejh_received_returns(trade_id TEXT PRIMARY KEY,order_id TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS ejh_received_returns_order ON ejh_received_returns(order_id)`)
	}
	if err == nil {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS ejh_operation_claims(scope TEXT PRIMARY KEY,operation_id TEXT NOT NULL UNIQUE)`)
	}
	return err
}
func (s *Store) EJHSettings() (ejh.Settings, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := ejh.DefaultSettings()
	var raw, sealed string
	e := s.db.QueryRow(`SELECT profile,secret FROM ejh_settings WHERE id=1`).Scan(&raw, &sealed)
	if errors.Is(e, sql.ErrNoRows) {
		return v, "", nil
	}
	if e != nil {
		return v, "", e
	}
	if e = json.Unmarshal([]byte(raw), &v); e != nil {
		return v, "", e
	}
	secret, e := s.decryptOptional(sealed)
	v.SecretPresent = secret != ""
	if e == nil {
		e = s.loadEJHSigningLocked(&v)
	}
	return v, secret, e
}
func (s *Store) SaveEJHSettings(v ejh.Settings, secret string) (ejh.Settings, error) {
	v, e := ejh.Normalize(v)
	if e != nil {
		return v, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var old, sealed string
	e = s.db.QueryRow(`SELECT profile,secret FROM ejh_settings WHERE id=1`).Scan(&old, &sealed)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return v, e
	}
	if old != "" {
		var previous ejh.Settings
		_ = json.Unmarshal([]byte(old), &previous)
		var n int
		if e = s.db.QueryRow(`SELECT count(*) FROM ejh_records WHERE kind IN ('operation','card')`).Scan(&n); e != nil {
			return v, e
		}
		if n > 0 && (previous.URL != v.URL || previous.AppKey != v.AppKey) {
			return v, fmt.Errorf("已有卡台记录，禁止切换商户或地址后混用旧订单；请使用独立实例")
		}
	}
	if secret != "" {
		if len([]byte(secret)) < 8 {
			return v, fmt.Errorf("AppSecret 至少需要 8 字节")
		}
		sealed, e = s.encrypt(secret)
		if e != nil {
			return v, e
		}
	}
	if v.Enabled && (v.AppKey == "" || sealed == "") {
		return v, fmt.Errorf("启用前请填写 AppKey 和 AppSecret")
	}
	if e = s.loadEJHSigningLocked(&v); e != nil {
		return v, e
	}
	v.SecretPresent = sealed != ""
	tx, e := s.db.Begin()
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if e = lockProCardAdmission(tx); e != nil {
		return v, e
	}
	if e = s.snapshotEJHReversalFeesTx(tx); e != nil {
		return v, e
	}
	if _, e = tx.Exec(`INSERT INTO ejh_settings VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,secret=excluded.secret`, mustJSON(v), sealed); e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (s *Store) EJHPut(kind, id string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "trade" {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = lockProCardAdmission(tx); err != nil {
			return err
		}
		if err = s.ejhPutTx(tx, kind, id, value); err != nil {
			return err
		}
		return tx.Commit()
	}
	if op, ok := value.(ejh.Operation); kind == "operation" && ok {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = s.ejhPutTx(tx, kind, id, value); err != nil {
			return err
		}
		if op.Status == "completed" || op.Status == "failed" || op.Status == "resolved" {
			if _, err = tx.Exec(`DELETE FROM ejh_operation_claims WHERE operation_id=?`, id); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	return s.ejhPutLocked(kind, id, value)
}

// Persist admission before any supplier mutation. A crash retains the claim;
// only a recorded terminal result or audited resolution can release it.
func (s *Store) EJHBeginOperation(op ejh.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if op.Method == "cancel" {
		// Serialize cancellation admission with payment/reservation admission.
		if err = lockProCardAdmission(tx); err != nil {
			return err
		}
		var sealed string
		if err = tx.QueryRow(`SELECT sealed FROM ejh_records WHERE kind='card' AND id=?`, op.CardID).Scan(&sealed); err != nil {
			return err
		}
		raw, err := s.decrypt(sealed)
		if err != nil {
			return err
		}
		var card ejh.CardRecord
		if err = json.Unmarshal([]byte(raw), &card); err != nil {
			return err
		}
		if op.Automatic {
			if op.OrderNo != card.Remote.OrderNo || op.ID != EJHID("auto-cancel", card.ID) || op.AutoCheckStartedAt == nil {
				return fmt.Errorf("自动销卡请求编号或卡片归属不一致")
			}
			if err = s.ejhAutoCancelCheckTx(tx, card, *op.AutoCheckStartedAt); err != nil {
				return err
			}
		}
		switch card.Remote.Status {
		case "1", "3", "4", "5", "6", "7":
		default:
			return fmt.Errorf("该卡已销卡、销卡处理中或状态待核实，不能再次销卡")
		}
		if card.Error != "" {
			return fmt.Errorf("卡片资料待核实，不能销卡")
		}
		var uncertain bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pro_card_usage u JOIN pro_card_identities i ON i.card_key=u.card_key WHERE i.card_id=? AND u.status NOT IN ('success','failed'))`, card.ID).Scan(&uncertain); err != nil {
			return err
		}
		if uncertain {
			return fmt.Errorf("该卡存在结果未确定的付款，核实原订单后才能销卡")
		}
		cases, err := s.ejhCasesQuery(tx, nil)
		if err != nil {
			return err
		}
		for _, c := range cases {
			var released bool
			if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pro_card_usage u WHERE u.order_id=? AND u.status='success' AND `+paymentOnlyReleasedSQL+`)`, c.ID).Scan(&released); err != nil {
				return err
			}
			if released || (c.ProDead != nil && c.PaymentStatus == "success") {
				continue
			}
			if (c.CardID == card.ID || c.EJHCardID == card.ID) && c.State != "returned" && c.State != "unopened" {
				return fmt.Errorf("该卡有关联账号已开通或已申请待回款，不能销卡")
			}
		}
	}
	if err = tx.QueryRow(`SELECT count(*) FROM ejh_records WHERE kind='operation' AND id=?`, op.ID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return fmt.Errorf("操作已存在，请读取原结果，禁止重复提交")
	}
	scope := "card:" + op.CardID
	if op.Method == "open" {
		scope = "open"
	}
	if _, err = tx.Exec(`INSERT INTO ejh_operation_claims VALUES(?,?)`, scope, op.ID); err != nil {
		return fmt.Errorf("存在并发或待核实资金操作，未再次提交: %w", err)
	}
	if err = s.ejhPutTx(tx, "operation", op.ID, op); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ejhPutLocked(kind, id string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	sealed, e := s.encrypt(string(raw))
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT INTO ejh_records VALUES(?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET sealed=excluded.sealed,updated_at=excluded.updated_at`, kind, id, sealed, formatTime(time.Now()))
	return e
}
func (s *Store) EJHGet(kind, id string, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sealed string
	if e := s.db.QueryRow(`SELECT sealed FROM ejh_records WHERE kind=? AND id=?`, kind, id).Scan(&sealed); e != nil {
		return e
	}
	raw, e := s.decrypt(sealed)
	if e != nil {
		return e
	}
	return json.Unmarshal([]byte(raw), out)
}
func EJHRecords[T any](s *Store, kind string) ([]T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ejhRecordsLocked[T](s, kind)
}
func ejhRecordsLocked[T any](s *Store, kind string) ([]T, error) {
	rows, e := s.db.Query(`SELECT sealed FROM ejh_records WHERE kind=? ORDER BY updated_at DESC,id`, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var sealed string
		if e = rows.Scan(&sealed); e != nil {
			return nil, e
		}
		raw, e := s.decrypt(sealed)
		if e != nil {
			return nil, e
		}
		var v T
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func EJHID(scope, value string) string {
	h := sha256.Sum256([]byte(scope + "|" + value))
	return "ejh-" + fmt.Sprintf("%x", h[:16])
}
func (s *Store) ImportEJHCard(remote ejh.Card, enabled bool, origins ...*ejh.Operation) (result gptpay.Card, retErr error) {
	var empty gptpay.Card
	if remote.OrderNo == "" {
		return empty, fmt.Errorf("卡台未返回订单号")
	}
	id := EJHID("card", remote.OrderNo)
	record := ejh.CardRecord{ID: id, Remote: remote, CreatedAt: time.Now(), SyncedAt: time.Now()}
	defer func() {
		if retErr != nil {
			record.Error = retErr.Error()
			if err := s.EJHPut("card", id, record); err != nil {
				retErr = fmt.Errorf("%v; 保存卡片异常失败: %w", retErr, err)
			}
		}
	}()
	var prior ejh.CardRecord
	if s.EJHGet("card", id, &prior) == nil {
		record.NormalPending, record.NormalPendingAt = prior.NormalPending, prior.NormalPendingAt
		record.Platform, record.TemplateID, record.TemplateName = prior.Platform, prior.TemplateID, prior.TemplateName
		record.CreatedAt = prior.CreatedAt
		if record.CreatedAt.IsZero() {
			record.CreatedAt = prior.SyncedAt
		}
		record.Balance, record.BalanceAt = prior.Balance, prior.BalanceAt
		record.BalanceError, record.BalanceAttemptAt = prior.BalanceError, prior.BalanceAttemptAt
	}
	if remote.Status == "2" || remote.Status == "8" {
		record.NormalPending, record.NormalPendingAt = false, nil
	}
	if record.Platform == "" || (record.TemplateID == "" && len(origins) > 0) {
		if err := s.setEJHCardOrigin(&record, origins...); err != nil {
			return empty, err
		}
	}
	if e := s.EJHPut("card", id, record); e != nil {
		return empty, e
	}
	if e := s.ReconcileEJHRejectedIncrease(id); e != nil {
		return empty, e
	}
	if e := s.reconcileEJHCancellation(remote); e != nil {
		return empty, e
	}
	if len(remote.Validity) != 4 {
		return empty, fmt.Errorf("有效期非 MMYY，卡已保存但未加入 Pro，请核对")
	}
	month, e := strconv.Atoi(remote.Validity[:2])
	year, e2 := strconv.Atoi(remote.Validity[2:])
	if e != nil || e2 != nil || month < 1 || month > 12 {
		return empty, fmt.Errorf("有效期与文档 MMYY 不符，禁止猜测，请向卡台核对")
	}
	secret := gptpay.CardSecret{Number: remote.Number, Month: month, Year: 2000 + year, CVV: remote.CVV}
	if len(secret.CVV) < 3 || len(secret.CVV) > 4 {
		return empty, fmt.Errorf("卡台未返回完整 CVV，不能用于开通")
	}
	for _, r := range secret.Number + secret.CVV {
		if r < '0' || r > '9' {
			return empty, fmt.Errorf("卡号或 CVV 不完整，不能加入 Pro")
		}
	}
	if len(secret.Number) < 12 || len(secret.Number) > 19 {
		return empty, fmt.Errorf("卡号长度无效")
	}
	card := gptpay.Card{ID: id, Name: "EJH " + secret.Number[len(secret.Number)-4:], Enabled: enabled, Source: "ejh", IssuerOrderNo: remote.OrderNo}
	// Newly issued cards are EJH; refreshing an existing card must preserve its
	// explicit classification (including legacy Other).
	s.mu.Lock()
	typeErr := s.db.QueryRow(`SELECT card_type FROM pro_card_types WHERE card_key=?`, proCardKey(secret.Number)).Scan(&card.CardType)
	s.mu.Unlock()
	if errors.Is(typeErr, sql.ErrNoRows) {
		card.CardType = "ejh"
	} else if typeErr != nil {
		return empty, typeErr
	}
	if existing, _, e := s.GPTPayCard(id); e == nil {
		card = existing
		card.MaxAccounts = nil
	}
	card.Platform, card.TemplateID, card.TemplateName = record.Platform, record.TemplateID, record.TemplateName
	card.Source, card.IssuerOrderNo = "ejh", remote.OrderNo
	card.IssuerStatus = remote.Status
	card.IssuerLocked = remote.Locked == "1"
	card.IssuerFrozen = remote.Locked == "1" || remote.Status == "5"
	card.IssuerEnd = remote.EndTime
	s.mu.Lock()
	_, e = s.db.Exec(`INSERT INTO ejh_card_links VALUES(?,?,?) ON CONFLICT(card_key) DO NOTHING`, proCardKey(secret.Number), id, remote.OrderNo)
	if e == nil {
		var linkedID string
		e = s.db.QueryRow(`SELECT card_id FROM ejh_card_links WHERE card_key=? AND order_no=?`, proCardKey(secret.Number), remote.OrderNo).Scan(&linkedID)
		if e == nil && linkedID != id {
			e = fmt.Errorf("卡号与订单号的既有绑定不一致")
		}
	}
	s.mu.Unlock()
	if e != nil {
		return empty, e
	}
	return s.SaveGPTPayCard(card, secret)
}
func (s *Store) EJHSaveTrade(cardID string, t ejh.Trade) error {
	return s.EJHSaveTrades(cardID, []ejh.Trade{t})
}

// Save a complete card collection atomically; readers cannot see half a batch.
func (s *Store) EJHSaveTrades(cardID string, trades []ejh.Trade) error {
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
	for _, t := range trades {
		if err = s.ejhSaveTradeTx(tx, cardID, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ejhSaveTradeTx(tx *sql.Tx, cardID string, t ejh.Trade) error {
	if t.TradeNo == "" || t.OrderNo == "" || t.Type == "" {
		return fmt.Errorf("交易缺少流水号、订单号或类型，不能安全去重")
	}
	id := EJHID("trade", t.OrderNo+"|"+t.TradeNo+"|"+t.Type)
	v := ejh.TradeRecord{ID: id, CardID: cardID, Trade: t, SyncedAt: time.Now()}
	var sealed string
	e := tx.QueryRow(`SELECT sealed FROM ejh_records WHERE kind='trade' AND id=?`, id).Scan(&sealed)
	var changedFrom *ejh.TradeRecord
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if sealed != "" {
		raw, e := s.decrypt(sealed)
		if e != nil {
			return e
		}
		var old ejh.TradeRecord
		if e = json.Unmarshal([]byte(raw), &old); e != nil {
			return e
		}
		v.OrderID, v.OriginalTradeID, v.Confirmed, v.Amount, v.Currency, v.Evidence, v.Source, v.ConfirmedAt = old.OrderID, old.OriginalTradeID, old.Confirmed, old.Amount, old.Currency, old.Evidence, old.Source, old.ConfirmedAt
		if old.Confirmed && (old.Trade.Status != t.Status || old.Trade.CardAmount != t.CardAmount || old.Trade.CardCurrency != t.CardCurrency || old.Trade.RefundAmount != t.RefundAmount || old.Trade.TradeAmount != t.TradeAmount || old.Trade.BillAmount != t.BillAmount || old.Trade.TradeCurrency != t.TradeCurrency || old.Trade.BillCurrency != t.BillCurrency || old.Trade.RefundTag != t.RefundTag) {
			changedFrom = &old
			v.Confirmed = false
			v.ConfirmedAt = nil
			v.Evidence = "供应商流水变更，原确认需复核：" + old.Evidence
		}
	}
	if changedFrom != nil {
		if e = s.ejhPutTx(tx, "match_audit", EJHID("audit", id+time.Now().String()), map[string]any{"action": "supplier_changed", "before": changedFrom, "after": v, "at": time.Now()}); e != nil {
			return e
		}
	}
	if e = s.ejhPutTx(tx, "trade", id, v); e != nil {
		return e
	}
	return nil
}
func ejhSaveCaseTx(tx *sql.Tx, o gptpay.Order) error {
	var raw string
	v := ejh.Case{}
	e := tx.QueryRow(`SELECT profile FROM ejh_cases WHERE id=?`, o.ID).Scan(&raw)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if raw != "" {
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return e
		}
	}
	if o.Status == "success" && v.PaymentStatus != "success" {
		v.RemovedAt = nil
	}
	v.ID, v.Email, v.CardID, v.CardName, v.Last4, v.Plan, v.Provider, v.PaymentStatus, v.CreatedAt = o.ID, o.Email, o.CardID, o.CardName, o.CardLast4, o.PlanCode, o.Provider, o.Status, o.CreatedAt
	if o.Status == "success" && v.OpenedAt == nil {
		now := o.UpdatedAt
		if now.IsZero() {
			now = time.Now()
		}
		v.OpenedAt = &now
	}
	_, e = tx.Exec(`INSERT INTO ejh_cases VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET profile=excluded.profile`, v.ID, v.Email, v.CardID, mustJSON(v))
	return e
}
func (s *Store) EJHBackfillCases() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, e := s.db.Query(`SELECT profile FROM gptpay_orders WHERE id NOT IN (SELECT id FROM ejh_cases)`)
	if e != nil {
		return e
	}
	items := []gptpay.Order{}
	for rows.Next() {
		var raw string
		var o gptpay.Order
		if e = rows.Scan(&raw); e != nil {
			break
		}
		if e = json.Unmarshal([]byte(raw), &o); e != nil {
			break
		}
		items = append(items, o)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, o := range items {
		if e = ejhSaveCaseTx(tx, o); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) EJHMarkRequested(email, source string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = ejhMarkRequestedTx(tx, email, "", source, at); e != nil {
		return e
	}
	return tx.Commit()
}
func ejhMarkRequestedTx(tx *sql.Tx, email, orderID, source string, at time.Time) error {
	var raw string
	var e error
	if orderID != "" {
		e = tx.QueryRow(`SELECT profile FROM ejh_cases WHERE email=? AND id=? AND json_extract(profile,'$.payment_status')='success'`, email, orderID).Scan(&raw)
	} else {
		e = tx.QueryRow(`SELECT profile FROM ejh_cases WHERE email=? AND json_extract(profile,'$.payment_status')='success' AND json_extract(profile,'$.plan')<>'codex_point' ORDER BY json_extract(profile,'$.created_at') DESC LIMIT 1`, email).Scan(&raw)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var v ejh.Case
	if e = json.Unmarshal([]byte(raw), &v); e != nil {
		return e
	}
	if v.RequestedAt == nil {
		v.RequestedAt = &at
		v.RequestSource = source
	}
	_, e = tx.Exec(`UPDATE ejh_cases SET profile=? WHERE id=?`, mustJSON(v), v.ID)
	return e
}
func (s *Store) EJHCases() ([]ejh.Case, error) {
	return s.EJHCasesForEmails(nil)
}
func (s *Store) EJHCasesForEmails(emails []string) ([]ejh.Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ejhCasesQuery(s.db, emails)
}

type ejhCaseQuery interface {
	Query(string, ...any) (*sql.Rows, error)
}

// Compact SQL projections avoid decoding supplier event logs and unrelated
// account history for every financial row. Keep the original model decisions.
const ejhCaseAccountProjection = `json_object('management_scope',json_extract(m.profile,'$.management_scope'),'mail_pro_actions',json_extract(m.profile,'$.mail_pro_actions'),'pro_invite_status',json_extract(m.profile,'$.pro_invite_status'),'pro_accept_status',json_extract(m.profile,'$.pro_accept_status'),'pro_transfer_status',json_extract(m.profile,'$.pro_transfer_status'),'pro_remove_status',json_extract(m.profile,'$.pro_remove_status'),'vm2api',json_extract(m.profile,'$.vm2api'),'pro_post_merge_groups',json_extract(m.profile,'$.pro_post_merge_groups'),'pro_deactivation',json_extract(m.profile,'$.pro_deactivation'),'pro_merchant',json_extract(m.profile,'$.pro_merchant'),'pro_merge_v2',json_extract(m.profile,'$.pro_merge_v2'),'pro_auto',json_object('order_id',json_extract(m.profile,'$.pro_auto.order_id')))`
const ejhCaseOrderProjection = `json_object('updated_at',json_extract(o.profile,'$.updated_at'),'remote',json_object('id',json_extract(o.profile,'$.remote.id'),'creditUnit',json_extract(o.profile,'$.remote.creditUnit'),'chargedCredits',json_extract(o.profile,'$.remote.chargedCredits'),'reservedCredits',json_extract(o.profile,'$.remote.reservedCredits'),'releasedCredits',json_extract(o.profile,'$.remote.releasedCredits'),'settlementStatus',json_extract(o.profile,'$.remote.settlementStatus')))`

// Only a retained, completed flow with no evidence of downstream use qualifies.
// Missing/deleted accounts, running flows and prior pushes remain unknown. This
// scalar projection reads no credentials or event logs and adds no per-row query.
const ejhConsumptionNoSourceProjection = `(
 COALESCE(json_extract(m.profile,'$.pro_auto.status'),'')='completed'
 AND COALESCE(json_extract(m.profile,'$.pro_workflow_running'),0)=0
 AND COALESCE(json_extract(m.profile,'$.push_provider'),'')=''
 AND COALESCE(json_extract(m.profile,'$.push_status'),'')=''
 AND COALESCE(json_extract(m.profile,'$.sub2_account_id'),0)=0
 AND COALESCE(json_extract(m.profile,'$.sub2_account_name'),'')=''
 AND COALESCE(json_extract(m.profile,'$.cpa_auth_file_name'),'')=''
 AND json_extract(m.profile,'$.vm2api') IS NULL
 AND json_extract(m.profile,'$.pro_siwc') IS NULL
 AND json_extract(m.profile,'$.pro_push_downstream') IS NULL
 AND json_extract(m.profile,'$.pro_push_started_at') IS NULL
 AND json_extract(m.profile,'$.pro_push_ended_at') IS NULL
 AND COALESCE(json_extract(m.profile,'$.cost_provider'),'')=''
 AND COALESCE(json_extract(m.profile,'$.cost_connection_identity'),'')=''
 AND COALESCE(json_extract(m.profile,'$.cost_downstream_identity'),'')=''
 AND COALESCE(json_extract(m.profile,'$.user_cost_downstream_identity'),'')=''
 AND COALESCE(json_extract(m.profile,'$.cost_downstream_snapshot'),0)=0
 AND COALESCE(json_extract(m.profile,'$.user_cost_downstream_snapshot'),0)=0
 AND json_extract(m.profile,'$.cost_checked_at') IS NULL
 AND COALESCE(json_extract(m.profile,'$.pro_pending_push_target_id'),'')=''
)`

// A started step alone cannot establish whether the downstream create succeeded.
// Only an explicit audited review can classify interrupted steps without erasing them.
const ejhConsumptionNotPushedProjection = `(` + ejhConsumptionNoSourceProjection + `
 AND COALESCE(json_extract(m.profile,'$.pro_auto.stage'),'')<>'push'
 AND json_extract(m.profile,'$.pro_auto.steps.push') IS NULL
 AND json_extract(m.profile,'$.pro_auto.stage_times.push') IS NULL
 AND json_extract(m.profile,'$.pro_auto.stage_times."push:running"') IS NULL
 AND json_extract(m.profile,'$.pro_auto.stage_times."push:completed"') IS NULL
 AND json_extract(m.profile,'$.pro_auto.stage_times."push:failed"') IS NULL
 AND json_extract(m.profile,'$.pro_manual_stages.push') IS NULL
 AND json_extract(m.profile,'$.pro_stage_overrides.push') IS NULL
)`

const ejhConsumptionReviewProjection = `json_object('account_id',json_extract(m.profile,'$.id'),'flow_id',json_extract(m.profile,'$.pro_auto.id'),'order_id',json_extract(m.profile,'$.pro_auto.order_id'),'push_status',json_extract(m.profile,'$.push_status'),'push_step',json_extract(m.profile,'$.pro_auto.steps.push'),'push_times',json_extract(m.profile,'$.pro_auto.stage_times.push','$.pro_auto.stage_times."push:running"','$.pro_auto.stage_times."push:completed"','$.pro_auto.stage_times."push:failed"','$.pro_auto.stage_times."push:interrupted"'),'manual_push',json_extract(m.profile,'$.pro_manual_stages.push'),'override_push',json_extract(m.profile,'$.pro_stage_overrides.push'))`

func (s *Store) ejhCasesQuery(q ejhCaseQuery, emails []string) ([]ejh.Case, error) {
	rates, err := ejhReversalFeeRates(q)
	if err != nil {
		return nil, err
	}
	merchantFees, err := proMerchantFeesByOrder(q)
	if err != nil {
		return nil, err
	}
	overrides, err := ejhCaseStateOverrides(q)
	if err != nil {
		return nil, err
	}
	where := ""
	args := []any{}
	if emails != nil {
		if len(emails) == 0 {
			return []ejh.Case{}, nil
		}
		marks := make([]string, len(emails))
		for i, email := range emails {
			marks[i] = "?"
			args = append(args, strings.ToLower(email))
		}
		where = " WHERE c.email IN (" + strings.Join(marks, ",") + ")"
	}
	tradeRows, e := q.Query(`SELECT r.sealed FROM ejh_records r JOIN ejh_return_links l ON l.trade_id=r.id JOIN ejh_cases c ON c.id=l.order_id WHERE r.kind='trade'`+strings.Replace(where, " WHERE ", " AND ", 1), args...)
	if e != nil {
		return nil, e
	}
	byOrder := map[string][]ejh.TradeRecord{}
	for tradeRows.Next() {
		var sealed string
		if e = tradeRows.Scan(&sealed); e != nil {
			break
		}
		var raw string
		raw, e = s.decrypt(sealed)
		if e != nil {
			break
		}
		var t ejh.TradeRecord
		if e = json.Unmarshal([]byte(raw), &t); e != nil {
			break
		}
		byOrder[t.OrderID] = append(byOrder[t.OrderID], t)
	}
	if e == nil {
		e = tradeRows.Err()
	}
	tradeRows.Close()
	if e != nil {
		return nil, e
	}
	rows, e := q.Query(`SELECT c.profile,m.email,CASE WHEN k.card_type='ejh' THEN COALESCE(l.card_id,i.card_id,c.card_id) ELSE '' END,COALESCE(k.card_type,'other'),charge.sealed,`+ejhCaseOrderProjection+`,
        CASE WHEN COALESCE(json_extract(m.profile,'$.user_cost_downstream_identity'),'')<>''
        THEN COALESCE(json_extract(m.profile,'$.user_cost_downstream_snapshot'),0) END,COALESCE(oc.profile,'{}'),`+ejhCaseAccountProjection+`,COALESCE((SELECT profile FROM pro_dead_accounts WHERE email=c.email),'null'),`+ejhConsumptionNotPushedProjection+`,`+ejhConsumptionNoSourceProjection+`,`+ejhConsumptionReviewProjection+` FROM ejh_cases c
        LEFT JOIN ejh_records charge ON charge.kind='trade' AND charge.id=json_extract(c.profile,'$.original_trade_id')
        LEFT JOIN gptpay_orders o ON o.id=c.id
		LEFT JOIN mail_accounts m ON m.email=c.email
        LEFT JOIN pro_oai_confirmation_accounts oc ON oc.email=c.email
		LEFT JOIN pro_card_usage u ON u.order_id=c.id
  LEFT JOIN pro_card_identities i ON i.card_id=c.card_id
  LEFT JOIN pro_card_types k ON k.card_key=COALESCE(u.card_key,i.card_key)
		LEFT JOIN ejh_card_links l ON l.card_key=u.card_key
  `+where+` ORDER BY json_extract(c.profile,'$.created_at') DESC`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ejh.Case{}
	for rows.Next() {
		var raw string
		var exists sql.NullString
		var ejhCardID, cardType string
		var chargeRaw, orderRaw sql.NullString
		var consumption sql.NullFloat64
		var notPushed, noSource bool
		var reviewContext string
		var oaiRaw, oaiAccountRaw, deadRaw string
		if e = rows.Scan(&raw, &exists, &ejhCardID, &cardType, &chargeRaw, &orderRaw, &consumption, &oaiRaw, &oaiAccountRaw, &deadRaw, &notPushed, &noSource, &reviewContext); e != nil {
			return nil, e
		}
		var v ejh.Case
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(oaiRaw), &v.ProOAIConfirmations); e != nil {
			return nil, e
		}
		var oaiAccount model.MailAccountProfile
		if e = json.Unmarshal([]byte(oaiAccountRaw), &oaiAccount); e != nil {
			return nil, e
		}
		v.ProOAIActionsAvailable = oaiAccount.ManagementScope == "pro" && (oaiAccount.ProAuto.OrderID == "" || oaiAccount.ProAuto.OrderID == v.ID)
		if v.ProOAIActionsAvailable {
			v.ProOAIAvailableModes = oaiAccount.OAIAvailableModes()
		}
		if e = json.Unmarshal([]byte(deadRaw), &v.ProDead); e != nil {
			return nil, e
		}
		v.MerchantFeeUSD = merchantFees[v.ID]
		v.ConsumptionTotal = nil
		v.ConsumptionState = ""
		v.ConsumptionReviewKey = ejhConsumptionReviewKey(reviewContext)
		v.ConsumptionCanReview = exists.Valid && noSource && oaiAccount.ProAuto.OrderID == v.ID && cardType == "ejh"
		if consumption.Valid {
			v.ConsumptionTotal = &consumption.Float64
		} else if exists.Valid && notPushed && oaiAccount.ProAuto.OrderID == v.ID {
			v.ConsumptionState = "not_pushed"
		}
		if v.ConsumptionTotal == nil && v.ConsumptionReview != nil && v.ConsumptionReview.State == "not_pushed" &&
			(!exists.Valid || v.ConsumptionCanReview && v.ConsumptionReview.Context == v.ConsumptionReviewKey) {
			v.ConsumptionState = "not_pushed"
		}
		v.EJHCardID = ejhCardID
		v.CardType = cardType
		if chargeRaw.Valid {
			decoded, err := s.decrypt(chargeRaw.String)
			if err != nil {
				return nil, err
			}
			var charge ejh.TradeRecord
			if err = json.Unmarshal([]byte(decoded), &charge); err != nil {
				return nil, err
			}
			t := charge.Trade
			if t.Type == "1" || t.Type == "4" {
				v.Charge = &ejh.ActivationCharge{ID: charge.ID, TradeNo: t.TradeNo, Type: t.Type, Status: t.Status, CardAmount: t.CardAmount, CardCurrency: t.CardCurrency, TradeAmount: t.TradeAmount, TradeCurrency: t.TradeCurrency, BillAmount: t.BillAmount, BillCurrency: t.BillCurrency, Merchant: t.Merchant, TradeTime: t.TradeTime, SyncedAt: charge.SyncedAt}
			}
		}
		if orderRaw.Valid {
			var order gptpay.Order
			if e = json.Unmarshal([]byte(orderRaw.String), &order); e != nil {
				return nil, e
			}
			if order.Remote.ID != "" {
				v.Credits = &ejh.ActivationCredits{Unit: order.Remote.CreditUnit, Charged: order.Remote.ChargedCredits, Reserved: order.Remote.ReservedCredits, Released: order.Remote.ReleasedCredits, Settlement: order.Remote.SettlementStatus, UpdatedAt: order.UpdatedAt}
			}
		}
		v.Deleted = !exists.Valid
		v.State = "unopened"
		if v.PaymentStatus == "success" {
			v.State = "opened"
		}
		if v.RequestedAt != nil {
			v.State = "waiting"
		}
		v.Returns = []ejh.TradeRecord{}
		v.Amounts = map[string]string{}
		for _, t := range byOrder[v.ID] {
			t.Trade.Number = ""
			v.Returns = append(v.Returns, t)
			if t.Confirmed {
				v.State = "returned"
				if t.Trade.Type == "2" {
					v.Reversals++
				} else {
					v.Refunds++
				}
				v.Amounts[t.Currency] = t.Amount
			}
		}
		v.AutomaticState = v.State
		v.StateOverride = overrides[v.ID]
		if v.StateOverride != nil && ejh.ValidCaseState(v.StateOverride.State) {
			v.State = v.StateOverride.State
		}
		v.ReversalFee = nil
		if v.CardType == "ejh" && v.State == "returned" {
			v.ReversalFee = rates[v.ID]
		}
		for i := range v.Returns {
			v.Returns[i].ReversalFee = v.ReversalFee
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) EJHMatchTrade(id, orderID, originalID, amount, currency, evidence string, confirmed bool) error {
	if len(strings.TrimSpace(evidence)) < 3 {
		return fmt.Errorf("请填写核对依据，不能仅凭金额和时间认定")
	}
	if confirmed {
		if e := ejh.Positive(amount); e != nil {
			return e
		}
		if !ejh.ValidCurrency(currency) {
			return fmt.Errorf("确认金额需填写币种")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	trades, e := ejhRecordsLocked[ejh.TradeRecord](s, "trade")
	if e != nil {
		return e
	}
	var target *ejh.TradeRecord
	for i := range trades {
		if trades[i].ID == id {
			target = &trades[i]
		}
	}
	if target == nil || !target.Trade.IsReturn() {
		return fmt.Errorf("仅可关联授权撤销或交易退款")
	}
	if target.Confirmed {
		return fmt.Errorf("已确认流水不可覆盖，请先解除关联并保留更正依据")
	}
	if target.OrderID != "" && target.OrderID != orderID {
		return fmt.Errorf("流水已有归属，修改前请先解除关联并记录原因")
	}
	var raw string
	if e = s.db.QueryRow(`SELECT profile FROM ejh_cases WHERE id=?`, orderID).Scan(&raw); e != nil {
		return fmt.Errorf("开通订单不存在")
	}
	var c ejh.Case
	if e = json.Unmarshal([]byte(raw), &c); e != nil {
		return e
	}
	if c.CardID != target.CardID || c.PaymentStatus != "success" {
		var linkedCard string
		if c.PaymentStatus != "success" {
			return fmt.Errorf("只能关联成功开通订单")
		}
		if e = s.db.QueryRow(`SELECT l.card_id FROM pro_card_usage u JOIN ejh_card_links l ON l.card_key=u.card_key WHERE u.order_id=?`, orderID).Scan(&linkedCard); e != nil || linkedCard != target.CardID {
			return fmt.Errorf("只能关联同一张实体卡的成功开通订单")
		}
	}
	var accountReturns int
	if e = s.db.QueryRow(`SELECT count(*) FROM ejh_return_links l JOIN ejh_cases c ON c.id=l.order_id WHERE lower(c.email)=lower(?) AND l.trade_id<>?`, c.Email, id).Scan(&accountReturns); e != nil {
		return e
	}
	if accountReturns > 0 {
		return fmt.Errorf("该账号已关联一次回款，不允许跨订单重复记账；请先核对并解除错误关联")
	}
	if originalID == "" {
		return fmt.Errorf("请选择对应的原授权或清算流水")
	}
	var original *ejh.TradeRecord
	for i := range trades {
		t := &trades[i]
		if t.ID != id && t.OrderID == orderID {
			return fmt.Errorf("该开通订单已关联一次回款，请先解除错误关联，不允许重复记账")
		}
		if t.ID == originalID {
			original = t
		}
		if t.OrderID != "" && t.OriginalTradeID == originalID && t.OrderID != orderID {
			return fmt.Errorf("原交易已关联其他账号订单，不能重复分配")
		}
	}
	if original == nil || original.CardID != target.CardID || (original.Trade.Type != "1" && original.Trade.Type != "4") {
		return fmt.Errorf("原交易须为同卡授权或清算")
	}
	if confirmed && target.Trade.Status != "2" {
		return fmt.Errorf("卡台交易尚未入账，不能确认资金已退回")
	}
	if c.OriginalTradeID != "" && c.OriginalTradeID != originalID {
		return fmt.Errorf("该开通订单已关联其他原交易，请先核对并解除旧关联")
	}
	if e = chargeAvailable(s.db, trades, originalID, orderID); e != nil {
		return e
	}
	c.OriginalTradeID = originalID
	before := *target
	target.OrderID, target.OriginalTradeID, target.Amount, target.Currency, target.Evidence, target.Source, target.Confirmed = orderID, originalID, amount, currency, evidence, "manual_verified", confirmed
	if confirmed {
		now := time.Now()
		target.ConfirmedAt = &now
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = s.ejhPutTx(tx, "match_audit", EJHID("audit", id+time.Now().String()), map[string]any{"before": before, "after": target, "at": time.Now()}); e != nil {
		return e
	}
	if e = s.ejhPutTx(tx, "trade", id, target); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO ejh_return_links VALUES(?,?) ON CONFLICT(trade_id) DO UPDATE SET order_id=excluded.order_id`, id, orderID); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE ejh_cases SET profile=? WHERE id=?`, mustJSON(c), c.ID); e != nil {
		return e
	}
	if e = s.snapshotEJHReversalFeesTx(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) EJHUnmatch(id, reason string) error {
	if len(strings.TrimSpace(reason)) < 3 {
		return fmt.Errorf("请填写解除关联的核实依据")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var t ejh.TradeRecord
	var sealed string
	if e := s.db.QueryRow(`SELECT sealed FROM ejh_records WHERE kind='trade' AND id=?`, id).Scan(&sealed); e != nil {
		return e
	}
	raw, e := s.decrypt(sealed)
	if e != nil {
		return e
	}
	if e = json.Unmarshal([]byte(raw), &t); e != nil {
		return e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = s.ejhPutTx(tx, "match_audit", EJHID("audit", id+time.Now().String()), map[string]any{"before": t, "reason": reason, "action": "unmatch", "at": time.Now()}); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE ejh_cases SET profile=json_set(profile,'$.original_trade_id','') WHERE id=? AND COALESCE(json_extract(profile,'$.charge_evidence'),'')=''`, t.OrderID); e != nil {
		return e
	}
	t.OrderID, t.OriginalTradeID, t.Amount, t.Currency, t.Evidence, t.Source = "", "", "", "", reason, ""
	t.Confirmed = false
	t.ConfirmedAt = nil
	if e = s.ejhPutTx(tx, "trade", id, t); e != nil {
		return e
	}
	if _, e = tx.Exec(`DELETE FROM ejh_return_links WHERE trade_id=?`, id); e != nil {
		return e
	}
	if e = s.snapshotEJHReversalFeesTx(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) ejhPutTx(tx *sql.Tx, kind, id string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	sealed, e := s.encrypt(string(raw))
	if e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO ejh_records VALUES(?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET sealed=excluded.sealed,updated_at=excluded.updated_at`, kind, id, sealed, formatTime(time.Now()))
	if e == nil && kind == "trade" {
		var t ejh.TradeRecord
		if e = json.Unmarshal(raw, &t); e == nil {
			e = saveEJHReceivedReturnTx(tx, t)
		}
	}
	return e
}

// Maintain the admission index in the same transaction as the monetary evidence.
// A changed/unconfirmed return immediately restores its order's occupancy.
func saveEJHReceivedReturnTx(tx *sql.Tx, t ejh.TradeRecord) error {
	// Live evidence supersedes an imported summary, including corrections or
	// removal of a previously confirmed association.
	if _, err := tx.Exec(`DELETE FROM ejh_received_returns WHERE trade_id='transfer:' || ?
 OR trade_id IN (SELECT 'transfer:' || order_id FROM ejh_received_returns WHERE trade_id=?)`, t.OrderID, t.ID); err != nil {
		return err
	}
	received := t.Confirmed && t.OrderID != "" && ejh.ReturnReceived(t.Trade) &&
		ejh.Positive(t.Amount) == nil && ejh.ValidCurrency(t.Currency)
	if !received {
		_, err := tx.Exec("DELETE FROM ejh_received_returns WHERE trade_id=?", t.ID)
		return err
	}
	_, err := tx.Exec(`INSERT INTO ejh_received_returns VALUES(?,?) ON CONFLICT(trade_id) DO UPDATE SET order_id=excluded.order_id`, t.ID, t.OrderID)
	return err
}
