package sub2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"chatgpt-space-merge/internal/model"
)

// ExistingAccount exposes identity and membership only. OAuth secrets returned
// by the admin endpoint are deliberately never decoded or copied to Space.
type ExistingAccount struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Platform string  `json:"platform"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	GroupIDs []int64 `json:"group_ids"`
	Groups   []Group `json:"groups"`
	Extra    struct {
		AuthProvider   string          `json:"auth_provider"`
		SpaceCompleted json.RawMessage `json:"space_completion"`
	} `json:"extra"`
	Credentials struct {
		Email          string `json:"email"`
		AuthMode       string `json:"auth_mode"`
		LegacyAuthMode string `json:"openai_auth_mode"`
		WebAccountID   string `json:"siwc_web_account_id"`
		AccountID      string `json:"chatgpt_account_id"`
	} `json:"credentials"`
}

func (a ExistingAccount) SIWC() bool {
	return a.Extra.AuthProvider == "siwc" || a.Credentials.AuthMode == "siwc" || a.Credentials.LegacyAuthMode == "siwc"
}

func (a ExistingAccount) matches(v model.Sub2Settings, email string) bool {
	if a.ID < 1 || a.Platform != "openai" || a.Type != "oauth" || a.Status != "active" || !strings.EqualFold(strings.TrimSpace(a.Credentials.Email), strings.TrimSpace(email)) || a.SIWC() != (v.AuthMode == "siwc") {
		return false
	}
	for _, id := range v.GroupIDs {
		found := slices.Contains(a.GroupIDs, id)
		for _, g := range a.Groups {
			if g.ID == id {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Search narrows the remote scan; exact credential email and all configured
// groups are still checked locally. Never infer identity from a display name.
func (c *Client) FindExistingAccount(ctx context.Context, v model.Sub2Settings, password, email string) (ExistingAccount, error) {
	var found []ExistingAccount
	seen := map[int64]bool{}
	total, count := -1, 0
	for page := 1; page <= 20; page++ {
		path := fmt.Sprintf("/api/v1/admin/accounts?platform=openai&search=%s&page=%d&page_size=100", url.QueryEscape(email), page)
		raw, err := c.doJSON(ctx, v, password, http.MethodGet, path, nil, nil)
		if err != nil {
			return ExistingAccount{}, err
		}
		var result struct {
			Items []ExistingAccount `json:"items"`
			Total *int              `json:"total"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			return ExistingAccount{}, err
		}
		if result.Total == nil || *result.Total < 0 || result.Items == nil || total >= 0 && total != *result.Total {
			return ExistingAccount{}, errors.New("Sub2 账号列表不完整或已变化，请重试关联")
		}
		total = *result.Total
		for _, a := range result.Items {
			if a.ID < 1 || seen[a.ID] {
				return ExistingAccount{}, errors.New("Sub2 账号分页重复或 ID 无效")
			}
			seen[a.ID] = true
			count++
			if a.matches(v, email) {
				found = append(found, a)
			}
		}
		if count == total {
			break
		}
		if count > total || len(result.Items) == 0 || page == 20 {
			return ExistingAccount{}, errors.New("Sub2 账号列表不完整，未进行关联")
		}
	}
	if len(found) == 0 {
		return ExistingAccount{}, errors.New("当前 Sub 及所选分组中没有邮箱完全匹配的有效 OAuth 账号，请检查账号、授权方式和分组")
	}
	if len(found) != 1 {
		return ExistingAccount{}, errors.New("当前分组有多个同邮箱账号，请在 Sub 清理重复账号后再关联")
	}
	raw, err := c.doJSON(ctx, v, password, http.MethodGet, fmt.Sprintf("/api/v1/admin/accounts/%d", found[0].ID), nil, nil)
	if err != nil {
		return ExistingAccount{}, err
	}
	var fresh ExistingAccount
	if err = json.Unmarshal(raw, &fresh); err != nil {
		return fresh, err
	}
	if fresh.ID != found[0].ID || !fresh.matches(v, email) {
		return ExistingAccount{}, errors.New("Sub 账号身份或分组已变化，未进行关联")
	}
	return fresh, nil
}
