package model

import "time"

// Sub2Settings contains the non-secret part of the Sub2API connection.
// The password is encrypted separately by the store.
type Sub2Settings struct {
	URL                        string   `json:"url"`
	Email                      string   `json:"email"`
	PasswordPresent            bool     `json:"password_present"`
	GroupID                    int64    `json:"group_id,omitempty"`
	GroupName                  string   `json:"group_name,omitempty"`
	GroupIDs                   []int64  `json:"group_ids,omitempty"`
	GroupNames                 []string `json:"group_names,omitempty"`
	Models                     []string `json:"models,omitempty"`
	AccountConcurrency         int      `json:"account_concurrency"`
	Priority                   int      `json:"priority"`
	Enable401Check             bool     `json:"enable_401_check"`
	StatusCheckIntervalSeconds int      `json:"status_check_interval_seconds"`
	QuotaCheckIntervalSeconds  int      `json:"quota_check_interval_seconds"`
}

func DefaultSub2Settings() Sub2Settings {
	return Sub2Settings{AccountConcurrency: 10, Priority: 1, Enable401Check: true, StatusCheckIntervalSeconds: 120, QuotaCheckIntervalSeconds: 120}
}

type FreeQuotaWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// FreeAccountProfile is the durable, non-secret projection of one account in
// the Free -> Team -> Codex OAuth -> Sub2 lifecycle.
type FreeAccountProfile struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	UserID            string `json:"user_id"`
	PersonalAccountID string `json:"personal_account_id"`
	PlanType          string `json:"plan_type"`
	// ImportMode identifies records received from an integration that must not
	// be treated as queued Team-rotation work.
	ImportMode               string           `json:"import_mode,omitempty"`
	AdminAccountID           string           `json:"admin_account_id,omitempty"`
	AdminEmail               string           `json:"admin_email,omitempty"`
	TeamAccountID            string           `json:"team_account_id,omitempty"`
	SeatType                 string           `json:"seat_type,omitempty"`
	Status                   string           `json:"status"`
	InviteStatus             string           `json:"invite_status"`
	AcceptStatus             string           `json:"accept_status"`
	OAuthStatus              string           `json:"oauth_status"`
	PushStatus               string           `json:"push_status"`
	QuotaStatus              string           `json:"quota_status"`
	RemoveStatus             string           `json:"remove_status"`
	Dead                     bool             `json:"dead,omitempty"`
	DeadReason               string           `json:"dead_reason,omitempty"`
	DeadDetectedAt           *time.Time       `json:"dead_detected_at,omitempty"`
	LastError                string           `json:"last_error,omitempty"`
	SourceTokenPresent       bool             `json:"source_token_present"`
	OAuthAccessTokenPresent  bool             `json:"oauth_access_token_present"`
	OAuthRefreshTokenPresent bool             `json:"oauth_refresh_token_present"`
	OAuthAccountID           string           `json:"oauth_account_id,omitempty"`
	Sub2AccountID            int64            `json:"sub2_account_id,omitempty"`
	Sub2AccountName          string           `json:"sub2_account_name,omitempty"`
	Sub2GroupID              int64            `json:"sub2_group_id,omitempty"`
	Sub2GroupName            string           `json:"sub2_group_name,omitempty"`
	Sub2GroupIDs             []int64          `json:"sub2_group_ids,omitempty"`
	Sub2GroupNames           []string         `json:"sub2_group_names,omitempty"`
	ReloginCount             int              `json:"relogin_count"`
	Quota5H                  *FreeQuotaWindow `json:"quota_5h,omitempty"`
	Quota7D                  *FreeQuotaWindow `json:"quota_7d,omitempty"`
	ExhaustionPolicy         string           `json:"exhaustion_policy"`
	AutoRemove               bool             `json:"auto_remove"`
	ImportedAt               time.Time        `json:"imported_at"`
	JoinedAt                 *time.Time       `json:"joined_at,omitempty"`
	OAuthReadyAt             *time.Time       `json:"oauth_ready_at,omitempty"`
	PushedAt                 *time.Time       `json:"pushed_at,omitempty"`
	QuotaCheckedAt           *time.Time       `json:"quota_checked_at,omitempty"`
	StatusCheckedAt          *time.Time       `json:"status_checked_at,omitempty"`
	RemovedAt                *time.Time       `json:"removed_at,omitempty"`
	CreatedAt                time.Time        `json:"created_at"`
	UpdatedAt                time.Time        `json:"updated_at"`
}
