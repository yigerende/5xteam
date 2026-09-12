package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"chatgpt-space-merge/internal/model"
)

type MailAccountsPageResult struct {
	Items       []model.MailAccountProfile
	Pipelines   []model.FreeAccountProfile
	Total       int
	Counts      map[string]int
	SpaceCounts map[string]int
}

type FreeAccountsPageSummary struct {
	All                   int                       `json:"all"`
	Outside               int                       `json:"outside"`
	Inside                int                       `json:"inside"`
	Removed               int                       `json:"removed"`
	Dead                  int                       `json:"dead"`
	OAuthReady            int                       `json:"oauth_ready"`
	Monitoring            int                       `json:"monitoring"`
	InsidePremium         int                       `json:"inside_premium"`
	Quota7DRemainingTotal float64                   `json:"quota_7d_remaining_total"`
	Quota7DCount          int                       `json:"quota_7d_count"`
	OldestStatusCheckedAt string                    `json:"oldest_status_checked_at,omitempty"`
	OldestQuotaCheckedAt  string                    `json:"oldest_quota_checked_at,omitempty"`
	PendingSeatsByAdmin   map[string]map[string]int `json:"pending_seats_by_admin"`
	InvitePending         int                       `json:"invite_pending"`
}

type ProAccountsPageSummary struct {
	All        int `json:"all"`
	OAuthReady int `json:"oauth_ready"`
	Pushed     int `json:"pushed"`
	Merged     int `json:"merged"`
}

type AdminAccountsPageSummary struct {
	All               int     `json:"all"`
	ChildEntries      int     `json:"child_entries"`
	ChildTotalCostUSD float64 `json:"child_total_cost_usd"`
}

type HistoryPageSummary struct {
	All       int `json:"all"`
	Completed int `json:"completed"`
	Partial   int `json:"partial"`
	Accounts  int `json:"accounts"`
}

const mailAccountPageCTE = `
WITH pipeline_sources AS (
	SELECT id,profile,updated_at,1 AS active FROM free_accounts
), latest_pipeline AS (
	SELECT profile,
		LOWER(COALESCE(json_extract(profile, '$.email'), '')) AS pipeline_email,
		ROW_NUMBER() OVER (
			PARTITION BY LOWER(COALESCE(json_extract(profile, '$.email'), ''))
			ORDER BY active DESC, updated_at DESC, id DESC
		) AS row_number
	FROM pipeline_sources
), visits AS (
	SELECT email,COUNT(DISTINCT team_id) AS visited FROM team_visits GROUP BY email
), joined AS (
	SELECT json_set(m.profile,'$.visited_team_count',COALESCE(v.visited,0),'$.history_uncertain',json(CASE WHEN COALESCE(json_extract(p.profile,'$.history_uncertain'),0)=1 THEN 'true' ELSE 'false' END)) AS profile, m.encrypted_credentials, m.updated_at, p.profile AS pipeline_profile,
		CASE WHEN LOWER(COALESCE(json_extract(m.profile, '$.management_scope'), '')) = 'pro' THEN 'pro' ELSE 'mail' END AS management_scope,
		CASE
			WHEN COALESCE(json_extract(p.profile,'$.dead'),0)=1 OR json_extract(m.profile,'$.chatgpt_status')='dead' OR json_extract(m.profile,'$.registration_status')='dead' THEN 'dead'
			WHEN json_extract(p.profile, '$.remote_removed_at') IS NOT NULL THEN 'removed'
			WHEN json_extract(p.profile, '$.remove_status') = 'completed' THEN 'removed'
			WHEN json_extract(p.profile, '$.accept_status') = 'completed' THEN 'inside'
			WHEN COALESCE(v.visited,0)>0 THEN 'removed'
			ELSE 'outside'
		END AS space_state,
		LOWER(m.email || ' ' || m.label || ' ' || COALESCE(json_extract(m.profile, '$.login_method'), '')) AS search_text,
		COALESCE(NULLIF(json_extract(m.profile, '$.created_at'), ''), m.updated_at) AS entered_at,
		LOWER(COALESCE(json_extract(m.profile, '$.login_method'), 'mailtoken')) AS login_method
	FROM mail_accounts m
	LEFT JOIN latest_pipeline p ON p.pipeline_email = LOWER(m.email) AND p.row_number = 1
	LEFT JOIN visits v ON v.email=LOWER(m.email)
)
`

