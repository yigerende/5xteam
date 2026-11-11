// Package restock contains the side-effect-free Pro replenishment policy.
package restock

import (
	"chatgpt-space-merge/internal/model"
	"errors"
	"time"
)

type Config struct {
	DeadAccountPause     bool           `json:"dead_account_pause"`
	CapacityTiersEnabled bool           `json:"capacity_tiers_enabled"`
	CapacityTiers        []CapacityTier `json:"capacity_tiers"`
	DemandGate           *DemandGate    `json:"-"`
	NoRestockWindows     []DailyWindow  `json:"no_restock_windows"`
	DailyEJHMergeEnabled bool           `json:"daily_ejh_merge_enabled"`
	DailyEJHMergeTime    string         `json:"daily_ejh_merge_time"`

	CapacityPercent          int    `json:"capacity_percent"`
	ExitEnabled              bool   `json:"exit_enabled"`
	ExitMinutes              int    `json:"exit_minutes"`
	ExitWarningPercent       int    `json:"exit_warning_percent"`
	ExitRefreshSeconds       int    `json:"exit_refresh_seconds"`
	ExitDemandHoldSeconds    int    `json:"exit_demand_hold_seconds"`
	NoRestockEnabled         bool   `json:"no_restock_enabled"`
	NoRestockStart           string `json:"no_restock_start"`
	NoRestockEnd             string `json:"no_restock_end"`
	ForceMergeEnabled        bool   `json:"force_merge_enabled"`
	ForceMergeMinutes        int    `json:"force_merge_minutes"`
	MinNewUsePercent         int    `json:"min_new_use_percent"`
	FaultPercent             int    `json:"fault_percent"`
	Version                  int    `json:"version"`
	Enabled                  bool   `json:"enabled"`
	Mode                     string `json:"mode"`
	GroupID                  int64  `json:"group_id"`
	TimeEnabled              bool   `json:"time_enabled"`
	TriggerMinutes           int    `json:"trigger_minutes"`
	TargetMinutes            int    `json:"target_minutes"`
	Early                    bool   `json:"early"`
	ConcurrencyEnabled       bool   `json:"concurrency_enabled"`
	TriggerPercent           int    `json:"trigger_percent"`
	TargetPercent            int    `json:"target_percent"`
	Confirmations            int    `json:"confirmations"`
	EmergencyQueue           int    `json:"emergency_queue"`
	MinBatch                 int    `json:"min_batch"`
	MaxBatch                 int    `json:"max_batch"`
	AllowSmall               bool   `json:"allow_small"`
	MaxInflight              int    `json:"max_inflight"`
	Reserve                  int    `json:"reserve"`
	EmptyGroupCount          int    `json:"empty_group_count"`
	CheckSeconds             int    `json:"check_seconds"`
	QuotaSeconds             int    `json:"quota_seconds"`
	MoneyWindowMinutes       int    `json:"money_window_minutes"`
	CooldownSeconds          int    `json:"cooldown_seconds"`
	EmergencyCooldownSeconds int    `json:"emergency_cooldown_seconds"`
	HourLimit                int    `json:"hour_limit"`
	DayLimit                 int    `json:"day_limit"`
	HourBudget               int64  `json:"hour_budget"`
	DayBudget                int64  `json:"day_budget"`
	UnitCost                 int64  `json:"unit_cost"`
	PoolLimit                int    `json:"pool_limit"`
	FailureLimit             int    `json:"failure_limit"`
	FreshSeconds             int    `json:"fresh_seconds"`
	QuotaCoverage            int    `json:"quota_coverage"`
	LeadMinutes              int    `json:"lead_minutes"`
	MarginMinutes            int    `json:"margin_minutes"`
	ForecastMinutes          int    `json:"forecast_minutes"`
	LeadSamples              int    `json:"lead_samples"`
	RetentionDays            int    `json:"retention_days"`
}

