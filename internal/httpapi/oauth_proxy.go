package httpapi

import (
	"errors"
	"strings"
	"sync"
)

type oauthProxyLease struct {
	url         string
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
	minimum := int(^uint(0) >> 1)
	least := make([]int, 0, len(candidates))
	for index, item := range candidates {
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

	lease := &oauthProxyLease{url: selected.url, label: selected.label, activeCount: activeCount}
	lease.releaseFn = func() { s.releaseOAuthProxy(selected.url) }
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
	lease := &oauthProxyLease{url: proxyURL, label: label, activeCount: activeCount}
	lease.releaseFn = func() { s.releaseOAuthProxy(proxyURL) }
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
