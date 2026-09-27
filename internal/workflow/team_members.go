package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type TeamMember struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	SeatType    string `json:"seat_type"`
	CreatedTime string `json:"created_time"`
	Active      bool   `json:"active"`
}

type TeamMemberPage struct {
	Items   []TeamMember `json:"items"`
	Total   int          `json:"total"`
	Offset  int          `json:"offset"`
	Limit   int          `json:"limit"`
	HasMore bool         `json:"has_more"`
}

// ListTeamMembers reads one live page through the mother's seat-query transport.
// Only display fields are returned; credentials and arbitrary upstream data are not exposed.
func (c *Client) ListTeamMembers(ctx context.Context, adminToken, teamID, query string, offset, limit int) (TeamMemberPage, error) {
	page := TeamMemberPage{Items: []TeamMember{}, Total: -1, Offset: offset, Limit: limit}
	if strings.TrimSpace(teamID) == "" || offset < 0 || limit < 1 || limit > 25 {
		return page, errors.New("母号成员查询参数无效")
	}
	params := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}, "query": {strings.TrimSpace(query)}}
	value, err := c.getJSON(ctx, "/accounts/"+url.PathEscape(teamID)+"/users?"+params.Encode(), adminToken, teamID)
	if err != nil {
		return page, err
	}
	items := jsonItems(value)
	if items == nil {
		return page, errors.New("母号成员查询响应缺少成员列表")
	}
	for _, item := range items {
		str := func(key string) string { v, _ := item[key].(string); return strings.TrimSpace(v) }
		email := str("email")
		if email == "" {
			email = str("email_address")
		}
		deactivated, isString := item["deactivated_time"].(string)
		page.Items = append(page.Items, TeamMember{
			ID: str("id"), Email: email, Name: str("name"), Role: str("role"),
			SeatType: strings.ToLower(str("seat_type")), CreatedTime: str("created_time"),
			Active: item["deactivated_time"] == nil || (isString && strings.TrimSpace(deactivated) == ""),
		})
	}
	if total, ok := value["total"].(float64); ok && total >= 0 {
		page.Total = int(total)
	}
	page.HasMore = len(items) >= limit
	if page.Total >= 0 {
		page.HasMore = len(items) > 0 && offset+len(items) < page.Total
	}
	return page, nil
}

// FindMemberByEmail uses the same authenticated browser transport as the
// mother's seat query. The server-side search is fuzzy, so only an exact email
// match is evidence of membership. A pending invitation is never evidence.
func (c *Client) FindMemberByEmail(ctx context.Context, adminToken, teamID, email string) (*TeamMember, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("子号邮箱不能为空")
	}
	for offset := 0; offset < 1000; offset += 25 {
		page, err := c.ListTeamMembers(ctx, adminToken, teamID, email, offset, 25)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if !strings.EqualFold(item.Email, email) {
				continue
			}
			if item.ID == "" {
				return nil, errors.New("母号成员查询匹配到邮箱，但缺少成员 ID")
			}
			return &item, nil
		}
		if !page.HasMore {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("母号成员查询超过分页上限，未确认子号 %s", email)
}
