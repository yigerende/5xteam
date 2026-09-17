package store

import (
	"context"
	"errors"
	"strings"
)

// FreeAccountSelection contains only the fields needed by the existing bulk actions.
type FreeAccountSelection struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	AdminAccountID  string `json:"admin_account_id"`
	TeamAccountID   string `json:"team_account_id"`
	InviteStatus    string `json:"invite_status"`
	AcceptStatus    string `json:"accept_status"`
	OAuthStatus     string `json:"oauth_status"`
	RemoveStatus    string `json:"remove_status"`
	Dead            bool   `json:"dead"`
	Sub2AccountID   int64  `json:"sub2_account_id"`
	CPAAuthFileName string `json:"cpa_auth_file_name"`
}

func (s *Store) SelectFreeAccountsByAdmin(ctx context.Context, adminID string) ([]FreeAccountSelection, error) {
	adminID = strings.TrimSpace(adminID)
	if adminID == "" {
		return nil, errors.New("请选择母号")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM admin_accounts WHERE id=?", adminID).Scan(&exists); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,
		COALESCE(json_extract(profile,'$.email'),''),
		COALESCE(json_extract(profile,'$.admin_account_id'),''),
		COALESCE(json_extract(profile,'$.team_account_id'),''),
		COALESCE(json_extract(profile,'$.invite_status'),''),
		COALESCE(json_extract(profile,'$.accept_status'),''),
		COALESCE(json_extract(profile,'$.oauth_status'),''),
		COALESCE(json_extract(profile,'$.remove_status'),''),
		COALESCE(json_extract(profile,'$.dead'),0),
		COALESCE(json_extract(profile,'$.sub2_account_id'),0),
		COALESCE(json_extract(profile,'$.cpa_auth_file_name'),'')
		FROM free_accounts WHERE json_extract(profile,'$.admin_account_id')=?
		ORDER BY updated_at DESC,id DESC`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FreeAccountSelection, 0)
	for rows.Next() {
		var item FreeAccountSelection
		if err := rows.Scan(&item.ID, &item.Email, &item.AdminAccountID, &item.TeamAccountID,
			&item.InviteStatus, &item.AcceptStatus, &item.OAuthStatus, &item.RemoveStatus,
			&item.Dead, &item.Sub2AccountID, &item.CPAAuthFileName); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
