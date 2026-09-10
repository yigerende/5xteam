package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/workflow"
)

type oauthProxyLease struct {
	url         string
	key         string
	label       string
	activeCount int
	releaseOnce sync.Once
	releaseFn   func()
}

func (l *oauthProxyLease) Release() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		if l.releaseFn != nil {
			l.releaseFn()
		}
	})
}

func normalizeOAuthProxyMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "global"
	}
	return value
}

// acquireOAuthProxy reserves one proxy for an entire OAuth attempt. The
// lease is process-local because OAuth workers are process-local as well.
func (s *Server) acquireOAuthProxy() (*oauthProxyLease, error) {
	return s.acquireOAuthProxyExcluding(nil)
}

// acquireOAuthProxyExcluding selects the least-used configured line while
// avoiding lines that already failed in the current OAuth login round. If all
// lines are excluded, the full pool is considered again so a single-line pool
// can still retry with a new sticky session when its provider supports it.
func (s *Server) acquireOAuthProxyExcluding(excluded map[string]struct{}) (*oauthProxyLease, error) {
	settings := s.store.Settings()
	mode := normalizeOAuthProxyMode(settings.OAuthProxyMode)
	if mode == "global" {
		proxyURL := strings.TrimSpace(settings.ProxyURL)
		if proxyURL == "" {
			return nil, errors.New("请先配置全局代理；OpenAI OAuth 禁止直连")
		}
		return s.reserveOAuthProxy(proxyURL, "全局代理"), nil
	}

	type candidate struct {
		url   string
		label string
	}
	profiles := s.store.Proxies()
	candidates := make([]candidate, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		proxyURL := strings.TrimSpace(profile.URL)
		if proxyURL == "" {
			continue
		}
		if _, exists := seen[proxyURL]; exists {
			continue
		}
		seen[proxyURL] = struct{}{}
		label := strings.TrimSpace(profile.Name)
		if label == "" {
			label = "代理池线路"
		}
		candidates = append(candidates, candidate{url: proxyURL, label: label})
	}
	if len(candidates) == 0 {
		proxyURL := strings.TrimSpace(settings.ProxyURL)
		if proxyURL == "" {
			return nil, errors.New("代理池为空且未配置全局代理；OpenAI OAuth 禁止直连")
		}
		return s.reserveOAuthProxy(proxyURL, "全局代理（代理池为空时回退）"), nil
	}

	s.oauthProxyMu.Lock()
	if s.oauthProxyActive == nil {
		s.oauthProxyActive = make(map[string]int)
	}
	available := make([]int, 0, len(candidates))
	for index, item := range candidates {
		if _, blocked := excluded[item.url]; !blocked {
			available = append(available, index)
		}
	}
	if len(available) == 0 {
		available = make([]int, 0, len(candidates))
		for index := range candidates {
			available = append(available, index)
		}
	}
	minimum := int(^uint(0) >> 1)
	least := make([]int, 0, len(candidates))
	for _, index := range available {
		item := candidates[index]
		count := s.oauthProxyActive[item.url]
		if count < minimum {
			minimum = count
			least = least[:0]
			least = append(least, index)
		} else if count == minimum {
			least = append(least, index)
		}
	}
	selected := candidates[least[int(s.oauthProxyCursor%uint64(len(least)))]]
	s.oauthProxyCursor++
	s.oauthProxyActive[selected.url]++
	activeCount := s.oauthProxyActive[selected.url]
	s.oauthProxyMu.Unlock()

	lease := &oauthProxyLease{url: selected.url, key: selected.url, label: selected.label, activeCount: activeCount}
	lease.releaseFn = func() { s.releaseOAuthProxy(lease.key) }
	return lease, nil
}

func (s *Server) reserveOAuthProxy(proxyURL, label string) *oauthProxyLease {
	s.oauthProxyMu.Lock()
	if s.oauthProxyActive == nil {
		s.oauthProxyActive = make(map[string]int)
	}
	s.oauthProxyActive[proxyURL]++
	activeCount := s.oauthProxyActive[proxyURL]
	s.oauthProxyMu.Unlock()
	lease := &oauthProxyLease{url: proxyURL, key: proxyURL, label: label, activeCount: activeCount}
	lease.releaseFn = func() { s.releaseOAuthProxy(lease.key) }
	return lease
}

func (s *Server) releaseOAuthProxy(proxyURL string) {
	s.oauthProxyMu.Lock()
	defer s.oauthProxyMu.Unlock()
	if count := s.oauthProxyActive[proxyURL]; count > 1 {
		s.oauthProxyActive[proxyURL] = count - 1
	} else {
		delete(s.oauthProxyActive, proxyURL)
	}
}

var oauthProxySessionPattern = regexp.MustCompile(`(?i)(session-)([a-z0-9]+)`)

// rotateOAuthProxySession keeps the provider endpoint and credentials intact
// while changing only the sticky-session token. This is used between complete
// OAuth attempts, never in the middle of one attempt.
func rotateOAuthProxySession(proxyURL string, round, quality int) string {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.User == nil {
		return proxyURL
	}
	username := parsed.User.Username()
	if !oauthProxySessionPattern.MatchString(username) {
		return proxyURL
	}
	seed := time.Now().UnixNano()%90000000 + 10000000
	token := fmt.Sprintf("%08d", (seed+int64(round*97+quality*13))%100000000)
	username = oauthProxySessionPattern.ReplaceAllString(username, "${1}"+token)
	if password, ok := parsed.User.Password(); ok {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = url.User(username)
	}
	return parsed.String()
}

func oauthProxyEndpoint(proxyURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

type oauthProxyProbeResult struct {
	OK            bool           `json:"ok"`
	Retryable     bool           `json:"retryable"`
	ErrorCode     string         `json:"error_code"`
	Stage         string         `json:"stage"`
	HTTPStatus    int            `json:"http_status"`
	AuthStatus    int            `json:"auth_status"`
	Error         string         `json:"error"`
	ExitIP        string         `json:"exit_ip"`
	Location      string         `json:"loc"`
	Colo          string         `json:"colo"`
	EgressFirst   map[string]any `json:"egress_first"`
	EgressConfirm map[string]any `json:"egress_confirm"`
	Proxy         map[string]any `json:"proxy"`
}

func (s *Server) probeOAuthProxy(ctx context.Context, proxyURL string) (oauthProxyProbeResult, error) {
	python := workflow.FindPython()
	if python == "" {
		return oauthProxyProbeResult{}, errors.New("未找到 Python 运行环境")
	}
	script := workflow.FindInternalScript("protocol_proxy_probe.py")
	if script == "" {
		return oauthProxyProbeResult{}, errors.New("未找到 OAuth 代理质检脚本")
	}
	cmd := exec.CommandContext(ctx, python, script, proxyURL)
	cmd.Dir, _ = os.Getwd()
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return oauthProxyProbeResult{}, ctx.Err()
		}
		return oauthProxyProbeResult{}, fmt.Errorf("代理质检执行失败: %w", err)
	}
	var result oauthProxyProbeResult
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &result); err != nil {
		return oauthProxyProbeResult{}, fmt.Errorf("解析代理质检结果失败: %w", err)
	}
	return result, nil
}
