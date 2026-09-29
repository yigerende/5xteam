package store

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

func (s *Store) initializeSeatRecovery() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS seat_recovery_settings (id INTEGER PRIMARY KEY CHECK(id=1), payload TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS seat_recovery_tasks (id TEXT PRIMARY KEY, email TEXT NOT NULL, team_id TEXT NOT NULL, finished INTEGER NOT NULL, lane INTEGER NOT NULL, payload TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE UNIQUE INDEX IF NOT EXISTS seat_recovery_active_email ON seat_recovery_tasks(email) WHERE finished=0;
	CREATE UNIQUE INDEX IF NOT EXISTS seat_recovery_lane ON seat_recovery_tasks(team_id) WHERE lane=1;
	CREATE INDEX IF NOT EXISTS seat_recovery_active ON seat_recovery_tasks(finished,updated_at);
	CREATE TABLE IF NOT EXISTS seat_recovery_entry_lanes (team_id TEXT PRIMARY KEY, task_id TEXT NOT NULL UNIQUE);
	CREATE TABLE IF NOT EXISTS seat_recovery_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, payload TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS seat_recovery_logs_task ON seat_recovery_logs(task_id,id);
	`)
	return err
}

func (s *Store) SeatRecoverySettings() (model.SeatRecoverySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := model.DefaultSeatRecoverySettings()
	var raw string
	rows, err := s.db.Query("SELECT payload FROM seat_recovery_settings WHERE id=1")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	if rows.Next() {
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &out)
		}
	}
	return out, errors.Join(err, rows.Err())
}

func (s *Store) SaveSeatRecoverySettings(v model.SeatRecoverySettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("INSERT INTO seat_recovery_settings(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", mustJSON(v))
	return err
}

func (s *Store) SeatRecoveryTasks(activeOnly bool, limit, offset int) ([]model.SeatRecoveryTask, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	if activeOnly {
		where = " WHERE finished=0"
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM seat_recovery_tasks" + where).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query("SELECT payload FROM seat_recovery_tasks"+where+" ORDER BY lane DESC,finished,updated_at,id LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.SeatRecoveryTask{}
	for rows.Next() {
		var raw string
		var p model.SeatRecoveryTask
		if err = rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *Store) SeatRecoveryTask(id string) (model.SeatRecoveryTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var p model.SeatRecoveryTask
	err := s.db.QueryRow("SELECT payload FROM seat_recovery_tasks WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	return p, err
}

func (s *Store) CreateSeatRecoveryTask(p model.SeatRecoveryTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	eligible, err := s.seatRecoveryMailEligibleLocked(p.Email)
	if err != nil {
		return err
	}
	if !eligible {
		return errors.New("子号不在邮件管理的未进入空间或已使用过账号中")
	}
	// Admission and Team claim/cycle creation use the same store lock. Whichever
	// claims this email first wins; the other workflow must not move the child.
	var claims int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM auto_rotation_claims c JOIN free_accounts f ON f.id=c.account_id WHERE LOWER(json_extract(f.profile,'$.email'))=?`, p.Email).Scan(&claims); err != nil {
		return err
	}
	if claims > 0 {
		return errors.New("该子号已被 Team 轮转预定")
	}
	var current string
	rows, err := s.db.Query(`SELECT profile FROM free_accounts WHERE LOWER(COALESCE(json_extract(profile,'$.email'),''))=? ORDER BY updated_at DESC,id DESC LIMIT 1`, p.Email)
	if err != nil {
		return err
	}
	if rows.Next() {
		err = rows.Scan(&current)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if current != "" {
		var free model.FreeAccountProfile
		if err = json.Unmarshal([]byte(current), &free); err != nil {
			return err
		}
		if free.ID != p.FreeID || free.CycleID != p.FreeCycle || SeatRecoveryTeamInFlight(free) || free.Dead {
			return errors.New("子号的 Team 状态已改变，未创建恢复任务")
		}
	} else if p.FreeID != "" {
		return errors.New("子号的 Team 记录已改变，未创建恢复任务")
	}
	_, err = s.db.Exec("INSERT INTO seat_recovery_tasks(id,email,team_id,finished,lane,payload,updated_at) VALUES(?,?,?,0,0,?,?)", p.ID, p.Email, p.TeamID, mustJSON(p), formatTime(p.UpdatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return errors.New("该子号已有未结束的席位恢复任务")
	}
	return err
}

// A reservation concerns only this child. Team seat totals and scheduling rules
// remain unchanged, and finished recovery tasks immediately release the email.
func (s *Store) seatRecoveryAvailableLocked(email string) error {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM seat_recovery_tasks WHERE email=? AND finished=0", strings.ToLower(strings.TrimSpace(email))).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("子号正在独立席位恢复中，请完成恢复后再轮转")
	}
	return nil
}

func (s *Store) SeatRecoveryAvailable(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seatRecoveryAvailableLocked(email)
}

// Waiting outside accounts can be recovered. A request already sent/running
// belongs to Team until it is removed; an old completed cycle is not in flight.
func SeatRecoveryTeamInFlight(p model.FreeAccountProfile) bool {
	return p.ID != "" && p.RemoveStatus != "completed" && p.RemoteRemovedAt == nil &&
		(p.InviteStatus == "running" || p.InviteStatus == "completed" || p.AcceptStatus == "running" || p.AcceptStatus == "completed")
}

// Task updates preserve control flags changed by the UI while a network call runs.
func (s *Store) UpdateSeatRecoveryTask(id string, mutate func(*model.SeatRecoveryTask)) (model.SeatRecoveryTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p model.SeatRecoveryTask
	var raw string
	if err := s.db.QueryRow("SELECT payload FROM seat_recovery_tasks WHERE id=?", id).Scan(&raw); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	mutate(&p)
	p.UpdatedAt = time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE seat_recovery_tasks SET finished=?,lane=?,payload=?,updated_at=? WHERE id=?", p.Finished, p.Lane, mustJSON(p), formatTime(p.UpdatedAt), id); err != nil {
		return p, err
	}
	if p.Finished || (p.Stage != "join" && p.Stage != "confirm") {
		if _, err = tx.Exec("DELETE FROM seat_recovery_entry_lanes WHERE task_id=?", id); err != nil {
			return p, err
		}
	}
	return p, tx.Commit()
}

