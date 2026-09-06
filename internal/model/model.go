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
	ID                    string    `json:"id"`
	Email                 string    `json:"email"`
	Label                 string    `json:"label"`
	Group                 string    `json:"group"`
	MailPasswordPresent   bool      `json:"mail_password_present"`
	ClientIDPresent       bool      `json:"client_id_present"`
	MailRefreshPresent    bool      `json:"mail_refresh_token_present"`
	PickupURLPresent      bool      `json:"pickup_url_present"`
	GptPasswordPresent    bool      `json:"gpt_password_present"`
	TotpSecretPresent     bool      `json:"totp_secret_present"`
	AccessTokenPresent    bool      `json:"access_token_present"`
	RefreshTokenPresent   bool      `json:"refresh_token_present"`
	ATCheckedAt           *time.Time `json:"at_checked_at,omitempty"`
	ATValid               bool      `json:"at_valid"`
	ATCheckHTTPStatus     int       `json:"at_check_http_status,omitempty"`
	ATCheckMessage        string    `json:"at_check_message,omitempty"`
	LoginMethod           string    `json:"login_method,omitempty"`
	RegistrationStatus    string    `json:"registration_status"`
	RegistrationLastError string    `json:"registration_last_error,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
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
	ID                   string     `json:"id"`
	Label                string     `json:"label"`
	Email                string     `json:"email"`
	Name                 string     `json:"name"`
	UserID               string     `json:"user_id"`
	AccountID            string     `json:"account_id"`
	TeamAccountID        string     `json:"team_account_id"`
	PlanType             string     `json:"plan_type"`
	TokenPresent         bool       `json:"token_present"`
	RefreshTokenPresent  bool       `json:"refresh_token_present"`
	AccessTokenExpiresAt *time.Time `json:"access_token_expires_at,omitempty"`
	LastRefreshedAt      *time.Time `json:"last_refreshed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
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
