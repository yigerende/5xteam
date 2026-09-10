package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func smsID() string { return fmt.Sprintf("sms-%d", time.Now().UnixNano()) }

func (s *Store) SMSPhones(provider, state string, limit, offset int) ([]map[string]any, map[string]int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	provider, state = strings.TrimSpace(provider), strings.TrimSpace(state)
	cutoff := formatTime(time.Now().Add(-30 * time.Second))
	now := formatTime(time.Now())
	const cte = `WITH phone_data AS (
		SELECT p.id,p.provider,p.phone_number,p.card_code,p.api_url,p.max_bindings,p.status,p.lease_expires_at,p.note,
			p.last_code,p.last_code_at,p.last_error,p.last_attempt_at,p.created_at,p.updated_at,COUNT(b.gpt_email) AS bind_count,
			CASE
				WHEN p.status!='active' THEN 'disabled'
				WHEN p.lease_expires_at!='' AND p.lease_expires_at<? THEN 'expired'
				WHEN COUNT(b.gpt_email)>=p.max_bindings THEN 'full'
				WHEN p.last_attempt_at!='' AND p.last_attempt_at>=? THEN 'cooldown'
				ELSE 'available'
			END AS computed_state
		FROM sms_phones p LEFT JOIN sms_phone_bindings b ON b.phone_id=p.id
		GROUP BY p.id
	)`
	stats := map[string]int{"total": 0, "available": 0, "cooldown": 0, "full": 0, "expired": 0, "disabled": 0}
	statRows, err := s.db.Query(cte+` SELECT computed_state,COUNT(*) FROM phone_data WHERE (?='' OR provider=?) GROUP BY computed_state`, now, cutoff, provider, provider)
	if err != nil {
		return nil, nil, 0, err
	}
	for statRows.Next() {
		var key string
		var count int
		if statRows.Scan(&key, &count) == nil {
			stats[key] = count
			stats["total"] += count
		}
	}
	statRows.Close()
	var total int
	if err := s.db.QueryRow(cte+` SELECT COUNT(*) FROM phone_data WHERE (?='' OR provider=?) AND (?='' OR computed_state=?)`, now, cutoff, provider, provider, state, state).Scan(&total); err != nil {
		return nil, nil, 0, err
	}
	rows, err := s.db.Query(cte+` SELECT id,provider,phone_number,card_code,api_url,max_bindings,status,lease_expires_at,note,last_code,last_code_at,last_error,last_attempt_at,created_at,updated_at,bind_count,computed_state
		FROM phone_data WHERE (?='' OR provider=?) AND (?='' OR computed_state=?) ORDER BY created_at DESC,id LIMIT ? OFFSET ?`, now, cutoff, provider, provider, state, state, limit, offset)
	if err != nil {
		return nil, nil, 0, err
	}
	// Read and close the page rows before querying bindings. SQLite may have a
	// single active reader; querying bindings while rows is still open can
	// block indefinitely when the store is used with a small connection pool.
	type phoneRow struct {
		id, prov, phone, card, apiURL, status, lease, note, last, lastAt, lastErr, lastAttempt, created, updated, computedState string
		maxBindings, bindCount int
	}
	pageRows := make([]phoneRow, 0, limit)
	for rows.Next() {
		var id, prov, phone, card, apiURL, status, lease, note, last, lastAt, lastErr, lastAttempt, created, updated, computedState string
		var maxBindings, bindCount int
		if err := rows.Scan(&id, &prov, &phone, &card, &apiURL, &maxBindings, &status, &lease, &note, &last, &lastAt, &lastErr, &lastAttempt, &created, &updated, &bindCount, &computedState); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		pageRows = append(pageRows, phoneRow{id: id, prov: prov, phone: phone, card: card, apiURL: apiURL, maxBindings: maxBindings, status: status, lease: lease, note: note, last: last, lastAt: lastAt, lastErr: lastErr, lastAttempt: lastAttempt, created: created, updated: updated, bindCount: bindCount, computedState: computedState})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, 0, err
	}
	rows.Close()
	items := make([]map[string]any, 0, len(pageRows))
	for _, row := range pageRows {
		id, prov, phone, card, apiURL := row.id, row.prov, row.phone, row.card, row.apiURL
		bindings := s.smsBindingsLocked(id)
		masked := card
		if len(card) > 8 {
			masked = card[:4] + "****" + card[len(card)-4:]
		}
		items = append(items, map[string]any{"id": id, "provider": prov, "phone_number": phone, "card_code_masked": masked, "api_url": apiURL, "max_bindings": row.maxBindings, "status": row.status, "lease_expires_at": row.lease, "note": row.note, "last_code": row.last, "last_code_at": row.lastAt, "last_error": row.lastErr, "last_attempt_at": row.lastAttempt, "created_at": row.created, "updated_at": row.updated, "bind_count": row.bindCount, "bindings": bindings, "state": row.computedState})
	}
	return items, stats, total, nil
}

