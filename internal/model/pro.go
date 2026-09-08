package model

import "time"

// ProSettings is intentionally independent from Team rotation push settings.
// Its two providers are mutually exclusive through Provider.
type ProSettings struct {
	Provider                  string       `json:"provider"`
	Sub2                      Sub2Settings `json:"sub2"`
	CPA                       CPASettings  `json:"cpa"`
	QuotaEnabled              bool         `json:"quota_enabled"`
	QuotaCheckIntervalSeconds int          `json:"quota_check_interval_seconds"`
	AutoMergeEnabled          bool         `json:"auto_merge_enabled"`
	TargetAdminID             string       `json:"target_admin_id,omitempty"`
	TargetSeatType            string       `json:"target_seat_type"`
	RetryCount                int          `json:"retry_count"`
	RetryIntervalSeconds      int          `json:"retry_interval_seconds"`
	Concurrency               int          `json:"concurrency"`
	LastQuotaSweepAt          *time.Time   `json:"last_quota_sweep_at,omitempty"`
	NextQuotaSweepAt          *time.Time   `json:"next_quota_sweep_at,omitempty"`
}

func DefaultProSettings() ProSettings {
	return ProSettings{
		Provider: "sub2", Sub2: DefaultSub2Settings(), CPA: DefaultCPASettings(),
		QuotaCheckIntervalSeconds: 120, TargetSeatType: "default",
		RetryCount: 2, RetryIntervalSeconds: 3, Concurrency: 2,
	}
}

type ProOAuthSession struct {
	ID          string    `json:"session_id"`
	State       string    `json:"state"`
	TargetEmail string    `json:"target_email,omitempty"`
	RedirectURI string    `json:"redirect_uri"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}
