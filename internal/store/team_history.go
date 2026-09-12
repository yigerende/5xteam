package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

var ErrStaleCycle = errors.New("账号轮次已变更，忽略上一轮结果")

func (s *Store) initializeTeamHistory() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS team_visits (
		user_id TEXT NOT NULL, email TEXT NOT NULL, team_id TEXT NOT NULL, payload TEXT NOT NULL,
		PRIMARY KEY(user_id,team_id), UNIQUE(email,team_id));
		CREATE INDEX IF NOT EXISTS team_visits_email ON team_visits(email);
		CREATE TABLE IF NOT EXISTS team_cycles (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, email TEXT NOT NULL, profile TEXT NOT NULL, updated_at TEXT NOT NULL);
		CREATE INDEX IF NOT EXISTS team_cycles_identity ON team_cycles(email,updated_at DESC);
		CREATE INDEX IF NOT EXISTS team_cycles_user ON team_cycles(user_id,updated_at DESC);`)
	if err != nil {
		return err
	}
	for _, progress := range s.AccountProgress("") {
		if progress.EnteredAt == nil || progress.TeamAccountID == "" || progress.Email == "" {
			continue
		}
		v := model.TeamVisit{TeamAccountID: progress.TeamAccountID, AdminEmail: progress.AdminEmail, EnteredAt: progress.EnteredAt, RemovedAt: progress.RemovedAt, Outcome: progress.LastStatus, Historical: true, CycleID: "legacy-progress"}
		raw, _ := json.Marshal(v)
		if _, err = s.db.Exec(`INSERT INTO team_visits(user_id,email,team_id,payload) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, progress.UserID, strings.ToLower(progress.Email), progress.TeamAccountID, string(raw)); err != nil {
			return err
		}
	}
	for _, p := range s.FreeAccounts() {
		if p.CycleID != "" {
			continue
		}
		_, err = s.UpdateFreeAccount(p.ID, func(item *model.FreeAccountProfile) {
			item.CycleID = "legacy-" + item.ID
			// Earlier versions did not retain a complete workspace ledger.
			item.HistoryUncertain = item.JoinedAt != nil || item.AcceptStatus == "completed" || item.RemoveStatus == "completed"
			if item.RemoveStatus == "completed" {
				item.DownstreamCleaned = true
				item.RemoteRemovedAt = item.RemovedAt
			}
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) syncTeamHistory(tx *sql.Tx, before model.FreeAccountProfile, p *model.FreeAccountProfile) error {
	if p.CycleID == "" {
		p.CycleID = fmt.Sprintf("%s-%d", p.ID, time.Now().UnixNano())
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if p.TeamAccountID != "" && (p.AcceptStatus == "completed" || p.JoinedAt != nil) {
		visit := model.TeamVisit{TeamAccountID: p.TeamAccountID, AdminAccountID: p.AdminAccountID,
			AdminEmail: p.AdminEmail, CycleID: p.CycleID, EnteredAt: p.JoinedAt, RemovedAt: p.RemoteRemovedAt,
			Outcome: p.Status, RemovalReason: p.RemovalReason, Historical: strings.HasPrefix(p.CycleID, "legacy-")}
		if visit.RemovedAt == nil {
			visit.RemovedAt = p.RemovedAt
		}
		var adminRaw string
		var admin model.AdminAccountProfile
		if tx.QueryRow("SELECT profile FROM admin_accounts WHERE id=?", p.AdminAccountID).Scan(&adminRaw) == nil {
			if err := json.Unmarshal([]byte(adminRaw), &admin); err != nil {
				return err
			}
			visit.AdminLabel = admin.Label
		}
		raw, _ := json.Marshal(visit)
		result, err := tx.Exec(`INSERT INTO team_visits(user_id,email,team_id,payload) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, p.UserID, email, p.TeamAccountID, string(raw))
		if err != nil {
			return err
		}
		inserted, _ := result.RowsAffected()
		if inserted > 0 && before.AcceptStatus != "completed" && p.AcceptStatus == "completed" && admin.ID != "" {
			admin.TeamRotationChildCount++
			admin.UpdatedAt = time.Now()
			encoded, _ := json.Marshal(admin)
			if _, err = tx.Exec("UPDATE admin_accounts SET profile=? WHERE id=?", string(encoded), admin.ID); err != nil {
				return err
			}
		}
		// Never replace the first entry timestamp or the original cycle on retries.
		_, err = tx.Exec(`UPDATE team_visits SET payload=json_set(payload,'$.outcome',?,'$.removal_reason',?,
			'$.removed_at',json_extract(?,'$.removed_at')) WHERE (user_id=? OR email=?) AND team_id=? AND json_extract(payload,'$.cycle_id')=?`,
			p.Status, p.RemovalReason, string(raw), p.UserID, email, p.TeamAccountID, p.CycleID)
		if err != nil {
			return err
		}
	}
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT team_id) FROM team_visits WHERE user_id=? OR email=?`, p.UserID, email).Scan(&p.VisitedTeamCount); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO team_cycles(id,user_id,email,profile,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET profile=excluded.profile,updated_at=excluded.updated_at`, p.CycleID, p.UserID, email, string(raw), formatTime(p.UpdatedAt))
	return err
}

func (s *Store) TeamVisits(email, userID string) ([]model.TeamVisit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT payload FROM team_visits WHERE email=? OR (user_id=? AND user_id!='') ORDER BY json_extract(payload,'$.entered_at') DESC`, strings.ToLower(strings.TrimSpace(email)), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.TeamVisit{}
	for rows.Next() {
		var raw string
		var v model.TeamVisit
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) HasVisitedTeam(p model.FreeAccountProfile, teamID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM team_visits WHERE (user_id=? OR email=?) AND team_id=?`, p.UserID, strings.ToLower(strings.TrimSpace(p.Email)), teamID).Scan(&count)
	return count > 0, err
}

func (s *Store) BeginFreeAccountCycle(id, expectedCycle, sourceToken string) (model.FreeAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var p model.FreeAccountProfile
	if err := s.db.QueryRow("SELECT profile FROM free_accounts WHERE id=?", id).Scan(&raw); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	if p.CycleID != expectedCycle {
		return p, ErrStaleCycle
	}
	if p.Dead || p.HistoryUncertain || p.RemoveStatus != "completed" || !p.DownstreamCleaned {
		return p, errors.New("账号未清理完成、历史未确认或已判死号，不能复用")
	}
	before := p
	// Keep identity, original list timestamps, lifetime costs and visit history.
	p = model.FreeAccountProfile{ID: p.ID, Label: p.Label, Email: p.Email, Name: p.Name, UserID: p.UserID,
		PersonalAccountID: p.PersonalAccountID, PlanType: p.PlanType, CreatedAt: p.CreatedAt, ImportedAt: p.ImportedAt,
		VisitedTeamCount: p.VisitedTeamCount, TotalCostUSD: p.TotalCostUSD, TotalUserCostUSD: p.TotalUserCostUSD,
		CostByAdmin: p.CostByAdmin, UserCostByAdmin: p.UserCostByAdmin, CostCheckedAt: p.CostCheckedAt, ReloginCount: p.ReloginCount,
		Status: "imported", InviteStatus: "pending", AcceptStatus: "pending", OAuthStatus: "pending", PushStatus: "pending",
		QuotaStatus: "pending", RemoveStatus: "pending", AutoRemove: true, ExhaustionPolicy: before.ExhaustionPolicy,
		ReusePending: true, SourceTokenPresent: sourceToken != "", UpdatedAt: time.Now()}
	encrypted, err := s.encrypt(sourceToken)
	if err != nil {
		return p, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if err = s.syncTeamHistory(tx, before, &p); err != nil {
		return p, err
	}
	encoded, _ := json.Marshal(p)
	_, err = tx.Exec(`UPDATE free_accounts SET profile=?, encrypted_source_token=?,encrypted_oauth_access_token='',encrypted_oauth_refresh_token='',updated_at=? WHERE id=?`, string(encoded), encrypted, formatTime(p.UpdatedAt), id)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (s *Store) FreeAccountClaim(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var task string
	_ = s.db.QueryRow("SELECT task_id FROM auto_rotation_claims WHERE account_id=?", id).Scan(&task)
	return task
}

// ReviewTeamHistory adds explicitly confirmed legacy visits without inventing dates.
func (s *Store) ReviewTeamHistory(p model.FreeAccountProfile, teamIDs []string) error {
	s.mu.Lock()
	tx, err := s.db.Begin()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	defer tx.Rollback()
	for _, team := range teamIDs {
		team = strings.TrimSpace(team)
		if team == "" {
			continue
		}
		visit := model.TeamVisit{TeamAccountID: team, Outcome: "历史确认", Historical: true, CycleID: "review-" + p.CycleID}
		var raw string
		var admin model.AdminAccountProfile
		if tx.QueryRow(`SELECT profile FROM admin_accounts WHERE json_extract(profile,'$.team_account_id')=? LIMIT 1`, team).Scan(&raw) == nil && json.Unmarshal([]byte(raw), &admin) == nil {
			visit.AdminAccountID = admin.ID
			visit.AdminEmail = admin.Email
			visit.AdminLabel = admin.Label
		}
		encoded, _ := json.Marshal(visit)
		if _, err = tx.Exec(`INSERT INTO team_visits(user_id,email,team_id,payload) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, p.UserID, strings.ToLower(p.Email), team, string(encoded)); err != nil {
			_ = tx.Rollback()
			s.mu.Unlock()
			return err
		}
	}
	err = tx.Commit()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = s.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) { item.HistoryUncertain = false })
	return err
}
