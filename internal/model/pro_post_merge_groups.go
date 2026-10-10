package model

import "time"

// Snapshot the target when removal starts. Retries update only this account on
// the same downstream and never repeat successful OpenAI removal operations.
type ProPostMergeGroups struct {
	Status     string           `json:"status"`
	GroupIDs   []int64          `json:"group_ids"`
	AccountID  int64            `json:"account_id"`
	Downstream ProDownstreamRef `json:"downstream"`
	Error      string           `json:"error,omitempty"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

func (p MailAccountProfile) ProPostMergeGroupsPending() bool {
	return p.ProPostMergeGroups != nil && p.ProPostMergeGroups.Status != "completed"
}