// A whole entry (request/invite -> approval/accept -> confirmed membership) owns
// this lane across requests and restarts. Preparation and dwell never own it.
func (s *Store) ClaimSeatRecoveryEntry(p model.SeatRecoveryTask) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var owner string
	rows, err := s.db.Query("SELECT task_id FROM seat_recovery_entry_lanes WHERE team_id=?", p.TeamID)
	if err != nil {
		return false, err
	}
	if rows.Next() {
		err = rows.Scan(&owner)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if readErr != nil {
		return false, readErr
	}
	if owner != "" {
		return owner == p.ID, nil
	}
	// Older versions may have sent multiple entries before approval. Drain an
	// existing in-flight entry first; never submit a new one ahead of it.
	rows, err = s.db.Query(`SELECT id FROM seat_recovery_tasks WHERE team_id=? AND finished=0
	AND json_extract(payload,'$.stage') IN ('join','confirm')
	AND (json_extract(payload,'$.join_sent')=1 OR json_extract(payload,'$.stage')='confirm')
	ORDER BY json_extract(payload,'$.created_at'),id LIMIT 1`, p.TeamID)
	if err != nil {
		return false, err
	}
	if rows.Next() {
		err = rows.Scan(&owner)
	}
	readErr = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if readErr != nil {
		return false, readErr
	}
	if owner != "" && owner != p.ID {
		return false, nil
	}
	_, err = s.db.Exec("INSERT INTO seat_recovery_entry_lanes(team_id,task_id) VALUES(?,?)", p.TeamID, p.ID)
	return err == nil, err
}

func (s *Store) AppendSeatRecoveryLog(id, stage, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("INSERT INTO seat_recovery_logs(task_id,payload) VALUES(?,?)", id, mustJSON(model.SeatRecoveryLog{At: time.Now(), Stage: stage, Message: message}))
	return err
}

func (s *Store) SeatRecoveryLogs(id string, before int64) ([]model.SeatRecoveryLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.db.Query("SELECT id,payload FROM seat_recovery_logs WHERE task_id=? AND id<? ORDER BY id DESC LIMIT 200", id, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SeatRecoveryLog{}
	for rows.Next() {
		var raw string
		var n int64
		var p model.SeatRecoveryLog
		if err = rows.Scan(&n, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		p.ID = n
		out = append(out, p)
	}
	return out, rows.Err()
}

// Match both outside and used mail accounts, excluding dead and currently inside.
func (s *Store) SeatRecoveryMailEligible(email string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seatRecoveryMailEligibleLocked(email)
}

func (s *Store) seatRecoveryMailEligibleLocked(email string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mail_accounts m
	LEFT JOIN free_accounts p ON p.id=(SELECT id FROM free_accounts WHERE LOWER(COALESCE(json_extract(profile,'$.email'),''))=LOWER(m.email) ORDER BY updated_at DESC,id DESC LIMIT 1)
	WHERE m.email=? AND LOWER(COALESCE(json_extract(m.profile,'$.management_scope'),''))!='pro'
	AND COALESCE(json_extract(p.profile,'$.dead'),0)!=1
	AND COALESCE(json_extract(m.profile,'$.chatgpt_status'),'')!='dead'
	AND COALESCE(json_extract(m.profile,'$.registration_status'),'')!='dead'
	AND (json_extract(p.profile,'$.remote_removed_at') IS NOT NULL OR json_extract(p.profile,'$.remove_status')='completed'
	OR COALESCE(json_extract(p.profile,'$.accept_status'),'')!='completed')`, strings.ToLower(strings.TrimSpace(email))).Scan(&count)
	return count == 1, err
}

// Persist only the refreshed AT/session; do not reset OAuth/Pro/Team state.
func (s *Store) SaveSeatRecoveryLogin(email, at, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile,encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&raw, &encrypted); err != nil {
		return err
	}
	var p model.MailAccountProfile
	var c model.MailAccountCredentials
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	plain, err := s.decrypt(encrypted)
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(plain), &c); err != nil {
		return err
	}
	c.AccessToken, c.ChatGPTSession = at, session
	p = mailProfileWithCredentials(p, c)
	now := time.Now()
	p.ATValid, p.ATCheckedAt, p.ATCheckHTTPStatus, p.ATCheckMessage = true, &now, 200, "席位恢复：临时 AT 登录成功"
	p.UpdatedAt = now
	sealed, err := s.encrypt(mustJSON(c))
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?,encrypted_credentials=?,updated_at=? WHERE email=?", mustJSON(p), sealed, formatTime(now), email)
	return err
}
