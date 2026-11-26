package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

var ErrEJHAdjustmentChanged = errors.New("核对数据已变化，请重新预览后平账")
var adjustmentID = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,100}$`)
var adjustmentMoney = regexp.MustCompile(`^-?\d{1,12}\.\d{2}$`)

type EJHAdjustmentSummary struct {
	Amount    string     `json:"amount"`
	Count     int64      `json:"count"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}
type EJHAdjustmentEvidence struct {
	Version         string          `json:"version"`
	RawGap          string          `json:"raw_gap"`
	TotalBefore     string          `json:"total_before"`
	GapBefore       string          `json:"gap_before"`
	SuggestedAmount string          `json:"suggested_amount"`
	CalculatedAt    time.Time       `json:"calculated_at"`
	Snapshot        json.RawMessage `json:"snapshot"`
}
type EJHAdjustment struct {
	EJHAdjustmentEvidence
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Note       string    `json:"note"`
	Amount     string    `json:"amount"`
	TotalAfter string    `json:"total_after"`
	GapAfter   string    `json:"gap_after"`
}

// Read this on a ProListReader to bind the version to the same WAL snapshot.
func (s *Store) EJHAccountingVersion(ctx context.Context) (string, error) {
	var a, b int64
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT revision FROM display_revisions WHERE scope='ejh'),(SELECT revision FROM display_revisions WHERE scope='ejh_finance')`).Scan(&a, &b)
	return fmt.Sprintf("%d:%d", a, b), err
}
func (s *Store) EJHAdjustmentSummary() (EJHAdjustmentSummary, error) {
	out := EJHAdjustmentSummary{Amount: "0.00"}
	err := s.EJHGet("reconciliation_summary", "usd", &out)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, err
}
func (s *Store) EJHAdjustmentByRequest(id string) (EJHAdjustment, error) {
	var out EJHAdjustment
	var ref struct {
		ID string `json:"id"`
	}
	if err := s.EJHGet("reconciliation_request", id, &ref); err != nil {
		return out, err
	}
	err := s.EJHGet("reconciliation_adjustment", ref.ID, &out)
	return out, err
}
func (s *Store) EJHAdjustments(ctx context.Context, page, size int) ([]EJHAdjustment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sealed FROM ejh_records WHERE kind='reconciliation_adjustment' ORDER BY id DESC LIMIT ? OFFSET ?`, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EJHAdjustment{}
	for rows.Next() {
		var sealed string
		if err = rows.Scan(&sealed); err != nil {
			return nil, err
		}
		raw, e := s.decrypt(sealed)
		if e != nil {
			return nil, e
		}
		var item EJHAdjustment
		if e = json.Unmarshal([]byte(raw), &item); e != nil {
			return nil, e
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Append the evidence, idempotency index and cumulative amount atomically. No
// card balance, trade, fee, revenue or business state is changed here.
func (s *Store) AppendEJHAdjustment(ctx context.Context, evidence EJHAdjustmentEvidence, id, actor, note, amount string) (EJHAdjustment, error) {
	var out EJHAdjustment
	note = strings.TrimSpace(note)
	if !adjustmentID.MatchString(id) || note == "" || len([]rune(note)) > 1000 || actor == "" {
		return out, fmt.Errorf("请提供有效请求编号和 1～1000 字平账原因")
	}
	for _, v := range []string{evidence.RawGap, evidence.GapBefore, evidence.TotalBefore, amount} {
		if !adjustmentMoney.MatchString(v) {
			return out, fmt.Errorf("核对金额不完整或无效，不能平账")
		}
	}
	n, _ := new(big.Rat).SetString(amount)
	if n.Sign() == 0 {
		return out, fmt.Errorf("平账金额不能为零")
	}
	raw, _ := new(big.Rat).SetString(evidence.RawGap)
	total, _ := new(big.Rat).SetString(evidence.TotalBefore)
	if new(big.Rat).Add(raw, total).FloatString(2) != evidence.GapBefore {
		return out, fmt.Errorf("核对金额不一致")
	}
	if !json.Valid(evidence.Snapshot) || evidence.CalculatedAt.IsZero() || time.Since(evidence.CalculatedAt) > 2*time.Minute {
		return out, ErrEJHAdjustmentChanged
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var ref struct {
		ID string `json:"id"`
	}
	err = s.ejhGetTx(tx, "reconciliation_request", id, &ref)
	if err == nil {
		if err = s.ejhGetTx(tx, "reconciliation_adjustment", ref.ID, &out); err != nil {
			return out, err
		}
		if out.Note != note || out.Actor != actor || out.Amount != amount {
			return out, fmt.Errorf("请求编号已用于另一笔平账")
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var a, b int64
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT revision FROM display_revisions WHERE scope='ejh'),(SELECT revision FROM display_revisions WHERE scope='ejh_finance')`).Scan(&a, &b); err != nil {
		return out, err
	}
	if fmt.Sprintf("%d:%d", a, b) != evidence.Version {
		return out, ErrEJHAdjustmentChanged
	}
	summary := EJHAdjustmentSummary{Amount: "0.00"}
	if err = s.ejhGetTx(tx, "reconciliation_summary", "usd", &summary); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if summary.Amount != evidence.TotalBefore {
		return out, ErrEJHAdjustmentChanged
	}
	after := new(big.Rat).Add(total, n).FloatString(2)
	if !adjustmentMoney.MatchString(after) {
		return out, fmt.Errorf("累计平账金额超出范围")
	}
	now := time.Now().UTC()
	out = EJHAdjustment{EJHAdjustmentEvidence: evidence, ID: fmt.Sprintf("%020d", summary.Count+1), RequestID: id, At: now, Actor: actor, Note: note, Amount: amount, TotalAfter: after, GapAfter: new(big.Rat).Add(new(big.Rat).Add(raw, total), n).FloatString(2)}
	summary = EJHAdjustmentSummary{Amount: after, Count: summary.Count + 1, UpdatedAt: &now}
	if err = s.ejhPutTx(tx, "reconciliation_adjustment", out.ID, out); err != nil {
		return out, err
	}
	if err = s.ejhPutTx(tx, "reconciliation_request", id, map[string]string{"id": out.ID}); err != nil {
		return out, err
	}
	if err = s.ejhPutTx(tx, "reconciliation_summary", "usd", summary); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
