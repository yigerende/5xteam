package model

import "time"

type Settings struct {
	BaseURL                string `json:"base_url"`
	AcceptedTOSVersion     string `json:"accepted_tos_version"`
	Role                   string `json:"role"`
	InviteDelaySeconds     int    `json:"invite_delay_seconds"`
	AcceptDelaySeconds     int    `json:"accept_delay_seconds"`
	TransferDelaySeconds   int    `json:"transfer_delay_seconds"`
	AccountIntervalSeconds int    `json:"account_interval_seconds"`
	RequestTimeoutSeconds  int    `json:"request_timeout_seconds"`
	NetworkRetryCount      int    `json:"network_retry_count"`
	NetworkRetryInterval   int    `json:"network_retry_interval_seconds"`
	Concurrency            int    `json:"concurrency"`
	ProxyURL               string `json:"proxy_url"`
	SMSProvider            string `json:"sms_provider"`
	AllowSMS               bool   `json:"allow_sms"`
	AutoCleanup            bool   `json:"auto_cleanup"`
	StopOnFirstFailure     bool   `json:"stop_on_first_failure"`
}

// MailAccountProfile is the local mailbox projection used by registration.
// Secrets are encrypted in Store and never returned by list APIs.
type MailAccountProfile struct {
	ID                    string           `json:"id"`
	Email                 string           `json:"email"`
	Label                 string           `json:"label"`
	Group                 string           `json:"group"`
	ManagementScope       string           `json:"management_scope,omitempty"`
	ProManagedAt          *time.Time       `json:"pro_managed_at,omitempty"`
	MailPasswordPresent   bool             `json:"mail_password_present"`
	ClientIDPresent       bool             `json:"client_id_present"`
	MailRefreshPresent    bool             `json:"mail_refresh_token_present"`
	PickupURLPresent      bool             `json:"pickup_url_present"`
	GptPasswordPresent    bool             `json:"gpt_password_present"`
	TotpSecretPresent     bool             `json:"totp_secret_present"`
	AccessTokenPresent    bool             `json:"access_token_present"`
	RefreshTokenPresent   bool             `json:"refresh_token_present"`
	IDTokenPresent        bool             `json:"id_token_present"`
	OAuthStatus           string           `json:"oauth_status,omitempty"`
	OAuthAccountID        string           `json:"oauth_account_id,omitempty"`
	OAuthUserID           string           `json:"oauth_user_id,omitempty"`
	OAuthExpiresAt        *time.Time       `json:"oauth_expires_at,omitempty"`
	OAuthAuthorizedAt     *time.Time       `json:"oauth_authorized_at,omitempty"`
	PushProvider          string           `json:"push_provider,omitempty"`
	PushStatus            string           `json:"push_status,omitempty"`
	Sub2AccountID         int64            `json:"sub2_account_id,omitempty"`
	Sub2AccountName       string           `json:"sub2_account_name,omitempty"`
	CPAAuthFileName       string           `json:"cpa_auth_file_name,omitempty"`
	Quota5H               *FreeQuotaWindow `json:"quota_5h,omitempty"`
	Quota7D               *FreeQuotaWindow `json:"quota_7d,omitempty"`
	QuotaStatus           string           `json:"quota_status,omitempty"`
	QuotaCheckedAt        *time.Time       `json:"quota_checked_at,omitempty"`
	SpaceMergedOnce       bool             `json:"space_merged_once"`
	SpaceMergedAt         *time.Time       `json:"space_merged_at,omitempty"`
	TargetAdminID         string           `json:"target_admin_id,omitempty"`
	TargetTeamID          string           `json:"target_team_id,omitempty"`
	TargetSeatType        string           `json:"target_seat_type,omitempty"`
	InviteStatus          string           `json:"pro_invite_status,omitempty"`
	AcceptStatus          string           `json:"pro_accept_status,omitempty"`
	TransferStatus        string           `json:"pro_transfer_status,omitempty"`
	RemoveStatus          string           `json:"pro_remove_status,omitempty"`
	ProLastError          string           `json:"pro_last_error,omitempty"`
	ProWorkflowRunning    bool             `json:"pro_workflow_running"`
	ChatGPTSessionPresent bool             `json:"chatgpt_session_present"`
	ATCheckedAt           *time.Time       `json:"at_checked_at,omitempty"`
	ATValid               bool             `json:"at_valid"`
	ATCheckHTTPStatus     int              `json:"at_check_http_status,omitempty"`
	ATCheckMessage        string           `json:"at_check_message,omitempty"`
	ChatGPTStatus         string           `json:"chatgpt_status,omitempty"`
	ChatGPTStatusMessage  string           `json:"chatgpt_status_message,omitempty"`
	ChatGPTStatusAt       *time.Time       `json:"chatgpt_status_at,omitempty"`
	CurrentPlanType       string           `json:"current_plan_type,omitempty"`
	SubscriptionPlan      string           `json:"subscription_plan,omitempty"`
	HasActiveSubscription bool             `json:"has_active_subscription"`
	PlusTrialEligible     bool             `json:"plus_trial_eligible"`
	PlanCheckStatus       string           `json:"plan_check_status,omitempty"`
	PlanCheckedAt         *time.Time       `json:"plan_checked_at,omitempty"`
	PlanLastSuccessAt     *time.Time       `json:"plan_last_success_at,omitempty"`
	PlanCheckHTTPStatus   int              `json:"plan_check_http_status,omitempty"`
	PlanCheckError        string           `json:"plan_check_error,omitempty"`
	PlanExpiresAt         string           `json:"plan_expires_at,omitempty"`
	PlanRenewsAt          string           `json:"plan_renews_at,omitempty"`
	BillingPeriod         string           `json:"billing_period,omitempty"`
	BillingCurrency       string           `json:"billing_currency,omitempty"`
	LoginMethod           string           `json:"login_method,omitempty"`
	RegistrationStatus    string           `json:"registration_status"`
	RegistrationLastError string           `json:"registration_last_error,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

// AccountPlanCheckResult is the normalized subset of ChatGPT's accounts/check
// response needed by the Pro account list.
type AccountPlanCheckResult struct {
	OK                    bool      `json:"ok"`
	HTTPStatus            int       `json:"http_status,omitempty"`
	AccountID             string    `json:"account_id,omitempty"`
	CurrentPlanType       string    `json:"current_plan_type,omitempty"`
	SubscriptionPlan      string    `json:"subscription_plan,omitempty"`
	HasActiveSubscription bool      `json:"has_active_subscription"`
	PlusTrialEligible     bool      `json:"plus_trial_eligible"`
	ExpiresAt             string    `json:"expires_at,omitempty"`
	RenewsAt              string    `json:"renews_at,omitempty"`
	BillingPeriod         string    `json:"billing_period,omitempty"`
	BillingCurrency       string    `json:"billing_currency,omitempty"`
	CheckedAt             time.Time `json:"checked_at"`
	AttemptCount          int       `json:"attempt_count"`
	Error                 string    `json:"error,omitempty"`
}

type MailAccountCredentials struct {
	Email            string `json:"email"`
	MailPassword     string `json:"mail_password"`
	ClientID         string `json:"client_id"`
	MailRefreshToken string `json:"mail_refresh_token"`
	PickupURL        string `json:"pickup_url"`
	GptPassword      string `json:"gpt_password"`
	TotpSecret       string `json:"totp_secret"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ChatGPTSession   string `json:"chatgpt_session"`
}

