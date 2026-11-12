package model

import (
	"fmt"
	"time"
)

type ProAutoState struct {
	OAuthSource           string                  `json:"oauth_source,omitempty"`
	Replacement           *ProRechargeReplacement `json:"replacement,omitempty"`
	PaymentReviewRequired bool                    `json:"payment_review_required,omitempty"`
	// Base connection and effective groups are separate so extra memberships
	// do not change the restock pool's identity or hide in-flight work.
	Sub2PushGroupIDs     []int64              `json:"sub2_push_group_ids,omitempty"`
	ProxyRetryAttempts   int                  `json:"proxy_retry_attempts,omitempty"`
	RetryAttempts        int                  `json:"retry_attempts,omitempty"`
	RetryAt              *time.Time           `json:"retry_at,omitempty"`
	StopVersion          uint64               `json:"stop_version,omitempty"`
	StopRequested        bool                 `json:"stop_requested,omitempty"`
	RestockRecoveries    int                  `json:"restock_recoveries,omitempty"`
	RestockRecoveryAt    *time.Time           `json:"restock_recovery_at,omitempty"`
	RestockBinding       string               `json:"restock_binding,omitempty"`
	QuotaPlan            string               `json:"quota_plan,omitempty"`
	StageTimes           map[string]time.Time `json:"stage_times,omitempty"`
	ProvisionedAt        *time.Time           `json:"provisioned_at,omitempty"`
	ID                   string               `json:"id,omitempty"`
	Status               string               `json:"status,omitempty"`
	Stage                string               `json:"stage,omitempty"`
	Steps                map[string]string    `json:"steps,omitempty"`
	Error                string               `json:"error,omitempty"`
	OrderID              string               `json:"order_id,omitempty"`
	PlanCode             string               `json:"plan_code,omitempty"`
	CardID               string               `json:"card_id,omitempty"`
	ProxyName            string               `json:"proxy_name,omitempty"`
	ExitIP               string               `json:"exit_ip,omitempty"`
	QuotaUsedThreshold   float64              `json:"quota_used_threshold"`
	QuotaIntervalSeconds int                  `json:"quota_interval_seconds"`
	StartedAt            time.Time            `json:"started_at,omitempty"`
	UpdatedAt            time.Time            `json:"updated_at,omitempty"`
	NextCheckAt          *time.Time           `json:"next_check_at,omitempty"`
}

// Each confirmed unpaid retry gets a new payment key; the workflow and
// restock task keep their identity. Recovery must use this key after a crash.
func (s ProAutoState) PaymentOrderID() string {
	if s.ID == "" {
		return ""
	}
	id := "pro-auto-" + s.ID
	if s.RetryAttempts > 0 {
		id += fmt.Sprintf("-retry-%d", s.RetryAttempts)
	}
	return id
}

