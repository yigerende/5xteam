package store

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *Store) initializeProRestock() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS pro_restock_config(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pro_restock_demand_gate(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL);
 DELETE FROM pro_restock_demand_gate;
 CREATE TABLE IF NOT EXISTS pro_restock_tasks(id TEXT PRIMARY KEY,email TEXT NOT NULL COLLATE NOCASE,status TEXT NOT NULL,created_at TEXT NOT NULL,cost INTEGER NOT NULL,payload TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS pro_restock_account ON pro_restock_tasks(email) WHERE status NOT IN ('ready','failed','cancelled');
 CREATE TABLE IF NOT EXISTS pro_restock_replacements(source TEXT PRIMARY KEY,task_id TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS pro_restock_payment_risk(task_id TEXT PRIMARY KEY,replacement_id TEXT UNIQUE,paused INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS pro_restock_decisions(id TEXT PRIMARY KEY,created_at TEXT NOT NULL,payload TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS pro_restock_decisions_recent ON pro_restock_decisions(created_at DESC,id DESC);
 CREATE TABLE IF NOT EXISTS pro_restock_snapshot(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pro_daily_ejh_merge_runs(day TEXT PRIMARY KEY,payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pro_restock_clock(id INTEGER PRIMARY KEY CHECK(id=1),last_batch TEXT,reset_at TEXT);
 CREATE TABLE IF NOT EXISTS pro_takeover_config(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL,encrypted_password TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pro_takeover_state(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pro_takeover_bindings(target_id TEXT NOT NULL,account_id INTEGER NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(target_id,account_id));
 CREATE TABLE IF NOT EXISTS pro_fallback_config(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL,target_id TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pro_fallback_bindings(target_id TEXT NOT NULL,account_id INTEGER NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(target_id,account_id));`)
	return err
}
func (s *Store) ProRestockConfig() (restock.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRestockConfig(s.db)
}
func readRestockConfig(q proCapacityQuery) (restock.Config, error) {
	c := restock.DefaultConfig()
	var raw string
	err := q.QueryRow(`SELECT payload FROM pro_restock_config WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c)
	}
	if err == nil {
		err = attachRestockDemand(q, &c)
	}
	return c, err
}
func (s *Store) SaveProRestockConfig(c restock.Config) (restock.Config, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := readRestockConfig(s.db)
	if err != nil {
		return c, err
	}
	if old.Version != c.Version {
		return c, errors.New("配置已被其他页面修改，请刷新后重试")
	}
	c.Version++
	_, err = s.db.Exec(`INSERT INTO pro_restock_config VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, mustJSON(c))
	return c, err
}
func (s *Store) ProRestockTasks() ([]restock.Task, error) {
	return s.proRestockTasksWhere("(status NOT IN ('ready','failed','cancelled') AND NOT "+restockReplacementDeliveredSQL+") OR created_at>?1 OR json_extract(payload,'$.updated_at')>?1 OR EXISTS(SELECT 1 FROM pro_restock_replacements WHERE task_id=pro_restock_tasks.id) OR EXISTS(SELECT 1 FROM pro_restock_payment_risk WHERE task_id=pro_restock_tasks.id AND replacement_id IS NULL)", formatTime(time.Now().Add(-48*time.Hour)))
}
func (s *Store) ProRestockDecisionTasks(id string) ([]restock.Task, error) {
	return s.proRestockTasksWhere("json_extract(payload,'$.decision_id')=?", id)
}

const proRestockTaskProjectionSQL = `json_set(payload,'$.force_merge_source',coalesce((SELECT source FROM pro_restock_replacements WHERE task_id=pro_restock_tasks.id),''),'$.payment_risk_pending',json(CASE WHEN EXISTS(SELECT 1 FROM pro_restock_payment_risk WHERE task_id=pro_restock_tasks.id AND replacement_id IS NULL AND paused=0) THEN 'true' ELSE 'false' END),'$.payment_risk_source',coalesce((SELECT task_id FROM pro_restock_payment_risk WHERE replacement_id=pro_restock_tasks.id),''))`

func (s *Store) proRestockTasksWhere(where string, arg any) ([]restock.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT "+proRestockTaskProjectionSQL+",payload FROM pro_restock_tasks WHERE "+where+" ORDER BY created_at", arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []restock.Task{}
	for rows.Next() {
		var raw string
		var t restock.Task
		if err = rows.Scan(&raw, &t.ObservedPayload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) SaveProRestockTask(t restock.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A late worker response must never resurrect an operator-stopped task.
	_, err := s.db.Exec(`UPDATE pro_restock_tasks SET status=?,cost=?,payload=? WHERE id=? AND json_extract(payload,'$.stopped_at') IS NULL`, t.Status, t.Cost, mustJSON(t), t.ID)
	return err
}

// ClaimProRestockDispatch is the durable boundary before any external card or
// payment action. A stale dispatcher cannot start an already claimed intent.
func (s *Store) ClaimProRestockDispatch(t restock.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE pro_restock_config SET payload=payload WHERE id=1`); err != nil {
		return err
	}
	c, err := readRestockConfig(tx)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := takeoverPurchaseGate(tx, t.TakeoverToken, now); err != nil {
		return err
	}
	if reason := c.NoRestockReason(now); reason != "" {
		return errors.New(reason)
	}
	replacementPro, _, pinned, err := readRestockReplacementPurchase(tx, t, c, now)
	if err != nil {
		return err
	}
	var provider string
	var strict bool
	var route *model.ProPurchaseRoute
	if pinned {
		route = replacementPro.PurchaseRoute
		provider, strict = replacementPro.CardProvider, route != nil || c.CardSchedule(now).Provider != ""
	} else {
		provider, strict, err = restockCardProvider(tx, c, now)
		if err != nil {
			return err
		}
		route, err = readProPurchase(tx, c, now)
		if err != nil {
			return err
		}
	}
	if !SameProPurchase(route, t.PurchaseRoute) {
		return errors.New("分时套餐或模板已变化，等待重新评估")
	}
	if t.CardProvider != provider || t.StrictCardProvider != strict {
		return errors.New("补货卡台或时段已变化，等待按当前规则重新派发")
	}
	result, err := tx.Exec(`UPDATE pro_restock_tasks SET status=?,payload=? WHERE id=? AND status='reserved' AND EXISTS(SELECT 1 FROM pro_restock_config WHERE id=1 AND json_extract(payload,'$.version')=? AND json_extract(payload,'$.enabled')=1 AND json_extract(payload,'$.mode')='auto')`, t.Status, mustJSON(t), t.ID, t.Version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("补货意图已被领取、撤销或配置已变化")
	}
	return tx.Commit()
}
func restockLimits(q proCapacityQuery, now time.Time) (restock.Limits, error) {
	var l restock.Limits
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	hour := now.Add(-time.Hour)
	err := q.QueryRow(`SELECT coalesce(sum(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN created_at>=? OR status NOT IN ('ready','failed','cancelled') THEN cost ELSE 0 END),0),coalesce(sum(CASE WHEN created_at>=? OR status NOT IN ('ready','failed','cancelled') THEN cost ELSE 0 END),0) FROM pro_restock_tasks WHERE (status!='cancelled' OR (json_extract(payload,'$.stopped_at') IS NOT NULL AND json_extract(payload,'$.submitted_at') IS NOT NULL)) AND coalesce(json_extract(payload,'$.managed_externally'),0)=0`, formatTime(hour), formatTime(day), formatTime(hour), formatTime(day)).Scan(&l.HourCount, &l.DayCount, &l.HourCost, &l.DayCost)
	if err != nil {
		return l, err
	}
	// Consecutive failures since the last ready task or explicit breaker reset.
	err = q.QueryRow(`SELECT count(*) FROM pro_restock_tasks WHERE coalesce(json_extract(payload,'$.stage'),'')<>'dead' AND NOT EXISTS(SELECT 1 FROM pro_restock_payment_risk WHERE task_id=pro_restock_tasks.id) AND NOT EXISTS(SELECT 1 FROM mail_accounts m WHERE m.email=pro_restock_tasks.email AND (json_extract(m.profile,'$.pro_auto.replacement.pending')=1 OR coalesce(json_extract(m.profile,'$.pro_auto.replacement.next_email'),'')<>'')) AND (status IN ('failed','attention','unknown') OR json_extract(payload,'$.stopped_failure')=1) AND created_at>coalesce((SELECT max(created_at) FROM pro_restock_tasks WHERE status='ready'),'') AND created_at>coalesce((SELECT reset_at FROM pro_restock_clock WHERE id=1),'')`).Scan(&l.Failures)
	if err != nil {
		return l, err
	}
	var raw sql.NullString
	err = q.QueryRow(`SELECT last_batch FROM pro_restock_clock WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err == nil && raw.Valid {
		t, e := time.Parse(time.RFC3339Nano, raw.String)
		if e == nil {
			l.LastBatch = &t
		}
	}
	return l, err
}
func (s *Store) ProRestockLimits(now time.Time) (restock.Limits, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return restockLimits(s.db, now)
}
func (s *Store) ResetProRestockBreaker() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.db.Exec(`INSERT INTO pro_restock_clock(id,reset_at) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET reset_at=excluded.reset_at`, formatTime(time.Now()))
	return e
}

// ReserveProRestock atomically claims fresh accounts and budget. External card
// issuance and payment are deliberately outside this transaction and retain
// their existing durable intent/idempotency mechanisms.
func (s *Store) ReserveProRestock(d restock.Decision, target string, existingInflight int) ([]restock.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE pro_restock_config SET payload=payload WHERE id=1`); err != nil {
		return nil, err
	}
	c, err := readRestockConfig(tx)
	if err != nil {
		return nil, err
	}
	if !c.Enabled || c.Mode != "auto" || c.Version != d.Version {
		return nil, errors.New("配置版本或运行模式已变化")
	}
	var replacing int
	if err = tx.QueryRow(`SELECT count(*) FROM mail_accounts WHERE json_extract(profile,'$.pro_auto.replacement.pending')=1 AND coalesce(json_extract(profile,'$.pro_auto.stop_requested'),0)=0`).Scan(&replacing); err != nil {
		return nil, err
	}
	if replacing > 0 {
		return nil, errors.New("第二步失败换号尚在领取新账号，等待原批次续跑")
	}
	// Live evaluations can wait for the store/admission lock. Recheck their
	// real sample time at the reservation boundary, without renewing its age.
	if sample := d.Snapshot; d.DemandKnown && (!sample.Valid || !sample.LoadValid || sample.At.IsZero() || time.Since(sample.At) > time.Duration(c.FreshSeconds)*time.Second || sample.At.After(time.Now().Add(5*time.Second))) {
		return nil, errors.New("事务复核：监控快照过期或不完整，等待重新采集")
	}
	if err := takeoverPurchaseGate(tx, d.Snapshot.TakeoverToken, time.Now()); err != nil {
		return nil, err
	}
	// Recheck wall time after acquiring the transaction; the evaluation may
	// have completed just before the no-restock interval started.
	if reason := c.NoRestockReason(time.Now()); reason != "" {
		return nil, errors.New(reason)
	}
	provider, strict, err := restockCardProvider(tx, c, time.Now())
	if err != nil {
		return nil, err
	}
	route, err := readProPurchase(tx, c, time.Now())
	if err != nil {
		return nil, err
	}
	if route != nil {
		plan := fmt.Sprintf("%s:%s:%s:%d", route.PaymentProvider, route.PlanCode, route.Country, route.CreditQuantity)
		if plan != d.Snapshot.NewPlan {
			return nil, errors.New("分时套餐已变化，等待刷新监控后重新评估")
		}
	}
	d.CardProvider, d.CardSchedule = provider, c.CardSchedule(time.Now())
	l, err := restockLimits(tx, d.At)
	if err != nil {
		return nil, err
	}
	var active int
	if err = tx.QueryRow(`SELECT count(*) FROM pro_restock_tasks WHERE status NOT IN ('ready','failed','cancelled') AND NOT ` + restockReplacementDeliveredSQL).Scan(&active); err != nil {
		return nil, err
	}
	if active > 0 || existingInflight > 0 {
		return nil, errors.New("上一批仍有在途任务，全部结束并刷新监控后才能新增")
	}
	// Recheck the refresh barrier under the reservation transaction, including
	// confirmed failures and cancellations. Restart or an obsolete decision
	// must not bypass the final monitoring round.
	var completedJSON string
	if err = tx.QueryRow(`SELECT coalesce(json_group_array(json(payload)),'[]') FROM pro_restock_tasks WHERE (status IN ('ready','failed','cancelled') OR `+restockReplacementDeliveredSQL+`) AND json_extract(payload,'$.binding')=? AND json_extract(payload,'$.group_id')=?`, d.Snapshot.Binding, d.Snapshot.GroupID).Scan(&completedJSON); err != nil {
		return nil, err
	}
	var completed []restock.Task
	if err = json.Unmarshal([]byte(completedJSON), &completed); err != nil {
		return nil, err
	}
	if wait := restock.WaitForBatch(d.Snapshot, completed); wait != nil {
		return nil, errors.New(wait.Message)
	}
	persisted, err := replacementTasks(tx)
	if err != nil {
		return nil, err
	}
	stats := restock.ForceMergeSummary(c, d.Snapshot, persisted, d.At)
	riskSource, err := pendingPaymentRisk(tx, d.Snapshot)
	if err != nil {
		return nil, err
	}
	if d.ForceMergeNeed > 0 && riskSource == "" {
		need := max(d.TimeNeed, d.ConcurrencyNeed, d.ReserveNeed, d.EmptyGroupNeed, d.ExitNeed, len(stats.Sources))
		if need == 0 {
			return nil, errors.New("接替已由原任务覆盖，未重复预留")
		}
		if !c.AllowSmall && !d.EmptyGroupOnly() {
			need = max(need, c.MinBatch)
		}
		d.Suggested = min(d.Suggested, need)
	}
	n := min(d.Suggested, c.MaxBatch, c.MaxInflight-max(active, existingInflight), c.HourLimit-l.HourCount, c.DayLimit-l.DayCount, c.PoolLimit-len(d.Snapshot.Accounts)-existingInflight)
	if riskSource != "" {
		n = min(n, 1)
	}
	if c.HourBudget > 0 {
		n = min(n, int(max(int64(0), c.HourBudget-l.HourCost)/max(int64(1), c.UnitCost)))
	}
	if c.DayBudget > 0 {
		n = min(n, int(max(int64(0), c.DayBudget-l.DayCost)/max(int64(1), c.UnitCost)))
	}
	cooldown := c.CooldownSeconds
	if d.Waiting >= c.EmergencyQueue || (c.ExitEnabled && d.ExitNeed > 0 && d.Exit != nil && d.Exit.Emergency) {
		cooldown = c.EmergencyCooldownSeconds
	}
	if l.Failures >= c.FailureLimit || (l.LastBatch != nil && d.At.Sub(*l.LastBatch) < time.Duration(cooldown)*time.Second) {
		n = 0
	}
	strictMinimum := !c.AllowSmall && !d.EmptyGroupOnly() && riskSource == ""
	if n < 1 || (strictMinimum && n < c.MinBatch) {
		return nil, errors.New("事务复核：冷却、在途或预算不允许新增")
	}
	rows, err := tx.Query(proScheduleCandidateSQL, n)
	if err != nil {
		return nil, err
	}
	emails := []string{}
	for rows.Next() {
		var email string
		if err = rows.Scan(&email); err != nil {
			break
		}
		emails = append(emails, email)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(emails) == 0 || (strictMinimum && len(emails) < c.MinBatch) {
		return nil, errors.New("可领取的新账号不足批量要求")
	}
	out := []restock.Task{}
	for i, email := range emails {
		t := restock.Task{ID: fmt.Sprintf("%s-%d", d.ID, i+1), Plan: d.Snapshot.NewPlan, DecisionID: d.ID, Email: email, Version: c.Version, GroupID: c.GroupID, Binding: d.Snapshot.Binding, Status: "reserved", Stage: "queued", Cost: c.UnitCost, CreatedAt: d.At, UpdatedAt: d.At, Stages: map[string]time.Time{"reserved": d.At}}
		t.TakeoverToken = d.Snapshot.TakeoverToken
		t.PaymentRiskSource = riskSource
		if riskSource != "" {
			t.Message = "支付风控后的一次替补；若本账号再次支付风控，将暂停自动补货"
			if _, err = tx.Exec(`UPDATE pro_restock_payment_risk SET replacement_id=? WHERE task_id=? AND replacement_id IS NULL`, t.ID, riskSource); err != nil {
				return nil, err
			}
		}
		t.EmptyGroupStartup = d.EmptyGroupOnly()
		t.CardProvider, t.StrictCardProvider, t.PurchaseRoute = provider, strict, route
		t.ForceMergeTracked = true
		if d.Trace != nil {
			t.Capacity = d.Trace.NewAccountCapacity
		}
		if i < len(stats.Sources) {
			t.ForceMergeSource, t.ForceMergeAssigned = stats.Sources[i], true
		}
		var raw string
		var p model.MailAccountProfile
		if err = tx.QueryRow(`SELECT profile FROM mail_accounts WHERE email=?`, email).Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		p.ProRestockTaskID = t.ID
		p.ProRestockTargetID = target
		if _, err = tx.Exec(`UPDATE mail_accounts SET profile=? WHERE email=?`, mustJSON(p), email); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, t.ID, email, t.Status, formatTime(t.CreatedAt), t.Cost, mustJSON(t)); err != nil {
			return nil, err
		}
		if i < len(stats.Sources) {
			if _, err = tx.Exec("INSERT INTO pro_restock_replacements(source,task_id) VALUES(?,?)", t.ForceMergeSource, t.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, t)
	}
	if _, err = tx.Exec(`INSERT INTO pro_restock_clock(id,last_batch) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET last_batch=excluded.last_batch`, formatTime(d.At)); err != nil {
		return nil, err
	}
	d.Started = len(out)
	if _, err = tx.Exec(`INSERT INTO pro_restock_decisions VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, d.ID, formatTime(d.At), mustJSON(d)); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) CancelProRestock(t restock.Task, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var raw string
	if e = tx.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=? AND status='reserved'`, t.ID).Scan(&raw); e != nil {
		return e
	}
	if e = json.Unmarshal([]byte(raw), &t); e != nil {
		return e
	}
	t.Status = "cancelled"
	t.Cost = 0
	t.Message = reason
	t.UpdatedAt = time.Now()
	// A cancelled successor must not become a manual queued start after its
	// restock claim is detached by the legacy cancellation cleanup below.
	if _, e = tx.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_auto.replacement.queued',json('false'),'$.pro_auto.replacement.message',?,'$.pro_auto.stop_requested',json('true'),'$.pro_auto.stop_version',coalesce(json_extract(profile,'$.pro_auto.stop_version'),0)+1,'$.pro_auto.status','stopped') WHERE email=? AND json_extract(profile,'$.pro_auto.replacement.queued')=1`, reason, t.Email); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE pro_restock_tasks SET status='cancelled',cost=0,payload=? WHERE id=?`, mustJSON(t), t.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE mail_accounts SET profile=json_remove(profile,'$.pro_restock_task_id','$.pro_restock_target_id') WHERE email=? AND json_extract(profile,'$.pro_restock_task_id')=? AND coalesce(json_extract(profile,'$.pro_auto.id'),'')=''`, t.Email, t.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) ProRestockSnapshot() (restock.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v restock.Snapshot
	var raw string
	e := s.db.QueryRow(`SELECT payload FROM pro_restock_snapshot WHERE id=1`).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e == nil {
		e = json.Unmarshal([]byte(raw), &v)
	}
	return v, e
}
func (s *Store) SaveProRestockSnapshot(v restock.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.db.Exec(`INSERT INTO pro_restock_snapshot VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, mustJSON(v))
	return e
}
func (s *Store) SaveProRestockDecision(d restock.Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.db.Exec(`INSERT INTO pro_restock_decisions VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, d.ID, formatTime(d.At), mustJSON(d))
	return e
}
func (s *Store) ProRestockDecisions(limit, offset int) ([]restock.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, e := s.db.Query(`SELECT payload FROM pro_restock_decisions ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, min(500, max(1, limit)), max(0, offset))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []restock.Decision{}
	for rows.Next() {
		var raw string
		var d restock.Decision
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) ProRestockDecisionCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pro_restock_decisions`).Scan(&total)
	return total, err
}

func (s *Store) PurgeProRestock(days int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.db.Exec(`DELETE FROM pro_restock_decisions WHERE created_at<?`, formatTime(time.Now().Add(-time.Duration(days)*24*time.Hour)))
	return e
}