type MailMessage struct {
	ID         string    `json:"id"`
	Account    string    `json:"account"`
	Source     string    `json:"source,omitempty"`
	Folder     string    `json:"folder,omitempty"`
	MID        string    `json:"mid,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	From       string    `json:"from,omitempty"`
	Text       string    `json:"text,omitempty"`
	Snippet    string    `json:"snippet,omitempty"`
	Code       string    `json:"code,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type ProxyProfile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProxyTestResult struct {
	Reachable  bool      `json:"reachable"`
	HTTPStatus int       `json:"http_status,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	Message    string    `json:"message"`
	CheckedAt  time.Time `json:"checked_at"`
}

type AdminAccountProfile struct {
	ID                     string     `json:"id"`
	Label                  string     `json:"label"`
	Email                  string     `json:"email"`
	Name                   string     `json:"name"`
	UserID                 string     `json:"user_id"`
	AccountID              string     `json:"account_id"`
	TeamAccountID          string     `json:"team_account_id"`
	PlanType               string     `json:"plan_type"`
	TokenPresent           bool       `json:"token_present"`
	RefreshTokenPresent    bool       `json:"refresh_token_present"`
	AccessTokenExpiresAt   *time.Time `json:"access_token_expires_at,omitempty"`
	LastRefreshedAt        *time.Time `json:"last_refreshed_at,omitempty"`
	TeamRotationChildCount int        `json:"team_rotation_child_count"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// AdminSeatCapacity is the live seat snapshot for a Team workspace. The
// upstream API has changed field names over time, so the HTTP layer
// normalizes those variants into these stable fields.
type AdminSeatBucket struct {
	Total     int `json:"total"`
	Used      int `json:"used"`
	Remaining int `json:"remaining"`
}

type AdminSeatCapacity struct {
	Standard  AdminSeatBucket `json:"standard"`
	Premium   AdminSeatBucket `json:"premium"`
	FetchedAt time.Time       `json:"fetched_at"`
}

type AdminAccountTestResult struct {
	Valid      bool      `json:"valid"`
	HTTPStatus int       `json:"http_status,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	Message    string    `json:"message"`
	User       UserInfo  `json:"user"`
	CheckedAt  time.Time `json:"checked_at"`
}

// OpenAIAccountProfile is a persisted OpenAI/Codex account used by the
// quota-management screen. The access token itself is never included here;
// it is encrypted separately by the store and only used during a request.
type OpenAIAccountProfile struct {
	ID                    string     `json:"id"`
	Label                 string     `json:"label"`
	Email                 string     `json:"email"`
	Name                  string     `json:"name"`
	UserID                string     `json:"user_id"`
	AccountID             string     `json:"account_id"`
	PlanType              string     `json:"plan_type"`
	TokenPresent          bool       `json:"token_present"`
	RefreshTokenPresent   bool       `json:"refresh_token_present"`
	AccessTokenExpiresAt  *time.Time `json:"access_token_expires_at,omitempty"`
	LastCheckedAt         *time.Time `json:"last_checked_at,omitempty"`
	LastCheckValid        bool       `json:"last_check_valid"`
	LastCheckHTTPStatus   int        `json:"last_check_http_status,omitempty"`
	LastCheckMessage      string     `json:"last_check_message,omitempty"`
	ResetCredits          *int       `json:"reset_credits,omitempty"`
	ResetCreditsFetchedAt *time.Time `json:"reset_credits_fetched_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type OpenAIAccountCredentials struct {
	AccessToken  string
	RefreshToken string
}

func DefaultSettings() Settings {
	return Settings{
		BaseURL: "https://chatgpt.com/backend-api", AcceptedTOSVersion: "2024-12-17",
		Role: "standard-user", InviteDelaySeconds: 3,
		AcceptDelaySeconds: 2, TransferDelaySeconds: 5, RequestTimeoutSeconds: 45,
		NetworkRetryCount: 2, NetworkRetryInterval: 3,
		Concurrency: 2, AllowSMS: true, AutoCleanup: true,
	}
}

type UserInfo struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
	PlanType  string `json:"plan_type"`
}

