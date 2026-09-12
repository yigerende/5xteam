package model

import "time"

// TeamVisit is permanent business history, independent of diagnostic retention.
type TeamVisit struct {
	TeamAccountID  string     `json:"team_account_id"`
	AdminAccountID string     `json:"admin_account_id"`
	AdminEmail     string     `json:"admin_email"`
	AdminLabel     string     `json:"admin_label"`
	CycleID        string     `json:"cycle_id"`
	EnteredAt      *time.Time `json:"entered_at,omitempty"`
	RemovedAt      *time.Time `json:"removed_at,omitempty"`
	Outcome        string     `json:"outcome"`
	RemovalReason  string     `json:"removal_reason,omitempty"`
	Historical     bool       `json:"historical,omitempty"`
}
