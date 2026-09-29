package workflow

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

type HeldSeatMember struct {
	ID                  string `json:"id"`
	Email               string `json:"email"`
	SeatType            string `json:"seat_type"`
	ReclaimableSeatType string `json:"reclaimable_seat_type"`
	DeactivatedTime     string `json:"deactivated_time"`
}

func (c *Client) HeldSeatMembers(ctx context.Context, token, teamID, email string) ([]HeldSeatMember, error) {
	out := []HeldSeatMember{}
	for offset := 0; offset < 10000; offset += 25 {
		params := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {"25"}, "query": {email}, "active_vacancy_hold_seat_type": {"prolite"}}
		value, err := c.getJSON(ctx, "/accounts/"+url.PathEscape(teamID)+"/users?"+params.Encode(), token, teamID)
		if err != nil {
			return nil, err
		}
		items := jsonItems(value)
		if items == nil {
			return nil, errors.New("临停席位响应缺少成员列表")
		}
		for _, item := range items {
			str := func(key string) string { v, _ := item[key].(string); return strings.TrimSpace(v) }
			p := HeldSeatMember{ID: str("id"), Email: str("email"), SeatType: str("seat_type"), ReclaimableSeatType: str("reclaimable_seat_type"), DeactivatedTime: str("deactivated_time")}
			if p.Email == "" {
				p.Email = str("email_address")
			}
			if p.ID == "" || p.Email == "" {
				return nil, errors.New("临停成员身份不完整")
			}
			// Do not trust the filter alone when upstream changes its behavior.
			if p.ReclaimableSeatType == "prolite" && p.DeactivatedTime != "" && (email == "" || strings.EqualFold(p.Email, email)) {
				out = append(out, p)
			}
		}
		if len(items) < 25 {
			return out, nil
		}
		if total, ok := value["total"].(float64); ok && offset+len(items) >= int(total) {
			return out, nil
		}
	}
	return nil, errors.New("临停席位超过分页上限，未返回不完整扫描结果")
}

// This strict read never estimates availability from members or invitations.
// Seat changes must not silently purchase a new ordinary seat.
func (c *Client) RecoverySeatCapacity(ctx context.Context, token, teamID string) (model.AdminSeatCapacity, error) {
	var out model.AdminSeatCapacity
	v, err := c.getJSON(ctx, "/subscriptions?account_id="+url.QueryEscape(teamID), token, teamID)
	if err != nil {
		return out, err
	}
	entries, ok := v["seat_capacity"].([]any)
	if !ok {
		return out, errors.New("无法确认实际可用普通席位")
	}
	found := map[string]bool{}
	for _, raw := range entries {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := p["type"].(string)
		if typ != "default" && typ != "prolite" {
			continue
		}
		for _, key := range []string{"paid", "held", "available"} {
			n, ok := p[key].(float64)
			if !ok || n < 0 {
				return out, errors.New("席位容量数据不完整")
			}
		}
		found[typ] = true
	}
	if !found["default"] || !found["prolite"] {
		return out, errors.New("缺少普通或 5x 席位容量")
	}
	parseSubscriptionCapacity(&out, v)
	out.FetchedAt = time.Now()
	return out, nil
}

func (c *Client) SwitchRecoverySeat(ctx context.Context, token, teamID, userID, flowID, mutationID string) (Response, error) {
	if userID == "" || flowID == "" || mutationID == "" {
		return Response{}, errors.New("席位切换参数不完整")
	}
	return c.doWithOptions(ctx, http.MethodPost, "/accounts/"+url.PathEscape(teamID)+"/users/"+url.PathEscape(userID)+"/seat/update", token, teamID, map[string]any{"operation": "switch", "seat_type": "default", "flow_id": flowID, "mutation_attempt_id": mutationID}, requestOptions{recoverySeat: true})
}