func (s *Store) MailOutsideInvalidATEmails() ([]string, error) {
	return s.SelectOutsideMailAccounts(MailAccountSelection{ATStatus: "invalid"})
}

type MailAccountSelection struct {
	ATStatus        string   `json:"at_status"`
	RequireRT       bool     `json:"require_rt"`
	RequirePassword bool     `json:"require_password"`
	RequireTOTP     bool     `json:"require_totp"`
	RequireDead     bool     `json:"require_dead"`
	Emails          []string `json:"-"`
}

// A nil email list selects across all pages; an empty list selects nothing.
func (s *Store) SelectOutsideMailAccounts(filter MailAccountSelection) ([]string, error) {
	if filter.ATStatus != "" && filter.ATStatus != "valid" && filter.ATStatus != "invalid" && filter.ATStatus != "not_logged_in" {
		return nil, fmt.Errorf("状态只能为不限、AT 有效、AT 无效或 ChatGPT 未登录")
	}
	if filter.Emails != nil && len(filter.Emails) == 0 {
		return []string{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	query := mailAccountPageCTE + `
		SELECT LOWER(TRIM(json_extract(profile,'$.email'))) FROM joined
		WHERE management_scope='mail'`
	if filter.RequireDead {
		query += ` AND (COALESCE(json_extract(profile,'$.chatgpt_status'),'')='dead'
			OR COALESCE(json_extract(profile,'$.registration_status'),'')='dead'
			OR COALESCE(json_extract(pipeline_profile,'$.dead'),0)=1)`
	} else {
		query += ` AND space_state='outside'
			AND COALESCE(json_extract(profile,'$.chatgpt_status'),'')!='dead'
			AND COALESCE(json_extract(profile,'$.registration_status'),'')!='dead'
			AND COALESCE(json_extract(pipeline_profile,'$.dead'),0)=0`
	}
	args := []any{}
	if filter.ATStatus == "not_logged_in" {
		// Match the mail list's persisted "not logged in" status, not an invalid AT.
		query += ` AND COALESCE(json_extract(profile,'$.at_checked_at'),'')=''
			AND COALESCE(json_extract(profile,'$.access_token_present'),0)=0
			AND COALESCE(json_extract(profile,'$.auth_type'),'') NOT IN ('AT','RT')`
	} else if filter.ATStatus != "" {
		query += ` AND COALESCE(json_extract(profile,'$.at_checked_at'),'')!='' AND COALESCE(json_extract(profile,'$.at_valid'),0)=?`
		args = append(args, filter.ATStatus == "valid")
	}
	for _, requirement := range []struct {
		enabled bool
		field   string
	}{
		{filter.RequireRT, "refresh_token_present"},
		{filter.RequirePassword, "gpt_password_present"},
		{filter.RequireTOTP, "totp_secret_present"},
	} {
		if requirement.enabled {
			query += ` AND COALESCE(json_extract(profile,?),0)=1`
			args = append(args, "$."+requirement.field)
		}
	}
	if filter.Emails != nil {
		encoded, err := json.Marshal(filter.Emails)
		if err != nil {
			return nil, err
		}
		query += ` AND LOWER(TRIM(json_extract(profile,'$.email'))) IN (SELECT LOWER(TRIM(value)) FROM json_each(?))`
		args = append(args, string(encoded))
	}
	query += ` ORDER BY entered_at DESC, LOWER(json_extract(profile,'$.email'))`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	emails := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

func (s *Store) MailAccountsPage(scope, query, spaceState string, limit, offset int) (MailAccountsPageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope = normalizeMailManagementScope(scope)
	query = strings.ToLower(strings.TrimSpace(query))
	spaceState = strings.ToLower(strings.TrimSpace(spaceState))
	if spaceState != "outside" && spaceState != "inside" && spaceState != "removed" && spaceState != "dead" {
		spaceState = ""
	}
	limit, offset = normalizeLimitOffset(limit, offset)
	result := MailAccountsPageResult{
		Items: []model.MailAccountProfile{}, Pipelines: []model.FreeAccountProfile{},
		Counts: map[string]int{}, SpaceCounts: map[string]int{},
	}

	like := "%" + query + "%"
	filteredWhere := ` WHERE management_scope=? AND (?='' OR search_text LIKE ?) AND (?='' OR space_state=?)`
	if err := s.db.QueryRow(mailAccountPageCTE+`SELECT COUNT(*) FROM joined`+filteredWhere,
		scope, query, like, spaceState, spaceState).Scan(&result.Total); err != nil {
		return result, err
	}
	var all, outside, inside, removed, dead, outlook, mailcom, totp, mailtoken, directurl, none int
	if err := s.db.QueryRow(mailAccountPageCTE+`
		SELECT COUNT(*),
			COALESCE(SUM(space_state='outside'),0), COALESCE(SUM(space_state='inside'),0), COALESCE(SUM(space_state='removed'),0),
			COALESCE(SUM(space_state='dead'),0),
			COALESCE(SUM(login_method='outlook'),0), COALESCE(SUM(login_method='mailcom'),0), COALESCE(SUM(login_method='totp'),0),
			COALESCE(SUM(login_method='mailtoken'),0), COALESCE(SUM(login_method='directurl'),0),
			COALESCE(SUM(login_method NOT IN ('outlook','mailcom','totp','mailtoken','directurl')),0)
		FROM joined WHERE management_scope=?`, scope).Scan(&all, &outside, &inside, &removed, &dead, &outlook, &mailcom, &totp, &mailtoken, &directurl, &none); err != nil {
		return result, err
	}
	result.Counts = map[string]int{"all": all, "outlook": outlook, "mailcom": mailcom, "totp": totp, "mailtoken": mailtoken, "directurl": directurl, "none": none}
	result.SpaceCounts = map[string]int{"outside": outside, "inside": inside, "removed": removed, "dead": dead}

	rows, err := s.db.Query(mailAccountPageCTE+`
		SELECT profile, encrypted_credentials, updated_at, COALESCE(pipeline_profile,'') FROM joined`+filteredWhere+`
		ORDER BY CASE space_state WHEN 'outside' THEN 0 WHEN 'inside' THEN 1 ELSE 2 END, entered_at DESC, LOWER(COALESCE(json_extract(profile,'$.email'),''))
		LIMIT ? OFFSET ?`, scope, query, like, spaceState, spaceState, limit, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, encrypted, updatedAt, pipelineRaw string
		var profile model.MailAccountProfile
		if err := rows.Scan(&raw, &encrypted, &updatedAt, &pipelineRaw); err != nil {
			return result, err
		}
		if json.Unmarshal([]byte(raw), &profile) != nil {
			continue
		}
		if profile.CreatedAt.IsZero() {
			profile.CreatedAt, _ = parseTime(updatedAt)
		}
		if plain, decryptErr := s.decrypt(encrypted); decryptErr == nil {
			var credentials model.MailAccountCredentials
			if json.Unmarshal([]byte(plain), &credentials) == nil {
				profile = mailProfileWithCredentials(profile, credentials)
			}
		}
		result.Items = append(result.Items, profile)
		if pipelineRaw != "" {
			var pipeline model.FreeAccountProfile
			if json.Unmarshal([]byte(pipelineRaw), &pipeline) == nil {
				result.Pipelines = append(result.Pipelines, pipeline)
			}
		}
	}
	return result, rows.Err()
}

func (s *Store) MailMessagesPage(query, mailType string, limit, offset int) ([]model.MailMessage, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query = strings.ToLower(strings.TrimSpace(query))
	mailType = strings.ToLower(strings.TrimSpace(mailType))
	if mailType == "all" {
		mailType = ""
	}
	limit, offset = normalizeLimitOffset(limit, offset)
	where := ` WHERE (?='' OR LOWER(account || ' ' || payload) LIKE ?) AND (?='' OR LOWER(payload) LIKE ?)`
	like, typeLike := "%"+query+"%", "%"+mailType+"%"
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM mail_messages`+where, query, like, mailType, typeLike).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT payload FROM mail_messages`+where+` ORDER BY received_at DESC, id DESC LIMIT ? OFFSET ?`, query, like, mailType, typeLike, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.MailMessage, 0, limit)
	for rows.Next() {
		var raw string
		var item model.MailMessage
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	return items, total, rows.Err()
}

func (s *Store) FreeAccountsPage(spaceState string, limit, offset int) ([]model.FreeAccountProfile, int, FreeAccountsPageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spaceState = strings.ToLower(strings.TrimSpace(spaceState))
	if spaceState != "outside" && spaceState != "inside" && spaceState != "removed" && spaceState != "dead" {
		spaceState = ""
	}
	limit, offset = normalizeLimitOffset(limit, offset)
	const cte = `WITH mail_dead AS (
		SELECT LOWER(email) AS email, MAX(CASE WHEN json_extract(profile,'$.chatgpt_status')='dead' OR json_extract(profile,'$.registration_status')='dead' THEN 1 ELSE 0 END) AS dead
		FROM mail_accounts GROUP BY LOWER(email)
	), accounts AS (
		SELECT id, profile,
			CASE WHEN json_extract(profile,'$.dead')=1 OR COALESCE(md.dead,0)=1 THEN 'dead'
			WHEN json_extract(profile,'$.remove_status')='completed' OR json_extract(profile,'$.remote_removed_at') IS NOT NULL THEN 'removed' WHEN json_extract(profile,'$.accept_status')='completed' THEN 'inside'
			WHEN COALESCE(json_extract(profile,'$.visited_team_count'),0)>0 THEN 'removed' ELSE 'outside' END AS space_state,
			COALESCE(
				julianday(NULLIF(json_extract(profile,'$.imported_at'),'0001-01-01T00:00:00Z')),
				julianday(NULLIF(json_extract(profile,'$.created_at'),'0001-01-01T00:00:00Z')),
				julianday(updated_at)
			) AS entered_at
		FROM free_accounts f LEFT JOIN mail_dead md ON md.email=LOWER(json_extract(f.profile,'$.email'))
	)`
	var summary FreeAccountsPageSummary
	err := s.db.QueryRow(cte+` SELECT COUNT(*),
		COALESCE(SUM(space_state='outside'),0), COALESCE(SUM(space_state='inside'),0), COALESCE(SUM(space_state='removed'),0),
		COALESCE(SUM(space_state='dead'),0),
		COALESCE(SUM(json_extract(profile,'$.oauth_status')='completed'),0),
		COALESCE(SUM(json_extract(profile,'$.push_status')='completed' AND space_state!='removed'),0),
		COALESCE(SUM(json_extract(profile,'$.accept_status')='completed' AND json_extract(profile,'$.remove_status')!='completed' AND json_extract(profile,'$.remote_removed_at') IS NULL AND LOWER(COALESCE(json_extract(profile,'$.seat_type'),'')) IN ('prolite','premium','5x')),0),
		COALESCE(SUM(CASE WHEN space_state='inside' AND json_type(profile,'$.quota_7d.used_percent') IS NOT NULL THEN MAX(0,100-CAST(json_extract(profile,'$.quota_7d.used_percent') AS REAL)) ELSE 0 END),0),
		COALESCE(SUM(space_state='inside' AND json_type(profile,'$.quota_7d.used_percent') IS NOT NULL),0),
		COALESCE(MIN(CASE WHEN json_extract(profile,'$.push_status')='completed' AND space_state!='removed' THEN json_extract(profile,'$.status_checked_at') END),''),
		COALESCE(MIN(CASE WHEN json_extract(profile,'$.push_status')='completed' AND space_state!='removed' THEN json_extract(profile,'$.quota_checked_at') END),''),
		COALESCE(SUM(space_state!='removed' AND json_extract(profile,'$.accept_status')!='completed'
			AND json_extract(profile,'$.invite_status') IN ('pending','running','completed')
			AND COALESCE(json_extract(profile,'$.admin_account_id'),'')!=''),0)
		FROM accounts`).Scan(&summary.All, &summary.Outside, &summary.Inside, &summary.Removed, &summary.Dead, &summary.OAuthReady, &summary.Monitoring,
		&summary.InsidePremium, &summary.Quota7DRemainingTotal, &summary.Quota7DCount, &summary.OldestStatusCheckedAt, &summary.OldestQuotaCheckedAt, &summary.InvitePending)
	if err != nil {
		return nil, 0, summary, err
	}
	summary.PendingSeatsByAdmin = make(map[string]map[string]int)
	pendingRows, pendingErr := s.db.Query(cte + ` SELECT
		COALESCE(json_extract(profile,'$.admin_account_id'),''),
		CASE WHEN LOWER(COALESCE(json_extract(profile,'$.seat_type'),'')) IN ('prolite','premium','5x') THEN 'premium' ELSE 'standard' END,
		COUNT(*) FROM accounts
		WHERE space_state!='removed' AND json_extract(profile,'$.accept_status')!='completed'
			AND json_extract(profile,'$.invite_status') IN ('pending','running','completed')
			AND COALESCE(json_extract(profile,'$.admin_account_id'),'')!=''
		GROUP BY 1,2`)
	if pendingErr != nil {
		return nil, 0, summary, pendingErr
	}
	for pendingRows.Next() {
		var adminID, seatType string
		var count int
		if pendingRows.Scan(&adminID, &seatType, &count) == nil {
			if summary.PendingSeatsByAdmin[adminID] == nil {
				summary.PendingSeatsByAdmin[adminID] = map[string]int{"standard": 0, "premium": 0}
			}
			summary.PendingSeatsByAdmin[adminID][seatType] = count
		}
	}
	pendingRows.Close()
	var total int
	if err := s.db.QueryRow(cte+` SELECT COUNT(*) FROM accounts WHERE (?='' OR space_state=?)`, spaceState, spaceState).Scan(&total); err != nil {
		return nil, 0, summary, err
	}
	// Sort by the current workspace stage before pagination. Legacy records
	// without a stage timestamp fall back to when they entered the list.
	rows, err := s.db.Query(cte+` SELECT profile FROM accounts WHERE (?='' OR space_state=?)
		ORDER BY CASE space_state WHEN 'inside' THEN 0 WHEN 'outside' THEN 1 ELSE 2 END,
			COALESCE(CASE space_state
				WHEN 'removed' THEN julianday(NULLIF(json_extract(profile,'$.removed_at'),'0001-01-01T00:00:00Z'))
				WHEN 'inside' THEN julianday(NULLIF(json_extract(profile,'$.joined_at'),'0001-01-01T00:00:00Z'))
				WHEN 'dead' THEN julianday(NULLIF(json_extract(profile,'$.dead_detected_at'),'0001-01-01T00:00:00Z'))
				ELSE entered_at END, entered_at) DESC,
			id DESC LIMIT ? OFFSET ?`, spaceState, spaceState, limit, offset)
	if err != nil {
		return nil, 0, summary, err
	}
	defer rows.Close()
	items := make([]model.FreeAccountProfile, 0, limit)
	for rows.Next() {
		var raw string
		var item model.FreeAccountProfile
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	return items, total, summary, rows.Err()
}

func (s *Store) ProAccountsPage(query, mergeState string, limit, offset int) ([]model.MailAccountProfile, int, ProAccountsPageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query = strings.ToLower(strings.TrimSpace(query))
	mergeState = strings.ToLower(strings.TrimSpace(mergeState))
	if mergeState != "merged" && mergeState != "unmerged" {
		mergeState = ""
	}
	limit, offset = normalizeLimitOffset(limit, offset)
	const cte = `WITH accounts AS (
		SELECT profile, encrypted_credentials, updated_at,
			CASE WHEN json_extract(profile,'$.space_merged_once')=1 THEN 'merged' ELSE 'unmerged' END AS merge_state,
			LOWER(email || ' ' || COALESCE(json_extract(profile,'$.current_plan_type'),'') || ' ' || COALESCE(json_extract(profile,'$.push_provider'),'')) AS search_text,
			COALESCE(NULLIF(json_extract(profile,'$.pro_managed_at'),''), NULLIF(json_extract(profile,'$.updated_at'),''), updated_at) AS managed_at
		FROM mail_accounts WHERE LOWER(COALESCE(json_extract(profile,'$.management_scope'),''))='pro'
	)`
	var summary ProAccountsPageSummary
	if err := s.db.QueryRow(cte+` SELECT COUNT(*),
		COALESCE(SUM(json_extract(profile,'$.access_token_present')=1 AND json_extract(profile,'$.refresh_token_present')=1),0),
		COALESCE(SUM(json_extract(profile,'$.push_status')='completed'),0), COALESCE(SUM(merge_state='merged'),0) FROM accounts`).Scan(
		&summary.All, &summary.OAuthReady, &summary.Pushed, &summary.Merged); err != nil {
		return nil, 0, summary, err
	}
	like := "%" + query + "%"
	where := ` WHERE (?='' OR search_text LIKE ?) AND (?='' OR merge_state=?)`
	var total int
	if err := s.db.QueryRow(cte+` SELECT COUNT(*) FROM accounts`+where, query, like, mergeState, mergeState).Scan(&total); err != nil {
		return nil, 0, summary, err
	}
	rows, err := s.db.Query(cte+` SELECT profile, encrypted_credentials FROM accounts`+where+` ORDER BY managed_at DESC LIMIT ? OFFSET ?`, query, like, mergeState, mergeState, limit, offset)
	if err != nil {
		return nil, 0, summary, err
	}
	defer rows.Close()
	items := make([]model.MailAccountProfile, 0, limit)
	for rows.Next() {
		var raw, encrypted string
		var item model.MailAccountProfile
		if rows.Scan(&raw, &encrypted) != nil || json.Unmarshal([]byte(raw), &item) != nil {
			continue
		}
		if plain, decryptErr := s.decrypt(encrypted); decryptErr == nil {
			var credentials model.MailAccountCredentials
			if json.Unmarshal([]byte(plain), &credentials) == nil {
				item = mailProfileWithCredentials(item, credentials)
			}
		}
		items = append(items, item)
	}
	return items, total, summary, rows.Err()
}

func (s *Store) AdminAccountsPage(limit, offset int) ([]model.AdminAccountProfile, int, AdminAccountsPageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM admin_accounts").Scan(&total); err != nil {
		return nil, 0, AdminAccountsPageSummary{}, err
	}
	rows, err := s.db.Query("SELECT profile FROM admin_accounts ORDER BY label COLLATE NOCASE LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, AdminAccountsPageSummary{}, err
	}
	items := make([]model.AdminAccountProfile, 0, limit)
	for rows.Next() {
		var raw string
		var item model.AdminAccountProfile
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	s.reconcileAdminTeamRotationChildCounts(items)
	s.populateAdminCurrentSpace(items)
	summary := AdminAccountsPageSummary{All: total}
	allRows, err := s.db.Query("SELECT profile FROM admin_accounts")
	if err != nil {
		return nil, 0, summary, err
	}
	for allRows.Next() {
		var raw string
		var item model.AdminAccountProfile
		if allRows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			summary.ChildEntries += item.TeamRotationChildCount
		}
	}
	allRows.Close()
	for _, item := range items {
		summary.ChildTotalCostUSD += item.TeamRotationChildCost
	}
	// Cost is derived from child profiles. Calculate the all-account total once
	// so the summary card is independent from the current page.
	var costTotal sql.NullFloat64
	_ = s.db.QueryRow(`SELECT SUM(CASE
		WHEN json_type(profile,'$.cost_by_admin')='object' AND EXISTS (SELECT 1 FROM json_each(free_accounts.profile,'$.cost_by_admin'))
		THEN (SELECT COALESCE(SUM(CAST(value AS REAL)),0) FROM json_each(free_accounts.profile,'$.cost_by_admin'))
		ELSE CAST(COALESCE(json_extract(profile,'$.total_cost_usd'),0) AS REAL)
	END) FROM free_accounts`).Scan(&costTotal)
	if costTotal.Valid {
		summary.ChildTotalCostUSD = costTotal.Float64
	}
	return items, total, summary, nil
}

func (s *Store) OpenAIAccountsPage(limit, offset int) ([]model.OpenAIAccountProfile, int, error) {
	return jsonProfilePage[model.OpenAIAccountProfile](s, "openai_accounts", "label COLLATE NOCASE", limit, offset)
}

func (s *Store) ProxiesPage(limit, offset int) ([]model.ProxyProfile, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM proxies").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query("SELECT id,name,url,created_at,updated_at FROM proxies ORDER BY name COLLATE NOCASE LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.ProxyProfile, 0, limit)
	for rows.Next() {
		var item model.ProxyProfile
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.URL, &createdAt, &updatedAt); err != nil {
			return nil, 0, err
		}
		item.CreatedAt, _ = parseTime(createdAt)
		item.UpdatedAt, _ = parseTime(updatedAt)
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) HistoryPage(limit, offset int) ([]model.HistoryEntry, int, HistoryPageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	var summary HistoryPageSummary
	if err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(json_extract(payload,'$.status')='completed'),0),
		COALESCE(SUM(json_extract(payload,'$.status')='partial'),0),
		COALESCE(SUM(CAST(COALESCE(json_extract(payload,'$.total'),0) AS INTEGER)),0) FROM history`).Scan(
		&summary.All, &summary.Completed, &summary.Partial, &summary.Accounts); err != nil {
		return nil, 0, summary, err
	}
	rows, err := s.db.Query("SELECT payload FROM history ORDER BY completed_at DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, summary, err
	}
	defer rows.Close()
	items := make([]model.HistoryEntry, 0, limit)
	for rows.Next() {
		var raw string
		var item model.HistoryEntry
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			if item.Operation == "" {
				item.Operation = "full"
			}
			items = append(items, item)
		}
	}
	return items, summary.All, summary, rows.Err()
}

func (s *Store) AccountProgressPage(teamAccountID string, limit, offset int) ([]model.AccountProgress, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	teamAccountID = strings.TrimSpace(teamAccountID)
	where := ""
	args := []any{}
	if teamAccountID != "" {
		where = " WHERE team_account_id=?"
		args = append(args, teamAccountID)
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM account_progress"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query("SELECT payload FROM account_progress"+where+" ORDER BY updated_at DESC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.AccountProgress, 0, limit)
	for rows.Next() {
		var raw string
		var item model.AccountProgress
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	return items, total, rows.Err()
}

func (s *Store) AutoRotationRunsPage(limit, offset int) ([]model.AutoRotationRun, int, error) {
	return jsonPayloadPage[model.AutoRotationRun](s, "auto_rotation_runs", "", nil, "started_at DESC", limit, offset)
}

func (s *Store) AutoRotationTasksPage(runID string, limit, offset int) ([]model.AutoRotationTask, int, error) {
	runID = strings.TrimSpace(runID)
	where, args := "", []any(nil)
	if runID != "" {
		where, args = "run_id=?", []any{runID}
	}
	return jsonPayloadPage[model.AutoRotationTask](s, "auto_rotation_tasks", where, args, "updated_at DESC", limit, offset)
}

func (s *Store) AutoRotationEventsPage(runID, taskID, accountID, eventType, query string, limit, offset int) ([]model.AutoRotationEvent, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 8)
	if runID = strings.TrimSpace(runID); runID != "" {
		conditions, args = append(conditions, "run_id=?"), append(args, runID)
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		conditions, args = append(conditions, "task_id=?"), append(args, taskID)
	}
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		conditions, args = append(conditions, "account_id=?"), append(args, accountID)
	}
	if eventType = strings.TrimSpace(eventType); eventType != "" && eventType != "all" {
		conditions, args = append(conditions, "json_extract(payload,'$.type')=?"), append(args, eventType)
	}
	if query = strings.ToLower(strings.TrimSpace(query)); query != "" {
		conditions, args = append(conditions, "LOWER(payload) LIKE ?"), append(args, "%"+query+"%")
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM auto_rotation_events"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query("SELECT payload FROM auto_rotation_events"+where+" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.AutoRotationEvent, 0, limit)
	for rows.Next() {
		var raw string
		var item model.AutoRotationEvent
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	return items, total, rows.Err()
}

func jsonProfilePage[T any](s *Store, table, orderBy string, limit, offset int) ([]T, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(fmt.Sprintf("SELECT profile FROM %s ORDER BY %s LIMIT ? OFFSET ?", table, orderBy), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]T, 0, limit)
	for rows.Next() {
		var raw string
		var item T
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	return items, total, rows.Err()
}

func jsonPayloadPage[T any](s *Store, table, where string, args []any, orderBy string, limit, offset int) ([]T, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, offset = normalizeLimitOffset(limit, offset)
	clause := ""
	if strings.TrimSpace(where) != "" {
		clause = " WHERE " + where
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM "+table+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(fmt.Sprintf("SELECT payload FROM %s%s ORDER BY %s LIMIT ? OFFSET ?", table, clause, orderBy), pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]T, 0, limit)
	for rows.Next() {
		var raw string
		var item T
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			items = append(items, item)
		}
	}
	return items, total, rows.Err()
}

func normalizeLimitOffset(limit, offset int) (int, int) {
	switch limit {
	case 10, 50, 100, 500:
	default:
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
