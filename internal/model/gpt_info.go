package model

import "time"

// GPTInfoRequestStatus describes one read-only upstream query, without credentials.
type GPTInfoRequestStatus struct {
	OK          bool   `json:"ok"`
	HTTPStatus  int    `json:"http_status,omitempty"`
	Attempts    int    `json:"attempts"`
	Error       string `json:"error,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	CFMitigated string `json:"cf_mitigated,omitempty"`
	CFRay       string `json:"cf_ray,omitempty"`
}

type GPTInfoCheck struct {
	Status     string               `json:"status"`
	CheckedAt  time.Time            `json:"checked_at"`
	PlanSource string               `json:"plan_source,omitempty"`
	Plan       GPTInfoRequestStatus `json:"plan"`
	Me         GPTInfoRequestStatus `json:"me"`
	Error      string               `json:"error,omitempty"`
}

type MailGPTInfoResult struct {
	Check           GPTInfoCheck           `json:"check"`
	Plan            AccountPlanCheckResult `json:"plan"`
	CreatedAtOpenAI *time.Time             `json:"created_at_openai,omitempty"`
}
