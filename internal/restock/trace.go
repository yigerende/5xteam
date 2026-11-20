package restock

// DecisionTrace preserves intermediate policy inputs that cannot be recovered
// from current settings or task states when reviewing a historical decision.
// A missing trace means an older record, not zero demand or zero capacity.
type DecisionTrace struct {
	ConcurrencyTargetPercent    int           `json:"concurrency_target_percent,omitempty"`
	ConcurrencyTriggerPercent   int           `json:"concurrency_trigger_percent,omitempty"`
	ConcurrencyTrigger          int           `json:"concurrency_trigger"`
	Version                     int           `json:"version"`
	NewAccountCapacity          int           `json:"new_account_capacity"` // raw, persisted on tasks
	EffectiveNewAccountCapacity float64       `json:"effective_new_account_capacity,omitempty"`
	GroupInflight               int           `json:"group_inflight"`
	TimelyInflight              int           `json:"timely_inflight"`
	FutureCapacity              float64       `json:"future_capacity"`
	TimeTriggered               bool          `json:"time_triggered"`
	ConcurrencyTriggered        bool          `json:"concurrency_triggered"`
	MoneySizing                 *MoneySizing  `json:"money_sizing,omitempty"`
	NetNeed                     int           `json:"net_need"`
	AfterMinimum                *int          `json:"after_minimum,omitempty"`
	StrictMinimum               bool          `json:"strict_minimum"`
	Caps                        []DecisionCap `json:"caps,omitempty"`
	HardLimit                   *int          `json:"hard_limit,omitempty"`
	CooldownSeconds             *int          `json:"cooldown_seconds,omitempty"`
	CooldownRemainingSeconds    int           `json:"cooldown_remaining_seconds"`
	StopStage                   string        `json:"stop_stage"`
	EvaluationReason            string        `json:"evaluation_reason"`
}

type DecisionCap struct {
	Key       string `json:"key"`
	Remaining int    `json:"remaining"`
}

type MoneySizing struct {
	NewBudgetUSD float64 `json:"new_budget_usd"`
	Pending      int     `json:"pending"`
	GapUSD       float64 `json:"gap_usd"`
}
