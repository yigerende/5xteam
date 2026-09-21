package store

import (
	"encoding/json"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Job IDs prevent a late response from replacing a newer manual attempt.
func (s *Store) StartProManualStage(email, stage, id string) error {
	_, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		if p.ProAuto.Status == "running" {
			return
		}
		if p.ProManualStages == nil {
			p.ProManualStages = map[string]model.ProStageProgress{}
		}
		now := time.Now()
		p.ProManualStages[stage] = model.ProStageProgress{ID: id, Status: "running", StartedAt: now, UpdatedAt: now}
	})
	return err
}

func (s *Store) FinishProManualStage(email, stage, id, status, message string) error {
	_, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		progress, ok := p.ProManualStages[stage]
		if !ok || progress.ID != id || progress.Status != "running" {
			return
		}
		progress.Status, progress.Error, progress.UpdatedAt = status, message, time.Now()
		p.ProManualStages[stage] = progress
	})
	return err
}

// Restarted workers cannot resume their original request. Payment remains
// governed by its durable supplier order, which can still be queried.
func (s *Store) RecoverProManualStages() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT email,profile FROM mail_accounts WHERE json_extract(profile,'$.management_scope')='pro' AND json_type(profile,'$.pro_manual_stages')='object'`)
	if err != nil {
		return err
	}
	updates := map[string]model.MailAccountProfile{}
	for rows.Next() {
		var email, raw string
		var p model.MailAccountProfile
		if err = rows.Scan(&email, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			break
		}
		changed := false
		for step, progress := range p.ProManualStages {
			if progress.Status != "running" {
				continue
			}
			progress.Status, progress.Error, progress.UpdatedAt = "interrupted", "服务重启，手动任务已中断，请核实结果后重试", time.Now()
			if step == "recharge" {
				progress.Status, progress.Error = "unknown", "服务重启，请查询原订单确认开通结果，勿重复开通"
			}
			p.ProManualStages[step] = progress
			changed = true
		}
		if changed {
			updates[email] = p
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for email, p := range updates {
		raw, encodeErr := json.Marshal(p)
		if encodeErr != nil {
			return encodeErr
		}
		if _, err = s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", string(raw), email); err != nil {
			return err
		}
	}
	return nil
}

// Backfill display from pre-existing orders without contacting the supplier
// or decrypting payment credentials. Queries are limited to the current page.
func (s *Store) ProLatestPaymentOrders(emails []string) (map[string]gptpay.Order, error) {
	result := map[string]gptpay.Order{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for start := 0; start < len(emails); start += 400 {
		end := min(start+400, len(emails))
		values, args := []string{}, []any{}
		for _, email := range emails[start:end] {
			values = append(values, "(?)")
			args = append(args, strings.ToLower(strings.TrimSpace(email)))
		}
		rows, err := s.db.Query(`WITH targets(email) AS (VALUES `+strings.Join(values, ",")+`)
			SELECT o.profile FROM targets JOIN gptpay_orders o ON o.id=(
				SELECT id FROM gptpay_orders WHERE email=targets.email ORDER BY created_at DESC,id DESC LIMIT 1)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw string
			var order gptpay.Order
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(raw), &order); err != nil {
				break
			}
			result[strings.ToLower(order.Email)] = order
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
