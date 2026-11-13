package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

type EJHConsumptionReviewInput struct {
	State            string `json:"state"`
	Reason           string `json:"reason"`
	ExpectedKey      string `json:"expected_key"`
	ExpectedRevision *int64 `json:"expected_revision"`
	Confirm          bool   `json:"confirm"`
}

func ejhConsumptionReviewKey(raw string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}

// Check every saved destination, including old configurations. A pending create
// must be reconciled at its original downstream, never hidden by this review.
func (s *Store) ejhConsumptionPendingPushTx(tx *sql.Tx, accountID string) (bool, error) {
	rows, err := tx.Query("SELECT encrypted_payload FROM downstream_connections")
	if err != nil {
		return false, err
	}
	endpoints := map[string]bool{}
	for rows.Next() {
		var sealed string
		if err = rows.Scan(&sealed); err != nil {
			rows.Close()
			return false, err
		}
		raw, e := s.decrypt(sealed)
		if e != nil {
			rows.Close()
			return false, e
		}
		var target model.DownstreamTarget
		if e = json.Unmarshal([]byte(raw), &target); e != nil {
			rows.Close()
			return false, e
		}
		if target.Provider == "sub2" && target.Sub2.URL != "" {
			endpoint, e := sub2.EndpointKey(target.Sub2.URL)
			if e != nil {
				rows.Close()
				return false, e
			}
			endpoints[endpoint] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	var settingsRaw string
	if err = tx.QueryRow("SELECT profile FROM pro_settings WHERE id=1").Scan(&settingsRaw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if settingsRaw != "" {
		var settings model.ProSettings
		if err = json.Unmarshal([]byte(settingsRaw), &settings); err != nil {
			return false, err
		}
		if settings.Sub2.URL != "" {
			endpoint, e := sub2.EndpointKey(settings.Sub2.URL)
			if e != nil {
				return false, e
			}
			endpoints[endpoint] = true
		}
	}
	for endpoint := range endpoints {
		for _, prefix := range []string{"pro:", "pro-siwc:"} {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(endpoint+"\x00"+prefix+accountID)))
			var exists bool
			if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM sub2_push_attempts WHERE id=?)", id).Scan(&exists); err != nil {
				return false, err
			}
			if exists {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Store) SetEJHConsumptionReview(id string, in EJHConsumptionReviewInput) (ejh.Case, error) {
	var out ejh.Case
	in.Reason = strings.TrimSpace(in.Reason)
	if !in.Confirm || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || in.ExpectedKey == "" {
		return out, fmt.Errorf("请确认核实结果并刷新最新记录")
	}
	if in.State != "not_pushed" && in.State != "auto" {
		return out, fmt.Errorf("请选择确认未推送或恢复自动判断")
	}
	if n := utf8.RuneCountInString(in.Reason); n < 3 || n > 600 {
		return out, fmt.Errorf("请填写 3～600 字的核实依据")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = lockProCardAdmission(tx); err != nil {
		return out, err
	}
	var email string
	if err = tx.QueryRow("SELECT email FROM ejh_cases WHERE id=?", id).Scan(&email); err != nil {
		return out, err
	}
	cases, err := s.ejhCasesQuery(tx, []string{email})
	if err != nil {
		return out, err
	}
	for _, row := range cases {
		if row.ID == id {
			out = row
			break
		}
	}
	if out.ID == "" || out.CardType != "ejh" {
		return out, fmt.Errorf("仅可核实 EJH 关联账号")
	}
	revision := int64(0)
	if out.ConsumptionReview != nil {
		revision = out.ConsumptionReview.Revision
	}
	if revision != *in.ExpectedRevision || out.ConsumptionReviewKey != in.ExpectedKey {
		return out, fmt.Errorf("账号记录已变化，请刷新后重新核实")
	}
	if in.State == "not_pushed" {
		if !out.ConsumptionCanReview || out.ConsumptionTotal != nil {
			return out, fmt.Errorf("仅可核实已结束、没有下游绑定或消费记录的账号")
		}
		var profileRaw string
		if err = tx.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&profileRaw); err != nil {
			return out, err
		}
		var profile model.MailAccountProfile
		if err = json.Unmarshal([]byte(profileRaw), &profile); err != nil {
			return out, err
		}
		pending, e := s.ejhConsumptionPendingPushTx(tx, profile.ID)
		if e != nil {
			return out, e
		}
		if pending {
			return out, fmt.Errorf("存在待确认的下游推送，请先核实原下游结果")
		}
		rows, e := tx.Query("SELECT sealed FROM ejh_records WHERE kind='finance_usage'")
		if e != nil {
			return out, e
		}
		known := false
		for rows.Next() {
			var sealed string
			if err = rows.Scan(&sealed); err != nil {
				rows.Close()
				return out, err
			}
			raw, e := s.decrypt(sealed)
			if e != nil {
				rows.Close()
				return out, e
			}
			var record EJHConsumption
			if e = json.Unmarshal([]byte(raw), &record); e != nil {
				rows.Close()
				return out, e
			}
			if strings.EqualFold(strings.TrimSpace(record.Email), email) && record.Total >= 0 && !math.IsNaN(record.Total) && !math.IsInf(record.Total, 0) {
				known = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if known {
			return out, fmt.Errorf("该账号已有消费历史，不能标记为未推送")
		}
	}
	change := ejh.ConsumptionReview{State: in.State, Reason: in.Reason, Context: out.ConsumptionReviewKey, Revision: revision + 1, UpdatedAt: time.Now()}
	raw, err := json.Marshal(change)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec("UPDATE ejh_cases SET profile=json_set(profile,'$.consumption_review',json(?)) WHERE id=?", string(raw), id); err != nil {
		return out, err
	}
	if err = s.ejhPutTx(tx, "match_audit", EJHID("audit", fmt.Sprintf("consumption-review:%s:%d", id, change.Revision)), map[string]any{"action": "consumption_review", "order_id": id, "email": email, "before": out.ConsumptionReview, "after": change, "at": change.UpdatedAt}); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.ConsumptionReview = &change
	out.ConsumptionState = ""
	if in.State == "not_pushed" {
		out.ConsumptionState = "not_pushed"
	}
	return out, nil
}
