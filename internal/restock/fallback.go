package restock

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// FallbackConfig contains no credentials. It is separate from replenishment decisions.
type FallbackConfig struct {
	Rules           []FallbackRule   `json:"rules"`
	Version         int              `json:"version"`
	Enabled         bool             `json:"enabled"`
	URL             string           `json:"url"`
	Email           string           `json:"email"`
	PasswordPresent bool             `json:"password_present"`
	GroupID         int64            `json:"group_id"`
	GroupIDs        []int64          `json:"group_ids"`
	AccountIDs      []int64          `json:"account_ids"`
	AccountNames    map[int64]string `json:"account_names,omitempty"`
	CheckSeconds    int              `json:"check_seconds"`
}

// Nil rules retain the legacy binding to the replenishment monitor. An explicit
// empty list means no rules and must never restore old account selections.
type FallbackRule struct {
	Enabled        bool    `json:"enabled"`
	MonitorGroupID int64   `json:"monitor_group_id"`
	GroupIDs       []int64 `json:"group_ids"`
	AccountIDs     []int64 `json:"account_ids"`
}

func (c FallbackConfig) EffectiveRules(monitor int64) []FallbackRule {
	if c.Rules != nil {
		return c.Rules
	}
	return []FallbackRule{{Enabled: true, MonitorGroupID: monitor, GroupIDs: c.SelectedGroupIDs(), AccountIDs: c.AccountIDs}}
}

func (c FallbackConfig) RuleConfig(r FallbackRule) FallbackConfig {
	c.Rules = nil
	c.Enabled = c.Enabled && r.Enabled
	c.GroupID, c.GroupIDs, c.AccountIDs = 0, r.GroupIDs, r.AccountIDs
	return c
}

func DefaultFallbackConfig() FallbackConfig {
	return FallbackConfig{Version: 1, AccountIDs: []int64{}, CheckSeconds: 3}
}

// A missing list reads legacy single-group configurations. An explicit empty
// list means deselected and must not restore the old group_id.
func (c FallbackConfig) SelectedGroupIDs() []int64 {
	if c.GroupIDs != nil {
		return c.GroupIDs
	}
	if c.GroupID > 0 {
		return []int64{c.GroupID}
	}
	return []int64{}
}

func (c *FallbackConfig) NormalizeGroups() {
	if c.Rules != nil {
		c.GroupIDs, c.AccountIDs = []int64{}, []int64{}
		for _, r := range c.Rules {
			if !r.Enabled {
				continue
			}
			for _, id := range r.GroupIDs {
				if !slices.Contains(c.GroupIDs, id) {
					c.GroupIDs = append(c.GroupIDs, id)
				}
			}
			for _, id := range r.AccountIDs {
				if !slices.Contains(c.AccountIDs, id) {
					c.AccountIDs = append(c.AccountIDs, id)
				}
			}
		}
	}
	c.GroupIDs = append([]int64{}, c.SelectedGroupIDs()...)
	c.GroupID = 0
	if len(c.GroupIDs) > 0 {
		c.GroupID = c.GroupIDs[0]
	}
}

func (c FallbackConfig) Validate() error {
	if c.Rules != nil {
		if len(c.Rules) > 50 {
			return errors.New("兜底规则最多设置 50 条")
		}
		seen := map[int64]bool{}
		for i, r := range c.Rules {
			if r.MonitorGroupID < 0 || (c.Enabled && r.Enabled && r.MonitorGroupID == 0) {
				return fmt.Errorf("兜底规则 %d：请选择监控分组", i+1)
			}
			if r.MonitorGroupID > 0 && seen[r.MonitorGroupID] {
				return errors.New("同一监控分组请配置在一条规则内")
			}
			seen[r.MonitorGroupID] = true
			row := c.RuleConfig(r)
			if err := row.Validate(); err != nil {
				return fmt.Errorf("兜底规则 %d：%w", i+1, err)
			}
		}
		c.NormalizeGroups()
	}
	if c.CheckSeconds < 2 || c.CheckSeconds > 300 {
		return errors.New("兜底检查／失败重试周期应为 2～300 秒")
	}
	if c.URL != "" || c.Enabled {
		u, err := url.Parse(c.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("请输入有效的 Sub2 HTTP(S) 地址，不含认证、查询参数或片段")
		}
		if strings.TrimSpace(c.Email) == "" {
			return errors.New("请输入兜底 Sub2 管理员邮箱")
		}
	}
	groups := c.SelectedGroupIDs()
	if c.Rules == nil && c.Enabled && (len(groups) == 0 || len(c.AccountIDs) == 0) {
		return errors.New("请选择兜底分组及至少一个 OpenAI 账号")
	}
	if len(groups) > 100 {
		return errors.New("兜底分组最多选择 100 个")
	}
	seenGroups := map[int64]bool{}
	for _, id := range groups {
		if id <= 0 || seenGroups[id] {
			return errors.New("兜底分组 ID 无效或重复")
		}
		seenGroups[id] = true
	}
	if len(c.AccountIDs) > 1000 {
		return errors.New("兜底账号最多选择 1000 个")
	}
	seen := map[int64]bool{}
	for _, id := range c.AccountIDs {
		if id <= 0 || seen[id] {
			return errors.New("兜底账号 ID 无效或重复")
		}
		seen[id] = true
	}
	return nil
}

// FallbackObservation is a complete account-list sample, never an in-flight task count.
type FallbackObservation struct {
	GroupID   int64     `json:"group_id"`
	GroupName string    `json:"group_name"`
	Count     *int      `json:"count"`
	At        time.Time `json:"at"`
	Error     string    `json:"error,omitempty"`
}
type FallbackPlan struct {
	Active bool   `json:"active"`
	Hold   bool   `json:"hold"`
	Phase  string `json:"phase"`
	Reason string `json:"reason"`
}

// Ordinary empty-group fallback remains independent of replenishment decisions.
func (f FallbackConfig) Plan(now time.Time, o FallbackObservation) FallbackPlan {
	if !f.Enabled {
		return FallbackPlan{Phase: "disabled", Reason: "兜底未启用；关闭已接管账号调度"}
	}
	if f.Validate() != nil {
		return FallbackPlan{Hold: true, Phase: "unknown", Reason: "兜底配置无效，保持当前调度"}
	}
	if o.Error != "" || o.GroupID <= 0 || o.Count == nil || *o.Count < 0 || o.At.IsZero() || o.At.After(now.Add(time.Second)) || now.Sub(o.At) > time.Duration(max(10, f.CheckSeconds*2))*time.Second {
		return FallbackPlan{Hold: true, Phase: "unknown", Reason: "主监控组数据待确认，保持当前调度并按检查周期重试"}
	}
	if *o.Count == 0 {
		return FallbackPlan{Active: true, Phase: "active", Reason: "主监控分组没有 OAuth 账号：开启所选兜底账号调度"}
	}
	return FallbackPlan{Phase: "off", Reason: fmt.Sprintf("主监控分组有 %d 个 OAuth 账号：关闭所选兜底账号调度", *o.Count)}
}