func DefaultConfig() Config {
	return Config{CapacityPercent: DefaultCapacityPercent, ExitMinutes: 5, ExitWarningPercent: 5, ExitRefreshSeconds: 15, ExitDemandHoldSeconds: 60, NoRestockStart: "00:00", NoRestockEnd: "07:00", ForceMergeMinutes: 15, FaultPercent: 50, Version: 1, Mode: "observe", TimeEnabled: true, TriggerMinutes: 10, TargetMinutes: 15, Early: true, ConcurrencyEnabled: true, TriggerPercent: 85, TargetPercent: 75, Confirmations: 2, EmergencyQueue: 3, MinBatch: 3, MaxBatch: 5, AllowSmall: true, MaxInflight: 6, CheckSeconds: 5, QuotaSeconds: 60, MoneyWindowMinutes: DefaultMoneyWindowMinutes, CooldownSeconds: 60, EmergencyCooldownSeconds: 20, HourLimit: 20, DayLimit: 100, PoolLimit: 100, FailureLimit: 3, FreshSeconds: 20, QuotaCoverage: 80, LeadMinutes: 5, MarginMinutes: 1, ForecastMinutes: 60, LeadSamples: 10, RetentionDays: 7}
}
func (c Config) Validate() error {
	if err := c.validateCapacityTiers(); err != nil {
		return err
	}
	if c.DailyEJHMergeEnabled {
		if _, ok := dailyMinute(c.DailyEJHMergeTime); !ok {
			return errors.New("每日 EJH 定点合并时间必须为 HH:mm（00:00～23:59）")
		}
	}
	if c.CapacityPercent < 1 || c.CapacityPercent > 100 {
		return errors.New("总并发容量使用比例应为 1～100% 的整数")
	}
	if c.ExitMinutes < 1 || c.ExitMinutes > 1440 || c.ExitWarningPercent < 0 || c.ExitWarningPercent > 20 || c.ExitRefreshSeconds < 5 || c.ExitRefreshSeconds > 60 || c.ExitDemandHoldSeconds < 5 || c.ExitDemandHoldSeconds > 600 {
		return errors.New("退出前补货：提前时间应为 1～1440 分钟，额度预警为 0～20 个百分点，临期刷新为 5～60 秒，需求保留为 5～600 秒")
	}
	if c.MoneyWindowMinutes < 1 || c.MoneyWindowMinutes > MaxMoneyWindowMinutes {
		return errors.New("消耗计算窗口应为 1～60 分钟的整数")
	}
	if c.NoRestockEnabled {
		if err := c.validateNoRestockWindows(); err != nil {
			return err
		}
	}
	if c.ForceMergeMinutes < 1 || c.ForceMergeMinutes > 525600 {
		return errors.New("提前补货平均剩余时间阈值应为 1～525600 分钟")
	}
	if c.EmptyGroupCount < 0 || c.EmptyGroupCount > 1000 {
		return errors.New("空分组启动补货数量应为 0～1000，0 表示关闭")
	}
	if c.MinNewUsePercent < 0 || c.MinNewUsePercent > 100 {
		return errors.New("新号预计最低使用比例应为 0～100%，0 表示不限制")
	}
	if c.FaultPercent < 1 || c.FaultPercent > 100 {
		return errors.New("账号异常暂停比例应为 1～100%")
	}
	if c.Mode != "observe" && c.Mode != "auto" {
		return errors.New("运行模式无效")
	}
	if c.Enabled && c.GroupID <= 0 {
		return errors.New("请选择唯一监控分组")
	}
	if c.TriggerMinutes < 1 || c.TargetMinutes <= c.TriggerMinutes || c.TargetMinutes > 1440 || c.ForecastMinutes < c.TargetMinutes || c.ForecastMinutes > 1440 {
		return errors.New("目标分钟必须大于触发分钟，且不超过预测范围（最多 1440 分钟）")
	}
	if c.TriggerPercent < 1 || c.TriggerPercent > 100 || c.TargetPercent < 1 || c.TargetPercent >= c.TriggerPercent || c.Confirmations < 1 || c.Confirmations > 100 || c.EmergencyQueue < 1 {
		return errors.New("并发阈值、目标或确认次数无效")
	}
	if c.MinBatch < 1 || c.MaxBatch < c.MinBatch || c.MaxBatch > 100 || c.MaxInflight < c.MinBatch || c.MaxInflight > 1000 || c.Reserve < 0 || c.Reserve > c.PoolLimit {
		return errors.New("批量、在途或备用上限无效")
	}
	if c.CheckSeconds < 2 || c.CheckSeconds > 300 || c.QuotaSeconds < 30 || c.QuotaSeconds > 3600 || c.CooldownSeconds < 1 || c.CooldownSeconds > 86400 || c.EmergencyCooldownSeconds < 1 || c.EmergencyCooldownSeconds > c.CooldownSeconds {
		return errors.New("检查周期或冷却时间无效")
	}
	if c.HourLimit < 1 || c.DayLimit < c.HourLimit || c.DayLimit > 10000 || c.PoolLimit < 1 || c.PoolLimit > 10000 || c.FailureLimit < 1 || c.FailureLimit > 100 || c.HourBudget < 0 || c.DayBudget < 0 || c.UnitCost < 0 || c.UnitCost > 100000000 || c.HourBudget > 100000000000 || c.DayBudget > 100000000000 {
		return errors.New("预算、数量限制或熔断参数无效")
	}
	if (c.HourBudget > 0 || c.DayBudget > 0) && c.UnitCost <= 0 {
		return errors.New("设置金额预算时必须填写每号成本预留，使用与预算一致的记账单位")
	}
	if c.FreshSeconds < c.CheckSeconds || c.FreshSeconds > 3600 || c.QuotaCoverage < 1 || c.QuotaCoverage > 100 || c.LeadMinutes < 1 || c.LeadMinutes > 1440 || c.MarginMinutes < 0 || c.MarginMinutes > 120 || c.LeadSamples < 2 || c.LeadSamples > 1000 || c.RetentionDays < 1 || c.RetentionDays > 90 {
		return errors.New("数据保护或就绪耗时参数无效")
	}
	return nil
}

