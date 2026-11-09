package model

import (
	"errors"
	"time"
)

// Pro uses its own connection and policy; Team rotation settings are independent.
type ProQualitySettings struct {
	Enabled             bool             `json:"enabled"`
	PollIntervalSeconds int              `json:"poll_interval_seconds"`
	MaxResultAgeSeconds int              `json:"max_result_age_seconds"`
	ErrorFailureLimit   int              `json:"error_failure_limit"`
	QuestionEnabled     bool             `json:"question_enabled"`
	ModelAuditEnabled   bool             `json:"model_audit_enabled"`
	ConditionMode       string           `json:"condition_mode"`
	Action              string           `json:"action"`
	PauseBeforeAction   bool             `json:"pause_before_action"`
	NormalGroupIDs      []int64          `json:"normal_group_ids"`
	DegradedGroupIDs    []int64          `json:"degraded_group_ids"`
	AutoRestore         bool             `json:"auto_restore"`
	RecoveryLimit       int              `json:"recovery_limit"`
	Concurrency         int              `json:"concurrency"`
	Revision            string           `json:"revision"`
	Downstream          ProDownstreamRef `json:"downstream"`
}

func DefaultProQualitySettings() ProQualitySettings {
	return ProQualitySettings{PollIntervalSeconds: 60, MaxResultAgeSeconds: 900, ErrorFailureLimit: 2, QuestionEnabled: true,
		ConditionMode: "any", Action: "merge", RecoveryLimit: 2, Concurrency: 4,
		NormalGroupIDs: []int64{}, DegradedGroupIDs: []int64{}}
}

func (q ProQualitySettings) DecisionSettings() QualitySettings {
	v := DefaultQualitySettings()
	v.Enabled, v.QuestionEnabled, v.ModelAuditEnabled = q.Enabled, q.QuestionEnabled, q.ModelAuditEnabled
	v.PollIntervalSeconds, v.MaxResultAgeSeconds, v.ConditionMode = q.PollIntervalSeconds, q.MaxResultAgeSeconds, q.ConditionMode
	v.NormalGroupIDs, v.DegradedGroupIDs = q.NormalGroupIDs, q.DegradedGroupIDs
	v.AutoRestore, v.RecoveryLimit, v.Concurrency = q.AutoRestore, q.RecoveryLimit, q.Concurrency
	v.Action = q.Action
	if v.Action == "merge" || v.Action == "siwc" {
		v.Action = "kick" // Only reuse validation/decision rules, never the Team action.
	}
	return v
}

func (q ProQualitySettings) Validate() error {
	if q.ErrorFailureLimit < 1 || q.ErrorFailureLimit > 20 {
		return errors.New("连续检测失败次数应为 1～20 次")
	}
	if q.Action != "merge" && q.Action != "groups" && q.Action != "siwc" {
		return errors.New("降智处理请选择切换分组或直接合并")
	}
	if q.PollIntervalSeconds < 10 || q.MaxResultAgeSeconds < 30 {
		return errors.New("查询间隔至少 10 秒，结果有效期至少 30 秒")
	}
	return q.DecisionSettings().Validate()
}

type ProQualityState struct {
	AccountQuality
	ErrorFailures     int                        `json:"error_failures"`
	ErrorFailureLimit int                        `json:"error_failure_limit"`
	ErrorSample       string                     `json:"error_sample,omitempty"`
	ErrorDegraded     bool                       `json:"error_degraded"`
	ActionError       string                     `json:"action_error,omitempty"`
	ValidUntil        *time.Time                 `json:"valid_until,omitempty"`
	FetchedAt         *time.Time                 `json:"fetched_at,omitempty"`
	MergeAttempts     int                        `json:"merge_attempts"`
	NextMergeAt       *time.Time                 `json:"next_merge_at,omitempty"`
	ConnectionCheck   *ProQualityConnectionCheck `json:"connection_check,omitempty"`
}

type ProQualityConnectionCheck struct {
	CheckedAt  time.Time `json:"checked_at"`
	Sample     string    `json:"sample"`
	Status     string    `json:"status"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Error      string    `json:"error,omitempty"`
}

func (p *MailAccountProfile) ReconcileProQualityMerge() {
	if p.ProQuality.Action == "merge" && p.MergeV2Started() {
		p.ProQuality.ActionStatus, p.ProQuality.ActionError = p.ProMergeV2.Status, p.ProMergeV2.Error
		p.ProQuality.NextMergeAt = nil
		return
	}
	if p.ProQuality.Action == "merge" && p.MerchantStarted() {
		p.ProQuality.ActionStatus, p.ProQuality.ActionError = "pending", p.ProMerchant.Error
		if p.MerchantCompleted() {
			p.ProQuality.ActionStatus = "completed"
		}
		if p.ProMerchant.Status == "failed" {
			p.ProQuality.ActionStatus = "failed"
		}
		return
	}

	if p.ProQuality.Action == "merge" && p.DeletionAccepted() && !p.ProPostMergeGroupsPending() && !p.ProVM2CleanupPending() {
		p.ProQuality.ActionStatus, p.ProQuality.ActionError = "completed", ""
		p.ProQuality.NextMergeAt = nil
		return
	}
	if p.ProQuality.Action == "merge" && p.TransferStatus == "completed" &&
		(p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed") && !p.ProPostMergeGroupsPending() {
		p.ProQuality.ActionStatus, p.ProQuality.ActionError = "completed", ""
		p.ProQuality.NextMergeAt = nil
	}
}