type Step struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	HTTPStatus  int        `json:"http_status,omitempty"`
	Message     string     `json:"message,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type AccountResult struct {
	Index       int        `json:"index"`
	User        UserInfo   `json:"user"`
	Status      string     `json:"status"`
	CurrentStep string     `json:"current_step,omitempty"`
	Error       string     `json:"error,omitempty"`
	Steps       []Step     `json:"steps"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Job struct {
	ID            string          `json:"id"`
	Operation     string          `json:"operation"`
	Status        string          `json:"status"`
	TeamAccountID string          `json:"team_account_id"`
	Admin         UserInfo        `json:"admin"`
	Total         int             `json:"total"`
	Completed     int             `json:"completed"`
	Succeeded     int             `json:"succeeded"`
	Failed        int             `json:"failed"`
	Results       []AccountResult `json:"results"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}

type HistoryEntry struct {
	ID            string          `json:"id"`
	Operation     string          `json:"operation"`
	Status        string          `json:"status"`
	TeamAccountID string          `json:"team_account_id"`
	AdminEmail    string          `json:"admin_email"`
	Total         int             `json:"total"`
	Succeeded     int             `json:"succeeded"`
	Failed        int             `json:"failed"`
	CreatedAt     time.Time       `json:"created_at"`
	CompletedAt   time.Time       `json:"completed_at"`
	Results       []HistoryResult `json:"results"`
}

type HistoryResult struct {
	UserID         string   `json:"user_id,omitempty"`
	Email          string   `json:"email"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
	CompletedSteps []string `json:"completed_steps,omitempty"`
}

