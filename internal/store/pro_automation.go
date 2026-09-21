package store

import (
	"chatgpt-space-merge/internal/model"
	"encoding/json"
	"time"
)

// Only due automation records are loaded, never every mailbox each second.
func (s *Store) DueProAutomations(now time.Time) ([]model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT profile FROM mail_accounts WHERE json_extract(profile,'$.management_scope')='pro' AND json_extract(profile,'$.pro_auto.status')='waiting_quota' AND json_extract(profile,'$.pro_auto.next_check_at')<=? ORDER BY json_extract(profile,'$.pro_auto.next_check_at') LIMIT 20`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.MailAccountProfile{}
	for rows.Next() {
		var raw string
		var p model.MailAccountProfile
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func (s *Store) RecoverProAutomations() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT email,profile FROM mail_accounts WHERE json_extract(profile,'$.pro_auto.status')='running' OR (json_extract(profile,'$.management_scope')='pro' AND json_extract(profile,'$.pro_transfer_status')='completed' AND COALESCE(json_extract(profile,'$.pro_auto.id'),'')!='')`)
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
		if p.ReconcileProAutoMerge(time.Now()) {
			updates[email] = p
			continue
		}
		if p.ProAuto.Status != "running" {
			continue
		}
		p.ProAuto.Status = "interrupted"
		p.ProAuto.Error = "服务重启，原登录会话已释放；请查询原订单，禁止重复开通"
		if p.ProAuto.Steps["oauth"] == "completed" {
			p.ProAuto.Error = "服务重启，凭证已保存，可点击全自动按钮续跑后续步骤"
		}
		if p.ProAuto.Steps[p.ProAuto.Stage] == "running" {
			p.ProAuto.Steps[p.ProAuto.Stage] = "interrupted"
		}
		p.ProAuto.UpdatedAt = time.Now()
		updates[email] = p
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for email, p := range updates {
		raw, _ := json.Marshal(p)
		if _, err = s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", string(raw), email); err != nil {
			return err
		}
	}
	return nil
}
