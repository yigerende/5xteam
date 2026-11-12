package sub2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"chatgpt-space-merge/internal/model"
)

var ErrProxyAccountDeleted = errors.New("Sub2 确认账号已删除")

// A missing proxy field is not proof of successful unbinding. Decode explicitly
// rather than treating a missing numeric value as zero.
func (c *Client) AccountProxy(ctx context.Context, v model.Sub2Settings, password string, id int64) (int64, error) {
	if id <= 0 {
		return 0, errors.New("Sub2 账号 ID 无效")
	}
	raw, err := c.doJSON(ctx, v, password, http.MethodGet, fmt.Sprintf("/api/v1/admin/accounts/%d", id), nil, nil)
	if err != nil {
		var e *HTTPError
		if errors.As(err, &e) && e.Status == 404 {
			message := strings.ToLower(strings.TrimSpace(e.Message))
			var detail struct {
				Message string `json:"message"`
				Reason  string `json:"reason"`
			}
			if json.Unmarshal([]byte(e.Message), &detail) == nil {
				message = strings.ToLower(strings.TrimSpace(detail.Message))
			}
			if message == "account not found" || message == "账号不存在" {
				return 0, ErrProxyAccountDeleted
			}
		}
		return 0, err
	}
	var a struct {
		ID    int64           `json:"id"`
		Proxy json.RawMessage `json:"proxy_id"`
	}
	if err = json.Unmarshal(raw, &a); err != nil {
		return 0, errors.New("Sub2 代理绑定响应无法解析")
	}
	if a.ID != id || len(a.Proxy) == 0 {
		return 0, errors.New("Sub2 代理绑定响应缺少账号或代理字段")
	}
	if string(a.Proxy) == "null" {
		return 0, nil
	}
	var proxy int64
	if err = json.Unmarshal(a.Proxy, &proxy); err != nil || proxy < 0 {
		return 0, errors.New("Sub2 代理绑定字段无效")
	}
	return proxy, nil
}

func (c *Client) ClearAccountProxy(ctx context.Context, v model.Sub2Settings, password string, id int64) error {
	if id <= 0 {
		return errors.New("Sub2 账号 ID 无效")
	}
	_, err := c.doJSON(ctx, v, password, http.MethodPut, fmt.Sprintf("/api/v1/admin/accounts/%d", id), map[string]any{"proxy_id": 0}, nil)
	return err
}
