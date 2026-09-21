package model

import "time"

type ProAutoState struct {
	ProvisionedAt        *time.Time        `json:"provisioned_at,omitempty"`
	ID                   string            `json:"id,omitempty"`
	Status               string            `json:"status,omitempty"`
	Stage                string            `json:"stage,omitempty"`
	Steps                map[string]string `json:"steps,omitempty"`
	Error                string            `json:"error,omitempty"`
	OrderID              string            `json:"order_id,omitempty"`
	PlanCode             string            `json:"plan_code,omitempty"`
	CardID               string            `json:"card_id,omitempty"`
	ProxyName            string            `json:"proxy_name,omitempty"`
	ExitIP               string            `json:"exit_ip,omitempty"`
	QuotaUsedThreshold   float64           `json:"quota_used_threshold"`
	QuotaIntervalSeconds int               `json:"quota_interval_seconds"`
	StartedAt            time.Time         `json:"started_at,omitempty"`
	UpdatedAt            time.Time         `json:"updated_at,omitempty"`
	NextCheckAt          *time.Time        `json:"next_check_at,omitempty"`
}

// ProSettings is intentionally independent from Team rotation push settings.
// Its two providers are mutually exclusive through Provider.
type ProSettings struct {
	ScheduledEnabled          bool         `json:"scheduled_enabled"`
	MaxUnmerged               int          `json:"max_unmerged"`
	ScheduleIntervalSeconds   int          `json:"schedule_interval_seconds"`
	Provider                  string       `json:"provider"`
	Sub2                      Sub2Settings `json:"sub2"`
	CPA                       CPASettings  `json:"cpa"`
	QuotaEnabled              bool         `json:"quota_enabled"`
	QuotaUsedThreshold        float64      `json:"quota_used_threshold"`
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
		MaxUnmerged: 5, ScheduleIntervalSeconds: 120,
		Provider: "sub2", Sub2: DefaultSub2Settings(), CPA: DefaultCPASettings(),
		QuotaUsedThreshold: 100, QuotaCheckIntervalSeconds: 120, TargetSeatType: "default",
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
