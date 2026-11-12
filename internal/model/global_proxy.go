package model

import "errors"

type GlobalProxyPolicy struct {
	BackupIDs           []string `json:"backup_ids"`
	CooldownSeconds     int      `json:"cooldown_seconds"`
	TestIntervalSeconds int      `json:"test_interval_seconds"`
	RecoveryTests       *int     `json:"recovery_tests"`
}

func DefaultGlobalProxyPolicy() GlobalProxyPolicy {
	n := 3
	return GlobalProxyPolicy{BackupIDs: []string{}, CooldownSeconds: 300, TestIntervalSeconds: 5, RecoveryTests: &n}
}
func (p GlobalProxyPolicy) Normalized() GlobalProxyPolicy {
	if p.CooldownSeconds == 0 {
		d := DefaultGlobalProxyPolicy()
		d.BackupIDs = p.BackupIDs
		return d
	}
	if p.TestIntervalSeconds == 0 {
		p.TestIntervalSeconds = 5
	}
	return p
}
func (p GlobalProxyPolicy) Validate() error {
	p = p.Normalized()
	if p.CooldownSeconds < 10 || p.CooldownSeconds > 3600 {
		return errors.New("代理故障冷却须为 10～3600 秒")
	}
	if p.RecoveryTests != nil && (*p.RecoveryTests < 1 || *p.RecoveryTests > 10) {
		return errors.New("恢复测试须为 1～10 次，留空表示到期直接恢复")
	}
	if p.TestIntervalSeconds < 1 || p.TestIntervalSeconds > 300 {
		return errors.New("恢复测试间隔须为 1～300 秒")
	}
	if len(p.BackupIDs) > 49 {
		return errors.New("备用代理最多 49 条")
	}
	return nil
}

type GlobalProxyHealth struct {
	Key        string `json:"-"`
	State      string `json:"state"`
	Until      int64  `json:"cooldown_until"`
	Attempts   int    `json:"test_attempts"`
	CheckedAt  int64  `json:"checked_at"`
	Message    string `json:"message,omitempty"`
	Generation int64  `json:"-"`
	NextAt     int64  `json:"-"`
}
