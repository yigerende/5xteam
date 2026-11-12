package model

import (
	"errors"
	"time"
)

// Zero keeps the existing permanent binding behavior.
func (s Sub2Settings) ValidateProxyRetention() error {
	if s.ProxyReleaseMinutes < 0 || s.ProxyReleaseMinutes > 43200 {
		return errors.New("代理定时解绑须为 1～43200 分钟，0 表示一直绑定")
	}
	return nil
}

// No credentials are exposed by the binding status API. TargetID refers to an
// encrypted connection snapshot; it is independent of current push settings.
type Sub2ProxyBinding struct {
	ID         string     `json:"id"`
	Endpoint   string     `json:"endpoint,omitempty"`
	TargetID   string     `json:"target_id,omitempty"`
	Scope      string     `json:"scope,omitempty"`
	AccountID  int64      `json:"account_id"`
	ProxyID    int64      `json:"proxy_id"`
	ProxyName  string     `json:"proxy_name"`
	Minutes    int        `json:"minutes"`
	BoundAt    time.Time  `json:"bound_at"`
	DueAt      *time.Time `json:"due_at,omitempty"`
	State      string     `json:"state"`
	Attempts   int        `json:"attempts"`
	Error      string     `json:"error,omitempty"`
	CheckedAt  *time.Time `json:"checked_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	NextAt     int64      `json:"-"`
	Revision   int64      `json:"-"`
}
