package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func mergeRowString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mergeRowIDMatches(row map[string]any, id string) bool {
	if id == "" {
		return false
	}
	for _, key := range []string{"id", "user_id", "chatgpt_user_id"} {
		if row[key] == id {
			return true
		}
	}
	return false
}

// Only an unfiltered, completely paginated roster can establish absence.
func (c *Client) findMergeV2Target(ctx context.Context, token, team, email, userID, kind string) (map[string]any, error) {
	if strings.TrimSpace(email) == "" || team == "" {
		return nil, errors.New("移出复查缺少目标身份")
	}
	offset := 0
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		path := fmt.Sprintf("/accounts/%s/%s?offset=%d&limit=25&query=", url.PathEscape(team), kind, offset)
		if kind == "invites" {
			path += "&include_pending=true&include_requests=true"
		}
		v, err := c.getJSON(ctx, path, token, team)
		if err != nil {
			return nil, err
		}
		var rows []any
		for _, key := range []string{"items", "users", "members", "invites", "account_invites", "data"} {
			if items, ok := v[key].([]any); ok {
				rows = items
				break
			}
		}
		if rows == nil {
			return nil, errors.New("移出复查响应缺少成员或邀请列表")
		}
		total, hasTotal := v["total"].(float64)
		if _, exists := v["total"]; exists && (!hasTotal || total < float64(offset+len(rows)) || total != float64(int(total))) {
			return nil, errors.New("移出复查总数无效")
		}
		more, hasMore := v["has_more"].(bool)
		if _, exists := v["has_more"]; exists && !hasMore {
			return nil, errors.New("移出复查分页标识无效")
		}
		if hasMore && hasTotal && more != (float64(offset+len(rows)) < total) {
			return nil, errors.New("移出复查分页标识冲突")
		}
		identities := make([][]string, 0, len(rows))
		var found map[string]any
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("移出复查列表存在无效记录")
			}
			e := mergeRowString(row, "email", "email_address")
			if e == "" {
				return nil, errors.New("移出复查身份数据不完整")
			}
			identities = append(identities, []string{e, mergeRowString(row, "id"), mergeRowString(row, "user_id"), mergeRowString(row, "chatgpt_user_id")})
			if strings.EqualFold(e, email) || kind == "users" && mergeRowIDMatches(row, userID) {
				found = row
			}
		}
		fingerprint, _ := json.Marshal(identities)
		if len(rows) > 0 && seen[string(fingerprint)] {
			return nil, errors.New("移出复查分页重复，不能确认已移出")
		}
		seen[string(fingerprint)] = true
		if found != nil {
			return found, nil
		}
		offset += len(rows)
		if more || hasTotal && float64(offset) < total {
			if len(rows) == 0 {
				return nil, errors.New("移出复查分页数据不完整")
			}
			continue
		}
		if hasMore || hasTotal || len(rows) < 25 {
			return nil, nil
		}
	}
	return nil, errors.New("移出复查超过分页上限")
}

func (c *Client) MergeV2TargetPresent(ctx context.Context, token, team, email string) (bool, error) {
	for _, kind := range []string{"users", "invites"} {
		row, err := c.findMergeV2Target(ctx, token, team, email, "", kind)
		if err != nil {
			return false, err
		}
		if row != nil {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) ConfirmMergeV2Removed(ctx context.Context, token, team, email, userID string) error {
	for _, kind := range []string{"users", "invites"} {
		row, err := c.findMergeV2Target(ctx, token, team, email, userID, kind)
		if err != nil {
			return err
		}
		if row != nil {
			return errors.New("本单子号仍在原团队成员或邀请列表中")
		}
	}
	return nil
}

// A DELETE response is not evidence of absence. Before a child ID is available,
// cleanup may only derive it from an exact email match and ordinary-member role.
func (c *Client) RemoveMergeV2Target(ctx context.Context, token, team, email, userID string, allowUnidentified bool) error {
	remove := func() error {
		row, err := c.findMergeV2Target(ctx, token, team, email, userID, "users")
		if err != nil || row == nil {
			return err
		}
		role := mergeRowString(row, "role")
		if !strings.EqualFold(mergeRowString(row, "email", "email_address"), email) || (role != "standard-user" && role != "member" && role != "user") {
			return errors.New("原团队成员身份或权限不匹配，禁止移出")
		}
		id := userID
		if id == "" && allowUnidentified {
			id = mergeRowString(row, "user_id", "id", "chatgpt_user_id")
		}
		if id == "" || !mergeRowIDMatches(row, id) {
			return errors.New("原团队子号ID不匹配，禁止移出")
		}
		_, err = c.Kick(ctx, token, team, id)
		return err
	}
	removeErr := remove()
	invitation, err := c.findMergeV2Target(ctx, token, team, email, userID, "invites")
	if err != nil {
		return errors.Join(removeErr, err)
	}
	if invitation != nil {
		role := mergeRowString(invitation, "role")
		if !strings.EqualFold(mergeRowString(invitation, "email", "email_address"), email) || mergeRowString(invitation, "id") == "" || (role != "standard-user" && role != "member" && role != "user") {
			return errors.New("原团队邀请身份或权限不匹配，禁止撤销")
		}
		_, err = c.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(team)+"/invites", token, team, map[string]any{"email_address": email})
		removeErr = errors.Join(removeErr, err, remove())
	}
	if err = c.ConfirmMergeV2Removed(ctx, token, team, email, userID); err != nil {
		return errors.Join(removeErr, err)
	}
	return nil
}
