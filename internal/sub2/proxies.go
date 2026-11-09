package sub2

import (
	"context"
	"crypto/sha256"
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
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Protocol           string     `json:"protocol"`
	Host               string     `json:"host"`
	Port               int        `json:"port"`
	Status             string     `json:"status"`
	ExpiresAt          *time.Time `json:"expires_at"`
	AccountCount       *int       `json:"account_count"`
	BindingKey         string     `json:"binding_key"`
	CumulativeBindings int64      `json:"cumulative_bindings"`
	PendingBindings    int64      `json:"pending_bindings"`
	BindingRevision    int64      `json:"binding_revision"`
	BindingLimit       int64      `json:"binding_limit"`
	BindingSince       string     `json:"binding_since,omitempty"`
}

func (p Proxy) Available(now time.Time) bool {
	return p.ID > 0 && p.Status == "active" && p.AccountCount != nil && *p.AccountCount >= 0 && (p.ExpiresAt == nil || p.ExpiresAt.After(now)) && (p.BindingLimit == 0 || (p.BindingKey != "" && p.CumulativeBindings+p.PendingBindings < p.BindingLimit))
}

// Names, upstream IDs and passwords can change during a reimport. Preserve
// identity using the connection endpoint and username, without exposing it.
func ProxyBindingKey(protocol, host string, port int, username string) string {
	if strings.TrimSpace(host) == "" || port <= 0 || port > 65535 {
		return ""
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "http"
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]")), ".")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", protocol, host, port, username))))
}

func (c *Client) Proxies(ctx context.Context, settings model.Sub2Settings, password string) ([]Proxy, error) {
	data, err := c.doJSON(ctx, settings, password, http.MethodGet, "/api/v1/admin/proxies/all?with_count=true", nil, nil)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Proxy
		Username string `json:"username"`
	}
	if err := unmarshalList(data, &rows); err != nil {
		return nil, fmt.Errorf("解析 Sub2 代理失败: %w", err)
	}
	proxies := make([]Proxy, 0, len(rows))
	for _, row := range rows {
		p := row.Proxy
		p.BindingKey = ProxyBindingKey(p.Protocol, p.Host, p.Port, row.Username)
		p.CumulativeBindings, p.PendingBindings, p.BindingRevision, p.BindingLimit = 0, 0, 0, 0
		p.BindingSince = ""
		proxies = append(proxies, p)
	}
	return proxies, nil
}

// SelectProxy balances current accounts first, then lifetime bindings, with
// random choice only when both counts tie. Unresolved reservations count toward
// both totals so an unconfirmed create does not make a busy proxy look unused.
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
	var leastCurrent, leastLifetime int64
	seen := map[int64]bool{}
	for _, p := range proxies {
		if !selected[p.ID] || seen[p.ID] || !p.Available(now) {
			continue
		}
		seen[p.ID] = true
		current := int64(*p.AccountCount) + p.PendingBindings
		lifetime := p.CumulativeBindings + p.PendingBindings
		if len(candidates) == 0 || current < leastCurrent || (current == leastCurrent && lifetime < leastLifetime) {
			candidates = []Proxy{p}
			leastCurrent, leastLifetime = current, lifetime
		} else if current == leastCurrent && lifetime == leastLifetime {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return Proxy{}, 0, errors.New("所选 Sub2 代理均不可用、已过期、缺少绑定数量或累计绑定已达上限，已停止推送")
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