type Window struct {
	Seconds       int64     `json:"seconds"`
	ResetAt       int64     `json:"reset_at"`
	Used          float64   `json:"used"`
	Rate          float64   `json:"rate"`                    // percentage points/minute, same plan/window only
	ObservedRate  *float64  `json:"observed_rate,omitempty"` // latest interval, before conservative smoothing
	SampleSeconds float64   `json:"sample_seconds,omitempty"`
	Samples       int       `json:"samples"`
	At            time.Time `json:"at"`
}

// Fresh prevents old quota samples from reducing current or future capacity.
func (w Window) Fresh(now time.Time, maxAge time.Duration) bool {
	return !w.At.IsZero() && !w.At.After(now.Add(5*time.Second)) && now.Sub(w.At) <= maxAge && w.Seconds > 0 && w.ResetAt > now.Unix() && validWindow(w.Used, w.Rate)
}

type Account struct {
	TargetID            string        `json:"target_id,omitempty"`
	RemoteID            int64         `json:"remote_id,omitempty"`
	SourceURL           string        `json:"source_url,omitempty"`
	SIWC                bool          `json:"siwc,omitempty"`
	Quota5HThreshold    *float64      `json:"quota_5h_threshold,omitempty"`
	TakeoverPending     bool          `json:"takeover_pending,omitempty"`
	SupplyExcluded      bool          `json:"supply_excluded,omitempty"`
	QuotaMergeThreshold *float64      `json:"quota_merge_threshold,omitempty"`
	ExitDeadline        *time.Time    `json:"exit_deadline,omitempty"`
	Leaving             bool          `json:"leaving"`
	Money               *AccountMoney `json:"money,omitempty"`
	PurchasePlan        string        `json:"purchase_plan,omitempty"`
	CardProvider        string        `json:"card_provider,omitempty"`
	EJH                 bool          `json:"ejh"`
	ForceMergeKey       string        `json:"force_merge_key,omitempty"`
	PushTimeoutEnabled  *bool         `json:"push_timeout_enabled,omitempty"`
	ForceMergeDeadline  *time.Time    `json:"force_merge_deadline,omitempty"`
	Fault               bool          `json:"fault"`
	ID                  int64         `json:"id"`
	Name                string        `json:"name"`
	Plan                string        `json:"plan"`
	Ready               bool          `json:"ready"`
	Shared              bool          `json:"shared"`
	Capacity            int           `json:"capacity"`
	Used                int           `json:"used"`
	Waiting             int           `json:"waiting"`
	ExpiresAt           *time.Time    `json:"expires_at,omitempty"`
	Windows             []Window      `json:"windows"`
}
type Snapshot struct {
	MonitorOAuthCount   *int        `json:"monitor_oauth_count,omitempty"`
	MonitorOAuthError   string      `json:"monitor_oauth_error,omitempty"`
	DemandGate          *DemandGate `json:"no_restock_demand,omitempty"`
	TakeoverToken       string      `json:"takeover_token,omitempty"`
	MonitorGroups       []int64     `json:"monitor_groups,omitempty"`
	CollectionStartedAt time.Time   `json:"collection_started_at,omitempty"`
	ExitConfirmations   int         `json:"exit_confirmations"`
	ExitDemand          *ExitDemand `json:"exit_demand,omitempty"`
	NewPlan             string      `json:"new_plan"`
	LifetimeMinutes     int         `json:"lifetime_minutes"`
	ID                  string      `json:"id"`
	At                  time.Time   `json:"at"`
	GroupID             int64       `json:"group_id"`
	Binding             string      `json:"binding"`
	Valid               bool        `json:"valid"`
	LoadValid           bool        `json:"load_valid"`
	Error               string      `json:"error"`
	Accounts            []Account   `json:"accounts"`
}
type Task struct {
	// Opaque read version used only for monitoring compare-and-swap. Never exposed.
	ObservedPayload string `json:"-"`
	// Delivery can finish through an explicitly linked replacement while the
	// original payment remains unknown. Never change its financial status.
	FulfilledByTaskID string     `json:"fulfilled_by_task_id,omitempty"`
	FulfilledByEmail  string     `json:"fulfilled_by_email,omitempty"`
	FulfilledAt       *time.Time `json:"fulfilled_at,omitempty"`

	PaymentRiskPending  bool                    `json:"payment_risk_pending,omitempty"`
	PaymentRiskSource   string                  `json:"payment_risk_source,omitempty"`
	BalanceFallbackFrom string                  `json:"balance_fallback_from,omitempty"`
	PurchaseRoute       *model.ProPurchaseRoute `json:"purchase_route,omitempty"`
	TakeoverToken       string                  `json:"takeover_token,omitempty"`
	CardProvider        string                  `json:"card_provider,omitempty"`
	StrictCardProvider  bool                    `json:"strict_card_provider,omitempty"`
	Capacity            int                     `json:"capacity,omitempty"`
	ManagedExternally   bool                    `json:"managed_externally,omitempty"`
	ForceMergeSource    string                  `json:"force_merge_source,omitempty"`
	EmptyGroupStartup   bool                    `json:"empty_group_startup,omitempty"`
	Plan                string                  `json:"plan"`
	ID                  string                  `json:"id"`
	DecisionID          string                  `json:"decision_id"`
	Email               string                  `json:"email"`
	Version             int                     `json:"version"`
	GroupID             int64                   `json:"group_id"`
	Binding             string                  `json:"binding"`
	Status              string                  `json:"status"`
	Stage               string                  `json:"stage"`
	Message             string                  `json:"message"`
	OrderID             string                  `json:"order_id"`
	CardID              string                  `json:"card_id"`
	NextCardCheckAt     *time.Time              `json:"next_card_check_at,omitempty"`
	StoppedAt           *time.Time              `json:"stopped_at,omitempty"`
	StoppedFailure      bool                    `json:"stopped_failure,omitempty"`
	AccountID           int64                   `json:"account_id"`
	Cost                int64                   `json:"cost"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
	SubmittedAt         *time.Time              `json:"submitted_at,omitempty"`
	ReadyAt             *time.Time              `json:"ready_at,omitempty"`
	ETA                 *time.Time              `json:"eta,omitempty"`
	Stages              map[string]time.Time    `json:"stages"`
	// Retain assignment history after the source departs, so a ready account
	// cannot be recycled as the replacement for another original lifecycle.
	ForceMergeAssigned bool `json:"force_merge_assigned,omitempty"`
	ForceMergeTracked  bool `json:"force_merge_tracked,omitempty"`
}

// A confirmed failed payment gets one replacement, pinned to the same pool.
func PaymentRiskPending(s Snapshot, tasks []Task) bool {
	for _, t := range tasks {
		if t.PaymentRiskPending && t.Binding == s.Binding && t.GroupID == s.GroupID {
			return true
		}
	}
	return false
}

func (t Task) Active() bool {
	return !t.ReplacementDelivered() && t.Status != "ready" && t.Status != "failed" && t.Status != "cancelled"
}
func (t Task) ReplacementDelivered() bool {
	return t.FulfilledByTaskID != "" && t.FulfilledByEmail != "" && t.FulfilledAt != nil && !t.FulfilledAt.IsZero()
}
func (t Task) Predictable() bool {
	return t.Active() && t.ETA != nil && t.Status != "unknown" && t.Status != "attention" && t.Status != "waiting_card"
}

type Limits struct {
	HourCount int        `json:"hour_count"`
	DayCount  int        `json:"day_count"`
	HourCost  int64      `json:"hour_cost"`
	DayCost   int64      `json:"day_cost"`
	Failures  int        `json:"failures"`
	LastBatch *time.Time `json:"last_batch,omitempty"`
}
type Decision struct {
	CardProvider     string          `json:"card_provider,omitempty"`
	CardSchedule     CardSchedule    `json:"card_schedule"`
	BatchWait        *BatchWait      `json:"batch_wait,omitempty"`
	Exit             *ExitForecast   `json:"exit,omitempty"`
	ExitNeed         int             `json:"exit_need"`
	Trace            *DecisionTrace  `json:"trace,omitempty"`
	Money            MoneyForecast   `json:"money"`
	NoRestockActive  bool            `json:"no_restock_active"`
	ForceMerge       ForceMergeStats `json:"force_merge"`
	ForceMergeNeed   int             `json:"force_merge_need"`
	DemandKnown      bool            `json:"demand_known"`
	IdleEquivalent   float64         `json:"idle_equivalent"`
	ID               string          `json:"id"`
	At               time.Time       `json:"at"`
	Version          int             `json:"version"`
	Snapshot         Snapshot        `json:"snapshot"`
	Config           Config          `json:"config"`
	Limits           Limits          `json:"limits"`
	Reason           string          `json:"reason"`
	Warnings         []string        `json:"warnings"`
	SupplyMinutes    *float64        `json:"supply_minutes"`
	SupplyCapped     bool            `json:"supply_capped"`
	Coverage         float64         `json:"coverage"`
	Usable           int             `json:"usable"`
	ReportedCapacity int             `json:"reported_capacity"`
	Capacity         float64         `json:"capacity"`
	Used             int             `json:"used"`
	Waiting          int             `json:"waiting"`
	Inflight         int             `json:"inflight"`
	LeadMinutes      float64         `json:"lead_minutes"`
	TriggerMinutes   float64         `json:"trigger_minutes"`
	TargetMinutes    float64         `json:"target_minutes"`
	TimeNeed         int             `json:"time_need"`
	ConcurrencyNeed  int             `json:"concurrency_need"`
	ReserveNeed      int             `json:"reserve_need"`
	EmptyGroupNeed   int             `json:"empty_group_need"`
	Suggested        int             `json:"suggested"`
	Started          int             `json:"started"`
	Confirmations    int             `json:"confirmations"`
}

// EmptyGroupOnly identifies an explicit startup quantity, which must not be
// rounded up to the ordinary minimum batch size.
func (d Decision) EmptyGroupOnly() bool {
	return d.EmptyGroupNeed > 0 && d.ExitNeed == 0 && d.ForceMergeNeed == 0 && d.TimeNeed == 0 && d.ConcurrencyNeed == 0 && d.ReserveNeed == 0
}
