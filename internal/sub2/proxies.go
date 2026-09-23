package sub2

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Proxy deliberately excludes the administrator API's username/password fields.
type Proxy struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Protocol     string     `json:"protocol"`
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	Status       string     `json:"status"`
	ExpiresAt    *time.Time `json:"expires_at"`
	AccountCount *int       `json:"account_count"`
}

func (p Proxy) Available(now time.Time) bool {
	return p.ID > 0 && p.Status == "active" && p.AccountCount != nil && *p.AccountCount >= 0 && (p.ExpiresAt == nil || p.ExpiresAt.After(now))
}

func (c *Client) Proxies(ctx context.Context, settings model.Sub2Settings, password string) ([]Proxy, error) {
	data, err := c.doJSON(ctx, settings, password, http.MethodGet, "/api/v1/admin/proxies/all?with_count=true", nil, nil)
	if err != nil {
		return nil, err
	}
	proxies := []Proxy{}
	if err := unmarshalList(data, &proxies); err != nil {
		return nil, fmt.Errorf("解析 Sub2 代理失败: %w", err)
	}
	return proxies, nil
}

// SelectProxy uses the server's current count of all undeleted bound accounts.
func SelectProxy(proxies []Proxy, ids []int64, now time.Time) (Proxy, int, error) {
	selected := map[int64]bool{}
	for _, id := range ids {
		if id > 0 {
			selected[id] = true
		}
	}
	if len(selected) == 0 {
		return Proxy{}, 0, errors.New("已开启绑定代理，请至少选择一个 Sub2 代理")
	}
	candidates := []Proxy{}
	seen := map[int64]bool{}
	for _, p := range proxies {
		if !selected[p.ID] || seen[p.ID] || !p.Available(now) {
			continue
		}
		seen[p.ID] = true
		if len(candidates) == 0 || *p.AccountCount < *candidates[0].AccountCount {
			candidates = []Proxy{p}
		} else if *p.AccountCount == *candidates[0].AccountCount {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return Proxy{}, 0, errors.New("所选 Sub2 代理均不可用、已过期或缺少绑定数量，已停止推送")
	}
	return candidates[rand.IntN(len(candidates))], len(candidates), nil
}

func ValidateProxySettings(settings model.Sub2Settings) error {
	if settings.BindProxy && len(uniquePositiveIDs(settings.ProxyIDs)) == 0 {
		return errors.New("已开启绑定代理，请至少选择一个 Sub2 代理")
	}
	return nil
}

// EndpointKey also folds host case and default ports for the shared creation lock.
func EndpointKey(raw string) (string, error) {
	base, err := normalizeBaseURL(raw)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(base)
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("Sub2 地址不能包含凭据、查询参数或片段")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return strings.TrimRight(u.String(), "/"), nil
}

type HTTPError struct {
	Status        int
	Path, Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Sub2 %s HTTP %d: %s", e.Path, e.Status, e.Message)
}
