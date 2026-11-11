package sub2

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// MonitorAccount deliberately excludes tokens and raw credential payloads.
type MonitorAccount struct {
	Type             string     `json:"type"`
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Platform         string     `json:"platform"`
	Status           string     `json:"status"`
	Schedulable      bool       `json:"schedulable"`
	Concurrency      int        `json:"concurrency"`
	GroupIDs         []int64    `json:"group_ids"`
	Groups           []Group    `json:"groups"`
	ExpiresAt        *int64     `json:"expires_at"`
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil    *time.Time `json:"overload_until"`
	TempUntil        *time.Time `json:"temp_unschedulable_until"`
	Credentials      struct {
		Plan   string            `json:"plan_type"`
		Models map[string]string `json:"model_mapping"`
	} `json:"credentials"`
}

// Empty-group startup counts membership, not schedulability or local Pro state.
// Disabled OAuth members still exist; API-key accounts are not OAuth supply.
func CountGroupOAuth(accounts []MonitorAccount, group int64) (int, error) {
	count := 0
	for _, a := range accounts {
		member := false
		for _, id := range a.GroupIDs {
			if id == group {
				member = true
			}
		}
		for _, g := range a.Groups {
			if g.ID == group {
				member = true
			}
		}
		if !member {
			continue
		}
		if a.Platform != "openai" {
			return 0, errors.New("监控分组账号平台不明确，不能判定 OAuth 空组")
		}
		switch a.Type {
		case "oauth":
			count++
		case "apikey":
		default:
			return 0, errors.New("监控分组账号类型不明确，不能判定 OAuth 空组")
		}
	}
	return count, nil
}

func (a MonitorAccount) Ready(now time.Time, models []string) bool {
	if a.Platform != "openai" || a.Status != "active" || !a.Schedulable || a.Concurrency < 1 || (a.ExpiresAt != nil && *a.ExpiresAt <= now.Unix()) {
		return false
	}
	for _, t := range []*time.Time{a.RateLimitResetAt, a.OverloadUntil, a.TempUntil} {
		if t != nil && t.After(now) {
			return false
		}
	}
	if len(a.Credentials.Models) > 0 {
		for _, m := range models {
			m = strings.TrimSpace(m)
			supported := false
			for pattern := range a.Credentials.Models {
				// Match Sub2's exact or trailing-star model whitelist semantics.
				if m != "" && (pattern == m || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(m, strings.TrimSuffix(pattern, "*")))) {
					supported = true
					break
				}
			}
			if !supported {
				return false
			}
		}
	}
	return true
}
func (c *Client) MonitorAccounts(ctx context.Context, v model.Sub2Settings, password string, group int64) ([]MonitorAccount, error) {
	out := []MonitorAccount{}
	seen := map[int64]bool{}
	expected := -1
	for page := 1; page <= 100; page++ {
		// Account lists use group; ops/concurrency uses group_id.
		raw, e := c.doJSON(ctx, v, password, http.MethodGet, fmt.Sprintf("/api/v1/admin/accounts?platform=openai&group=%d&page=%d&page_size=100", group, page), nil, nil)
		if e != nil {
			return nil, e
		}
		var result struct {
			Items []MonitorAccount `json:"items"`
			Total *int             `json:"total"`
		}
		if e = json.Unmarshal(raw, &result); e != nil {
			return nil, e
		}
		if result.Total == nil {
			return nil, errors.New("Sub2 账号分页缺少总数")
		}
		if result.Items == nil || *result.Total < 0 {
			return nil, errors.New("Sub2 账号分页缺少有效列表或总数无效")
		}
		if expected >= 0 && *result.Total != expected {
			return nil, errors.New("Sub2 账号总数在采集期间变化，请重新采样")
		}
		expected = *result.Total
		if len(result.Items) == 0 && len(out) < *result.Total {
			return nil, errors.New("Sub2 账号分页不完整")
		}
		for _, a := range result.Items {
			if a.ID < 1 || seen[a.ID] {
				return nil, errors.New("Sub2 账号分页重复或 ID 无效")
			}
			seen[a.ID] = true
			if len(a.GroupIDs) == 0 {
				for _, g := range a.Groups {
					a.GroupIDs = append(a.GroupIDs, g.ID)
				}
			}
			member := false
			for _, id := range a.GroupIDs {
				if id == group {
					member = true
				}
			}
			if !member {
				return nil, errors.New("Sub2 返回了不属于监控组的账号")
			}
			out = append(out, a)
		}
		if len(out) == *result.Total {
			return out, nil
		}
		if len(out) > *result.Total {
			return nil, errors.New("Sub2 账号总数在采集期间变化，请重新采样")
		}
	}
	return nil, errors.New("Sub2 账号分页超出采集上限")
}

type MonitorLoad struct {
	AccountID int64 `json:"account_id"`
	Used      int   `json:"current_in_use"`
	Waiting   int   `json:"waiting_in_queue"`
	Capacity  int   `json:"max_capacity"`
	Known     *bool `json:"load_known"`
}

// Missing counters must not be interpreted as a legitimate zero load.
// load_known is an optional extension; native Sub2 responses omit it.
func (l *MonitorLoad) UnmarshalJSON(data []byte) error {
	var v struct {
		AccountID *int64 `json:"account_id"`
		Used      *int   `json:"current_in_use"`
		Waiting   *int   `json:"waiting_in_queue"`
		Capacity  *int   `json:"max_capacity"`
		Known     *bool  `json:"load_known"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.AccountID == nil || v.Used == nil || v.Waiting == nil || v.Capacity == nil {
		return errors.New("Sub2 账号负载缺少有效账号 ID、占用、等待或容量字段")
	}
	*l = MonitorLoad{AccountID: *v.AccountID, Used: *v.Used, Waiting: *v.Waiting, Capacity: *v.Capacity, Known: v.Known}
	return nil
}

type MonitorLoads struct {
	Enabled bool                  `json:"enabled"`
	At      time.Time             `json:"timestamp"`
	Account map[int64]MonitorLoad `json:"account"`
	Quality *struct {
		Complete  bool `json:"complete"`
		Expected  int  `json:"expected"`
		Succeeded int  `json:"succeeded"`
		Failed    int  `json:"failed"`
	} `json:"quality"`
}

func (c *Client) MonitorConcurrency(ctx context.Context, v model.Sub2Settings, password string, group int64) (MonitorLoads, error) {
	var result MonitorLoads
	raw, e := c.doJSON(ctx, v, password, http.MethodGet, fmt.Sprintf("/api/v1/admin/ops/concurrency?platform=openai&group_id=%d", group), nil, nil)
	if e != nil {
		return result, e
	}
	e = json.Unmarshal(raw, &result)
	if e == nil && !result.Enabled {
		e = errors.New("Sub2 实时监控未启用")
	}
	if e == nil && result.Account == nil {
		e = errors.New("Sub2 实时监控缺少有效账号负载列表")
	}
	return result, e
}
