package model

import "time"

type ProMerchant struct {
	Provider      string     `json:"provider,omitempty"`
	SuccessFeeUSD string     `json:"success_fee_usd"`
	FeeOrderID    string     `json:"fee_order_id,omitempty"`
	FeeCardID     string     `json:"fee_card_id,omitempty"`
	FeeIssuer     string     `json:"fee_issuer,omitempty"`
	RequestID     string     `json:"request_id"`
	JobID         string     `json:"job_id,omitempty"`
	BatchID       string     `json:"batch_id,omitempty"`
	Status        string     `json:"status"`
	RemoteStatus  string     `json:"remote_status,omitempty"`
	Message       string     `json:"message,omitempty"`
	Error         string     `json:"error,omitempty"`
	Attempts      int        `json:"attempts"`
	StartedAt     time.Time  `json:"started_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	AcceptedAt    *time.Time `json:"accepted_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	NextCheckAt   *time.Time `json:"next_check_at,omitempty"`
}

func (p MailAccountProfile) MerchantStarted() bool {
	return p.ProMerchant != nil && p.ProMerchant.RequestID != ""
}
func (p MailAccountProfile) MerchantCompleted() bool {
	return p.ProMerchant != nil && p.ProMerchant.CompletedAt != nil
}
func (p MailAccountProfile) MerchantAccepted() bool {
	return p.ProMerchant != nil && p.ProMerchant.AcceptedAt != nil
}
func (p MailAccountProfile) DispositionStarted() bool {
	return p.DeletionSubmitted() || p.MerchantStarted() || p.MergeV2Started()
}
func (p MailAccountProfile) MerchantNeedsContinuation() bool {
	return p.MerchantStarted() && !p.MerchantCompleted() && p.ProMerchant.Status != "failed"
}
func (p *MailAccountProfile) ReconcileProMerchant(now time.Time) bool {
	m := p.ProMerchant
	if !p.MerchantStarted() {
		return false
	}
	changed := false
	if (p.MerchantCompleted() || m.Status == "failed" || p.ProAuto.StopRequested) && m.NextCheckAt != nil {
		m.NextCheckAt = nil
		changed = true
	}
	status, step := "awaiting_merchant", "waiting"
	if p.MerchantCompleted() {
		status, step = "completed", "completed"
	} else if m.Error != "" {
		step = "failed"
	}
	if m.Status == "failed" {
		status = "failed"
	}
	if p.ProAuto.StopRequested {
		status = "stopped"
	}
	if p.ProAuto.ID == "" {
		return changed
	}
	changed = changed || p.ProAuto.Status != status || p.ProAuto.Stage != "merchant" || p.ProAuto.Error != m.Error || p.ProAuto.NextCheckAt != nil || p.ProAuto.Steps["merchant"] != step
	if p.ProAuto.Steps == nil {
		p.ProAuto.Steps = map[string]string{}
	}
	p.ProAuto.Status, p.ProAuto.Stage, p.ProAuto.Error = status, "merchant", m.Error
	p.ProAuto.Steps["merchant"] = step
	p.ProAuto.NextCheckAt, p.ProAuto.RetryAt, p.ProForceMergeNextAt = nil, nil, nil
	if changed {
		p.ProAuto.UpdatedAt = now
	}
	return changed
}
