package store

import (
	"context"
	"strings"
)

// SelectUnloggedProAccounts uses the list's partition but has no pagination.
// An expired or incomplete credential does not mean the account never logged in.
// Only return identifiers; selecting accounts must not read or expose secrets.
func (s *Store) SelectUnloggedProAccounts(ctx context.Context, query string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query = strings.ToLower(strings.TrimSpace(query))
	rows, err := s.db.QueryContext(ctx, proAccountsCTE+` SELECT email FROM accounts
		WHERE merge_state='unmerged' AND (?='' OR search_text LIKE ?)
		AND COALESCE(json_extract(profile,'$.access_token_present'),0)=0
		AND COALESCE(json_extract(profile,'$.refresh_token_present'),0)=0
		AND COALESCE(json_extract(profile,'$.id_token_present'),0)=0
		AND COALESCE(json_extract(profile,'$.chatgpt_session_present'),0)=0
		AND COALESCE(json_extract(profile,'$.siwc_present'),0)=0
		AND COALESCE(json_extract(profile,'$.at_checked_at'),'')=''
		AND COALESCE(json_extract(profile,'$.auth_type'),'') NOT IN ('AT','RT')
		AND COALESCE(json_extract(profile,'$.oauth_status'),'')!='completed'
		AND COALESCE(json_extract(profile,'$.oauth_authorized_at'),'')=''
		AND COALESCE(json_extract(profile,'$.pro_auto.steps.login'),'')!='completed'
		AND COALESCE(json_extract(profile,'$.pro_manual_stages.login.status'),'')!='completed'
		ORDER BY `+proAccountsOrderSQL("unmerged"), query, "%"+query+"%")
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