func smsState(status, lease, lastAttempt string, max, count int) string {
	if status != "active" {
		return "disabled"
	}
	if lease != "" {
		if t, e := time.Parse(time.RFC3339, lease); e == nil && t.Before(time.Now()) {
			return "expired"
		}
	}
	if count >= max {
		return "full"
	}
	if lastAttempt != "" {
		if t, e := time.Parse(time.RFC3339Nano, lastAttempt); e == nil && time.Since(t) < 30*time.Second {
			return "cooldown"
		}
	}
	return "available"
}
func (s *Store) smsBindingsLocked(id string) []map[string]any {
	rows, _ := s.db.Query("SELECT gpt_email,phone_number,bound_at FROM sms_phone_bindings WHERE phone_id=? ORDER BY bound_at", id)
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var e, n, b string
		if rows.Scan(&e, &n, &b) == nil {
			out = append(out, map[string]any{"gpt_email": e, "phone_number": n, "bound_at": b})
		}
	}
	return out
}
func (s *Store) SMSProviders() []map[string]any {
	return []map[string]any{{"key": "chongpt", "name": "chongpt", "realtime": false}, {"key": "chong10666", "name": "10666接码", "realtime": false}, {"key": "generic", "name": "通用接码", "realtime": false}, {"key": "hero_sms", "name": "hero-sms(实时)", "realtime": true}, {"key": "nextpro", "name": "nextpro(实时)", "realtime": true}, {"key": "congou", "name": "congou(实时)", "realtime": true}, {"key": "chatai", "name": "chatai(实时)", "realtime": true}}
}
func (s *Store) AddSMSPhone(provider, phone, card, apiURL, lease, note string, max int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM sms_phones WHERE (phone_number<>'' AND phone_number=?) OR (card_code<>'' AND card_code=?)", phone, card).Scan(&n)
	if n > 0 {
		return false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec("INSERT INTO sms_phones(id,provider,phone_number,card_code,api_url,max_bindings,status,lease_expires_at,note,created_at,updated_at) VALUES(?,?,?,?,?,?, 'active',?,?,?,?)", smsID(), provider, phone, card, apiURL, max, lease, note, now, now)
	return true, err
}
func (s *Store) UpdateSMSPhone(id string, fields map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := []string{}
	args := []any{}
	for _, k := range []string{"max_bindings", "status", "note"} {
		if v, ok := fields[k]; ok {
			sets = append(sets, k+"=?")
			args = append(args, v)
		}
	}
	if len(sets) == 0 {
		return errors.New("没有要修改的字段")
	}
	sets = append(sets, "updated_at=?")
	args = append(args, time.Now().UTC().Format(time.RFC3339Nano))
	args = append(args, id)
	_, err := s.db.Exec("UPDATE sms_phones SET "+strings.Join(sets, ",")+" WHERE id=?", args...)
	return err
}
func (s *Store) DeleteSMSPhones(ids []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range ids {
		r, e := s.db.Exec("DELETE FROM sms_phones WHERE id=?", id)
		if e != nil {
			return n, e
		}
		x, _ := r.RowsAffected()
		n += int(x)
		_, _ = s.db.Exec("DELETE FROM sms_phone_bindings WHERE phone_id=?", id)
	}
	return n, nil
}
func (s *Store) SMSPhone(id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var itemID, provider, phone, card, apiURL, status, lease, note, last, lastAt, lastErr, lastAttempt, created, updated string
	var maxBindings int
	if err := s.db.QueryRow(`SELECT id,provider,phone_number,card_code,api_url,max_bindings,status,lease_expires_at,note,
		last_code,last_code_at,last_error,last_attempt_at,created_at,updated_at FROM sms_phones WHERE id=?`, strings.TrimSpace(id)).Scan(
		&itemID, &provider, &phone, &card, &apiURL, &maxBindings, &status, &lease, &note,
		&last, &lastAt, &lastErr, &lastAttempt, &created, &updated); err != nil {
		return nil, errors.New("手机号不存在")
	}
	bindings := s.smsBindingsLocked(itemID)
	masked := card
	if len(card) > 8 {
		masked = card[:4] + "****" + card[len(card)-4:]
	}
	return map[string]any{
		"id": itemID, "provider": provider, "phone_number": phone, "card_code_masked": masked, "api_url": apiURL,
		"max_bindings": maxBindings, "status": status, "lease_expires_at": lease, "note": note,
		"last_code": last, "last_code_at": lastAt, "last_error": lastErr, "last_attempt_at": lastAttempt,
		"created_at": created, "updated_at": updated, "bind_count": len(bindings), "bindings": bindings,
		"state": smsState(status, lease, lastAttempt, maxBindings, len(bindings)),
	}, nil
}
func (s *Store) SaveSMSCode(id, code, lease string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, e := s.db.Exec("UPDATE sms_phones SET last_code=?,last_code_at=?,last_attempt_at=?,lease_expires_at=CASE WHEN ?<>'' THEN ? ELSE lease_expires_at END,updated_at=? WHERE id=?", code, now, now, lease, lease, now, id)
	return e
}
func (s *Store) BindSMSPhone(id, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var phone string
	if e := s.db.QueryRow("SELECT phone_number FROM sms_phones WHERE id=?", id).Scan(&phone); e != nil {
		return errors.New("手机号不存在")
	}
	var max, count int
	if e := s.db.QueryRow("SELECT max_bindings FROM sms_phones WHERE id=?", id).Scan(&max); e != nil {
		return e
	}
	// 一个 GPT 邮箱同一时间只能绑定一个号码，和 gpt-account-manager 一致。
	var existingPhone string
	if e := s.db.QueryRow("SELECT phone_number FROM sms_phone_bindings WHERE gpt_email=? LIMIT 1", strings.ToLower(strings.TrimSpace(email))).Scan(&existingPhone); e == nil {
		return fmt.Errorf("该账号已绑定 %s，请先解绑", existingPhone)
	}
	var latestBound string
	_ = s.db.QueryRow("SELECT bound_at FROM sms_phone_bindings WHERE phone_id=? ORDER BY bound_at DESC LIMIT 1", id).Scan(&latestBound)
	if latestBound != "" {
		if t, e := time.Parse(time.RFC3339Nano, latestBound); e == nil {
			if wait := 5*time.Minute - time.Since(t); wait > 0 {
				return fmt.Errorf("该号码冷却中，还需等待 %d 秒（绑定间隔 5 分钟）", int(wait.Seconds())+1)
			}
		}
	}
	_ = s.db.QueryRow("SELECT COUNT(*) FROM sms_phone_bindings WHERE phone_id=?", id).Scan(&count)
	if count >= max {
		return errors.New("该手机号已达到绑定上限")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, e := s.db.Exec("INSERT OR REPLACE INTO sms_phone_bindings(phone_id,gpt_email,phone_number,bound_at) VALUES(?,?,?,?)", id, strings.ToLower(strings.TrimSpace(email)), phone, now)
	return e
}
func (s *Store) UnbindSMSPhone(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.db.Exec("DELETE FROM sms_phone_bindings WHERE gpt_email=?", strings.ToLower(strings.TrimSpace(email)))
	return e
}
func (s *Store) SMSConfig(provider string) (map[string]any, error) {
	return s.smsConfig(provider, false)
}

// SMSConfigRaw is used only by the server-side provider adapter. It is never
// returned directly through the API because it contains secrets.
func (s *Store) SMSConfigRaw(provider string) (map[string]any, error) {
	return s.smsConfig(provider, true)
}

func (s *Store) smsConfig(provider string, includeSecrets bool) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var enabled int
	var payload string
	e := s.db.QueryRow("SELECT enabled,payload FROM sms_platform_configs WHERE provider=?", provider).Scan(&enabled, &payload)
	if e != nil {
		payload = "{}"
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(payload), &m)
	m["provider"] = provider
	m["enabled"] = enabled == 1
	m["api_key_set"] = strings.TrimSpace(fmt.Sprint(m["api_key"])) != ""
	if raw, ok := m["cdks"].(string); ok {
		count := 0
		for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
		m["cdks_count"] = count
	}
	if !includeSecrets {
		m["api_key"] = ""
	}
	return m, nil
}
func (s *Store) SaveSMSConfig(provider string, m map[string]any, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := map[string]any{}
	var payload string
	if s.db.QueryRow("SELECT payload FROM sms_platform_configs WHERE provider=?", provider).Scan(&payload) == nil {
		_ = json.Unmarshal([]byte(payload), &old)
	}
	for _, k := range []string{"api_key", "base_url", "service", "country", "max_price", "cdks"} {
		if v, ok := m[k]; ok && fmt.Sprint(v) != "" {
			old[k] = v
		}
	}
	raw, _ := json.Marshal(old)
	_, e := s.db.Exec("INSERT INTO sms_platform_configs(provider,enabled,payload,updated_at) VALUES(?,?,?,?) ON CONFLICT(provider) DO UPDATE SET enabled=excluded.enabled,payload=excluded.payload,updated_at=excluded.updated_at", provider, boolInt(enabled), string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