// ProSettings is intentionally independent from Team rotation push settings.
// Its providers are mutually exclusive through Provider.
type ProSettings struct {
	MergeV2Recovery              *ProMergeV2RecoverySettings `json:"merge_v2_recovery,omitempty"`
	BalanceFallbackEnabled       bool                        `json:"balance_fallback_enabled"`
	OAIConfirmationConcurrency   int                         `json:"oai_confirmation_concurrency"`
	OAIInquiry                   ProOAIInquiry               `json:"oai_inquiry"`
	OAIConfirmation              ProOAIConfirmationSettings  `json:"oai_confirmation"`
	MerchantAPIURL               string                      `json:"merchant_api_url"`
	MerchantProvider             string                      `json:"merchant_provider"`
	MerchantStarBridgeAPIURL     string                      `json:"merchant_starbridge_api_url"`
	MerchantStarBridgeKeyPresent bool                        `json:"merchant_starbridge_key_present"`
	MerchantSuccessFeeUSD        string                      `json:"merchant_success_fee_usd"`
	PurchaseScheduleEnabled      bool                        `json:"purchase_schedule_enabled"`
	PurchaseWindows              []ProPurchaseWindow         `json:"purchase_windows"`
	EJHPlanTemplates             map[string]string           `json:"ejh_plan_templates"`
	NexovccPlanTemplates         map[string]string           `json:"nexovcc_plan_templates"`
	PurchaseRoute                *ProPurchaseRoute           `json:"-"`
	CardProvider                 string                      `json:"card_provider"`
	NexovccAutoOpen              bool                        `json:"nexovcc_auto_open"`
	NexovccTemplateID            string                      `json:"nexovcc_template_id"`
	NexovccDailyOpenLimit        int                         `json:"nexovcc_daily_open_limit"`
	NexovccDailyAmountLimit      string                      `json:"nexovcc_daily_amount_limit"`
	EJHAutoOpen                  bool                        `json:"ejh_auto_open"`
	EJHTemplateID                string                      `json:"ejh_template_id"`
	EJHDailyOpenLimit            int                         `json:"ejh_daily_open_limit"`
	EJHDailyAmountLimit          string                      `json:"ejh_daily_amount_limit"`
	ForceMergeEnabled            bool                        `json:"force_merge_enabled"`
	ForceMergeMinutes            int                         `json:"force_merge_minutes"`
	PostMergeGroupIDs            []int64                     `json:"post_merge_group_ids"`
	PostMergeGroupNames          []string                    `json:"post_merge_group_names"`
	ScheduledEnabled             bool                        `json:"scheduled_enabled"`
	MaxUnmerged                  int                         `json:"max_unmerged"`
	ScheduleIntervalSeconds      int                         `json:"schedule_interval_seconds"`
	Provider                     string                      `json:"provider"`
	Sub2                         Sub2Settings                `json:"sub2"`
	CPA                          CPASettings                 `json:"cpa"`
	VM2API                       VM2APISettings              `json:"vm2api"`
	QuotaEnabled                 bool                        `json:"quota_enabled"`
	QuotaUsedThreshold           float64                     `json:"quota_used_threshold"`
	QuotaCheckIntervalSeconds    int                         `json:"quota_check_interval_seconds"`
	AutoMergeEnabled             bool                        `json:"auto_merge_enabled"`
	AutoMergeV2Enabled           bool                        `json:"auto_merge_v2_enabled"`
	AutoMerchantEnabled          bool                        `json:"auto_merchant_enabled"`
	MerchantKeyPresent           bool                        `json:"merchant_key_present"`
	AutoDeleteEnabled            bool                        `json:"auto_delete_enabled"`
	AutoDeletePaused             bool                        `json:"auto_delete_paused"` // Legacy key; pauses all automatic final-step modes.
	DeletionCheckIntervalSeconds int                         `json:"deletion_check_interval_seconds"`
	TargetAdminID                string                      `json:"target_admin_id,omitempty"`
	TargetSeatType               string                      `json:"target_seat_type"`
	RetryCount                   int                         `json:"retry_count"`
	RechargeFailoverCount        int                         `json:"recharge_failover_count"`
	RechargeReplacementCount     int                         `json:"recharge_replacement_count"`
	RetryIntervalSeconds         int                         `json:"retry_interval_seconds"`
	Concurrency                  int                         `json:"concurrency"`
	MergeConcurrency             int                         `json:"merge_concurrency"`
	LastQuotaSweepAt             *time.Time                  `json:"last_quota_sweep_at,omitempty"`
	NextQuotaSweepAt             *time.Time                  `json:"next_quota_sweep_at,omitempty"`
}

func DefaultProSettings() ProSettings {
	return ProSettings{
		OAIConfirmationConcurrency: 2,
		OAIInquiry:                 DefaultProOAIInquiry(),
		OAIConfirmation:            DefaultProOAIConfirmationSettings(),
		MerchantAPIURL:             "https://refunds.llmrefund.com/v1/refunds",
		MerchantProvider:           "original",
		MerchantStarBridgeAPIURL:   "https://bridge.ccgpai.com/api/v1/refund/orders",
		EJHDailyOpenLimit:          1, ForceMergeMinutes: 60, CardProvider: "ejh", NexovccDailyOpenLimit: 1,
		MaxUnmerged: 5, ScheduleIntervalSeconds: 120,
		Provider: "sub2", Sub2: DefaultSub2Settings(), CPA: DefaultCPASettings(), VM2API: DefaultVM2APISettings(),
		QuotaUsedThreshold: 100, QuotaCheckIntervalSeconds: 120, TargetSeatType: "default",
		DeletionCheckIntervalSeconds: 300,
		RetryCount:                   2, RechargeReplacementCount: 2, RetryIntervalSeconds: 3, Concurrency: 2, MergeConcurrency: 2,
	}
}

func (p ProSettings) AutoCardProvider() string {
	if p.CardProvider == "nexovcc" {
		return "nexovcc"
	}
	return "ejh"
}
func (p ProSettings) AutoCardEnabled() bool {
	if p.AutoCardProvider() == "nexovcc" {
		return p.NexovccAutoOpen
	}
	return p.EJHAutoOpen
}

type ProOAuthSession struct {
	ID          string    `json:"session_id"`
	State       string    `json:"state"`
	TargetEmail string    `json:"target_email,omitempty"`
	RedirectURI string    `json:"redirect_uri"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}
