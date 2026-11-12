package model

import (
	"errors"
	"math"
	"time"
)

// AuthMode selects SIWC within the Sub2 transport; connections and credentials
// remain independent from the ordinary Pro push destination.
type ProSIWCSettings struct {
	Sub2              Sub2Settings `json:"sub2"`
	MonitorGroupIDs   []int64      `json:"monitor_group_ids"`
	FiveHourEnabled   bool         `json:"five_hour_enabled"`
	FiveHourRemaining float64      `json:"five_hour_remaining"`
	SevenDayEnabled   bool         `json:"seven_day_enabled"`
	SevenDayRemaining float64      `json:"seven_day_remaining"`
	PollSeconds       int          `json:"poll_seconds"`
}

func DefaultProSIWCSettings() ProSIWCSettings {
	s := ProSIWCSettings{Sub2: DefaultSub2Settings(), MonitorGroupIDs: []int64{}, FiveHourEnabled: true, SevenDayEnabled: true, PollSeconds: 120}
	s.Sub2.AuthMode = "siwc"
	return s
}
func (s ProSIWCSettings) Validate() error {
	if err := s.Sub2.ValidateProxyRetention(); err != nil {
		return err
	}
	if s.PollSeconds < 10 || s.PollSeconds > 86400 {
		return errors.New("SIWC 检查间隔应为 10～86400 秒")
	}
	for _, v := range []float64{s.FiveHourRemaining, s.SevenDayRemaining} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
			return errors.New("SIWC 剩余额度阈值应为 0～100%")
		}
	}
	for _, id := range s.MonitorGroupIDs {
		if id < 1 {
			return errors.New("SIWC 监控分组无效")
		}
	}
	return nil
}

type ProSIWCState struct {
	MonitorGroupIDs     []int64    `json:"monitor_group_ids,omitempty"`
	RemoveGroupIDs      []int64    `json:"remove_group_ids,omitempty"`
	SourceCostCheckedAt *time.Time `json:"source_cost_checked_at,omitempty"`
	Status              string     `json:"status"` // queued, authorizing, pushing, warming, active, failed, ended
	Stage               string     `json:"stage"`
	Error               string     `json:"error,omitempty"`
	SourceTargetID      string     `json:"source_target_id,omitempty"`
	TargetID            string     `json:"target_id"`
	SourceAccountID     int64      `json:"source_account_id,omitempty"`
	AccountID           int64      `json:"account_id,omitempty"`
	SourceGroupIDs      []int64    `json:"source_group_ids,omitempty"`
	StartedAt           time.Time  `json:"started_at"`
	ReadyAt             *time.Time `json:"ready_at,omitempty"`
	RetryAt             *time.Time `json:"retry_at,omitempty"`
	EndedAt             *time.Time `json:"ended_at,omitempty"`
	Attempts            int        `json:"attempts"`
	CarriedCost         float64    `json:"carried_cost"`
	CarriedUserCost     float64    `json:"carried_user_cost"`
	BaselineCost        float64    `json:"baseline_cost"`
	BaselineUserCost    float64    `json:"baseline_user_cost"`
	NewCost             float64    `json:"new_cost"`
	NewUserCost         float64    `json:"new_user_cost"`
}

func (p MailAccountProfile) SIWCTransferring() bool {
	if p.ProSIWC == nil || p.DispositionStarted() || p.SpaceMergedOnce {
		return false
	}
	return p.ProSIWC.Status != "active" && p.ProSIWC.Status != "ended"
}