type AccountProgress struct {
	TeamAccountID string     `json:"team_account_id"`
	AdminEmail    string     `json:"admin_email"`
	UserID        string     `json:"user_id"`
	Email         string     `json:"email"`
	EnteredAt     *time.Time `json:"entered_at,omitempty"`
	TransferredAt *time.Time `json:"transferred_at,omitempty"`
	RemovedAt     *time.Time `json:"removed_at,omitempty"`
	LastOperation string     `json:"last_operation"`
	LastStatus    string     `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type AutoRotationSettings struct {
	Enabled          bool    `json:"enabled"`
	ThresholdPercent float64 `json:"threshold_percent"`
	IntervalSeconds  int     `json:"interval_seconds"`
	Concurrency      int     `json:"concurrency"`
	MaxPerRun        int     `json:"max_per_run"`
	RetryCount       int     `json:"retry_count"`
}

func DefaultAutoRotationSettings() AutoRotationSettings {
	return AutoRotationSettings{ThresholdPercent: 50, IntervalSeconds: 300, Concurrency: 2, MaxPerRun: 0, RetryCount: 1}
}

type AutoRotationRun struct {
	ID                     string     `json:"id"`
	Trigger                string     `json:"trigger"`
	Reason                 string     `json:"reason"`
	AveragePercent         float64    `json:"average_percent"`
	SeatTotal              int        `json:"seat_total"`
	SeatRemaining          int        `json:"seat_remaining"`
	ReservedSeats          int        `json:"reserved_seats"`
	Planned                int        `json:"planned"`
	Succeeded              int        `json:"succeeded"`
	Failed                 int        `json:"failed"`
	Status                 string     `json:"status"`
	StartedAt              time.Time  `json:"started_at"`
	CompletedAt            *time.Time `json:"completed_at,omitempty"`
	SpaceAccountCount      int        `json:"space_account_count"`
	QuotaAccountCount      int        `json:"quota_account_count"`
	CandidateRotationCount int        `json:"candidate_rotation_count"`
	CandidateMailCount     int        `json:"candidate_mail_count"`
	DecisionAvailableSeats int        `json:"decision_available_seats"`
	DecisionMaxPerRun      int        `json:"decision_max_per_run"`
}

type AutoRotationTask struct {
	ID              string             `json:"id"`
	RunID           string             `json:"run_id"`
	AccountID       string             `json:"account_id"`
	Email           string             `json:"email"`
	Source          string             `json:"source"`
	AdminAccountID  string             `json:"admin_account_id"`
	SeatType        string             `json:"seat_type"`
	CurrentStep     string             `json:"current_step"`
	Status          string             `json:"status"`
	InviteTriggered bool               `json:"invite_triggered"`
	SeatReserved    bool               `json:"seat_reserved"`
	ReservationID   string             `json:"reservation_id,omitempty"`
	RetryCount      int                `json:"retry_count"`
	Error           string             `json:"error,omitempty"`
	StartedAt       time.Time          `json:"started_at"`
	CompletedAt     *time.Time         `json:"completed_at,omitempty"`
	Steps           []AutoRotationStep `json:"steps,omitempty"`
	// Lifecycle marks the durable account history task created when an account
	// enters Team rotation. It is not an executable auto-rotation task.
	Lifecycle bool `json:"lifecycle,omitempty"`
}

type AutoRotationStep struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Message     string     `json:"message,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type AutoRotationEvent struct {
	ID             string         `json:"id"`
	RunID          string         `json:"run_id,omitempty"`
	TaskID         string         `json:"task_id,omitempty"`
	AccountID      string         `json:"account_id,omitempty"`
	Email          string         `json:"email,omitempty"`
	AdminAccountID string         `json:"admin_account_id,omitempty"`
	Type           string         `json:"type"`
	Source         string         `json:"source,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Operation      string         `json:"operation,omitempty"`
	Stage          string         `json:"stage,omitempty"`
	FromStatus     string         `json:"from_status,omitempty"`
	ToStatus       string         `json:"to_status,omitempty"`
	HTTPStatus     int            `json:"http_status,omitempty"`
	Attempt        int            `json:"attempt,omitempty"`
	DurationMS     int64          `json:"duration_ms,omitempty"`
	Message        string         `json:"message,omitempty"`
	Request        map[string]any `json:"request,omitempty"`
	Response       map[string]any `json:"response,omitempty"`
	Level          string         `json:"level,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}
