package ejh

import (
	"chatgpt-space-merge/internal/model"
	"time"
)

type Operation struct {
	Resolution    string `json:"resolution,omitempty"`
	RestockTaskID string `json:"restock_task_id,omitempty"`

	ErrorCode    string `json:"error_code,omitempty"`
	Platform     string `json:"platform"`
	TemplateName string `json:"template_name,omitempty"`

	PlanCode           string     `json:"plan_code,omitempty"`
	TemplateID         string     `json:"template_id,omitempty"`
	ID                 string     `json:"id"`
	Method             string     `json:"method"`
	CardID             string     `json:"card_id"`
	OrderNo            string     `json:"order_no"`
	Status             string     `json:"status"`
	Error              string     `json:"error"`
	Amount             string     `json:"amount"`
	Currency           string     `json:"currency"`
	Automatic          bool       `json:"automatic"`
	AutoCheckStartedAt *time.Time `json:"auto_check_started_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	Request            OpenInput  `json:"request"`
	Result             *Card      `json:"result,omitempty"`
	Note               string     `json:"note"`
	NextCheckAt        *time.Time `json:"next_check_at,omitempty"`
	CheckError         string     `json:"check_error,omitempty"`
}
type CardRecord struct {
	NormalPending   bool       `json:"normal_pending,omitempty"`
	NormalPendingAt *time.Time `json:"normal_pending_at,omitempty"`
	Platform        string     `json:"platform"`
	TemplateName    string     `json:"template_name,omitempty"`
	TemplateID      string     `json:"template_id,omitempty"`

	ID               string     `json:"id"`
	CreatedAt        time.Time  `json:"created_at"`
	Remote           Card       `json:"remote"`
	Balance          string     `json:"balance"`
	BalanceAt        *time.Time `json:"balance_at,omitempty"`
	BalanceError     string     `json:"balance_error,omitempty"`
	BalanceAttemptAt *time.Time `json:"balance_attempt_at,omitempty"`
	SyncedAt         time.Time  `json:"synced_at"`
	Error            string     `json:"error"`
}
type TradeRecord struct {
	ReversalFee     *ReversalFeeRate `json:"reversal_fee,omitempty"`
	ID              string           `json:"id"`
	CardID          string           `json:"card_id"`
	Trade           Trade            `json:"trade"`
	OrderID         string           `json:"payment_order_id"`
	OriginalTradeID string           `json:"original_trade_id"`
	Confirmed       bool             `json:"confirmed"`
	Amount          string           `json:"confirmed_amount"`
	Currency        string           `json:"confirmed_currency"`
	Evidence        string           `json:"evidence"`
	Source          string           `json:"source"`
	SyncedAt        time.Time        `json:"synced_at"`
	ConfirmedAt     *time.Time       `json:"confirmed_at,omitempty"`
}
type CaseStateOverride struct {
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Revision  int64     `json:"revision"`
}

func ValidCaseState(state string) bool {
	return state == "unopened" || state == "opened" || state == "waiting" || state == "returned"
}

type Case struct {
	ProDead                *model.ProDeadState                  `json:"pro_dead,omitempty"`
	ProOAIAvailableModes   []string                             `json:"pro_oai_available_modes,omitempty"`
	ProOAIActionsAvailable bool                                 `json:"pro_oai_actions_available"`
	ProOAIConfirmations    map[string]*model.ProOAIConfirmation `json:"pro_oai_confirmations,omitempty"`
	ReversalFee            *ReversalFeeRate                     `json:"reversal_fee,omitempty"`
	MerchantFeeUSD         string                               `json:"merchant_fee_usd"`
	// Removal only affects the unopened return record in the EJH dashboard.
	// Keep the underlying case for payment reconciliation and card occupancy.
	RemovedAt *time.Time `json:"removed_at,omitempty"`

	// Read from the account's current user-cost snapshot; nil means unqueried.
	ConsumptionTotal *float64 `json:"consumption_total,omitempty"`
	// Read-only projection; not_pushed is distinct from a queried zero.
	ConsumptionState string `json:"consumption_state,omitempty"`

	AutomaticState  string             `json:"automatic_state"`
	StateOverride   *CaseStateOverride `json:"state_override,omitempty"`
	Charge          *ActivationCharge  `json:"charge,omitempty"`
	Credits         *ActivationCredits `json:"credits,omitempty"`
	ChargeEvidence  string             `json:"charge_evidence,omitempty"`
	ChargeLinkedAt  *time.Time         `json:"charge_linked_at,omitempty"`
	CardType        string             `json:"card_type"`
	EJHCardID       string             `json:"ejh_card_id,omitempty"`
	OriginalTradeID string             `json:"original_trade_id"`
	ID              string             `json:"id"`
	Email           string             `json:"email"`
	CardID          string             `json:"card_id"`
	CardName        string             `json:"card_name"`
	Last4           string             `json:"last4"`
	Plan            string             `json:"plan"`
	Provider        string             `json:"provider"`
	PaymentStatus   string             `json:"payment_status"`
	CreatedAt       time.Time          `json:"created_at"`
	OpenedAt        *time.Time         `json:"opened_at,omitempty"`
	RequestedAt     *time.Time         `json:"requested_at,omitempty"`
	RequestSource   string             `json:"request_source"`
	Deleted         bool               `json:"deleted"`
	State           string             `json:"state"`
	Returns         []TradeRecord      `json:"returns"`
	Amounts         map[string]string  `json:"amounts"`
	Reversals       int                `json:"reversals"`
	Refunds         int                `json:"refunds"`
}
type SyncState struct {
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastAttempt *time.Time `json:"last_attempt,omitempty"`
	Error       string     `json:"error"`
}
