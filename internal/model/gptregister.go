package model

import "time"

type GPTRegisterJob struct {
	ID              string    `json:"id"`
	PlatformURL     string    `json:"platform_url"`
	Quantity        int       `json:"quantity"`
	NeedOAuth       bool      `json:"need_oauth"`
	EnterPro        bool      `json:"enter_pro"`
	State           string    `json:"state"`
	SubmitAttempted bool      `json:"submit_attempted"`
	RemoteUserID    string    `json:"remote_user_id,omitempty"`
	RemoteOrderID   string    `json:"remote_order_id,omitempty"`
	RemoteStatus    string    `json:"remote_status,omitempty"`
	Delivered       int       `json:"delivered"`
	Imported        int       `json:"imported"`
	Skipped         int       `json:"skipped"`
	UnitPrice       int64     `json:"unit_price"`
	Charged         int64     `json:"charged"`
	Released        int64     `json:"released"`
	Failures        int       `json:"failures"`
	Message         string    `json:"message"`
	NextAt          time.Time `json:"next_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
