package model

import "time"

type Settings struct {
	BaseURL                string `json:"base_url"`
	AcceptedTOSVersion     string `json:"accepted_tos_version"`
	Role                   string `json:"role"`
	SeatType               string `json:"seat_type"`
	InviteDelaySeconds     int    `json:"invite_delay_seconds"`
	AcceptDelaySeconds     int    `json:"accept_delay_seconds"`
	TransferDelaySeconds   int    `json:"transfer_delay_seconds"`
	AccountIntervalSeconds int    `json:"account_interval_seconds"`
	RequestTimeoutSeconds  int    `json:"request_timeout_seconds"`
	Concurrency            int    `json:"concurrency"`
	ProxyURL               string `json:"proxy_url"`
	AutoCleanup            bool   `json:"auto_cleanup"`
	StopOnFirstFailure     bool   `json:"stop_on_first_failure"`
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

type AdminAccountTestResult struct {
	Valid      bool      `json:"valid"`
	HTTPStatus int       `json:"http_status,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	Message    string    `json:"message"`
	User       UserInfo  `json:"user"`
	CheckedAt  time.Time `json:"checked_at"`
}

func DefaultSettings() Settings {
	return Settings{
		BaseURL: "https://chatgpt.com/backend-api", AcceptedTOSVersion: "2024-12-17",
		Role: "standard-user", SeatType: "default", InviteDelaySeconds: 3,
		AcceptDelaySeconds: 2, TransferDelaySeconds: 5, RequestTimeoutSeconds: 45,
		Concurrency: 2, AutoCleanup: true,
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
	Email  string `json:"email"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
