package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

type globalProxyWaiting struct{ Until time.Time }

func (e *globalProxyWaiting) Error() string {
	return "全局主备代理均在冷却或恢复测试中，等待可用代理"
}

type cooledLoginProxyError struct{ error }

func (e *cooledLoginProxyError) Unwrap() error { return e.error }

var proxyDeniedHTTP = regexp.MustCompile(`(?i)\b(?:http|status|response)[\s:=]*(?:401|403|407|429)\b`)
var proxyCurlTransport = regexp.MustCompile(`(?i)curl[ :]*(?:\((?:5|6|7|28|35|52|55|56|97)\)|(?:5|6|7|28|35|52|55|56|97)\b)`)

func isLoginProxyNetworkFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	text := strings.ToLower(err.Error())
	if proxyDeniedHTTP.MatchString(text) {
		return false
	}
	for _, word := range []string{"captcha", "challenge", "verification code", "验证码", "invalid password", "incorrect password", "密码错误", "account_deactivated", "账号已停用", "certificate verify", "certificate verification", "certificate has expired", "pooltimeout"} {
		if strings.Contains(text, word) {
			return false
		}
	}
	if proxyCurlTransport.MatchString(text) {
		return true
	}
	for _, word := range []string{"connect tunnel failed", "proxy connect aborted", "could not resolve proxy", "could not resolve host", "failed to connect", "connection reset", "connection refused", "connection aborted", "connection closed", "connection timed out", "connection timeout", "temporary failure in name resolution", "name or service not known", "tls handshake timeout", "unexpected eof", "empty reply from server", "recv failure", "send failure", "operation timed out", "operation timeout", "network timeout", "network error: timeout", "network error: timed out"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func (s *Server) recordLoginProxyFailure(lease *oauthProxyLease, err error) error {
	if lease == nil || !isLoginProxyNetworkFailure(err) {
		return err
	}
	h, changed, e := s.store.FailGlobalProxy(lease.key, lease.healthGeneration, time.Now())
	if e != nil {
		slog.Warn("保存登录代理冷却失败", "error", e)
		return err
	}
	if changed {
		slog.Warn("登录代理进入冷却", "proxy", lease.label, "cooldown_until", h.Until)
	}
	if changed || h.State != "available" {
		return &cooledLoginProxyError{err}
	}
	return err
}

func (s *Server) registerGlobalProxy(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/proxies/health", func(w http.ResponseWriter, r *http.Request) {
		profiles := s.store.Proxies()
		urls := make([]string, 0, len(profiles))
		for _, p := range profiles {
			urls = append(urls, p.URL)
		}
		states, err := s.store.GlobalProxyHealth(urls)
		if err != nil {
			writeAPI(w, 500, nil, "读取代理冷却状态失败")
			return
		}
		items := map[string]any{}
		for _, p := range profiles {
			items[p.ID] = states[p.URL]
		}
		writeAPI(w, 200, map[string]any{"items": items, "server_time": time.Now(), "policy": s.store.Settings().ProxyFailover.Normalized()}, "")
	})
}

func (s *Server) monitorGlobalProxyRecovery(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.tickGlobalProxyRecovery(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) tickGlobalProxyRecovery(ctx context.Context) {
	if !s.globalProxyRecoveryMu.TryLock() {
		return
	}
	defer s.globalProxyRecoveryMu.Unlock()
	probes, err := s.store.ClaimGlobalProxyProbes(time.Now())
	if err != nil {
		slog.Warn("读取代理恢复任务失败", "error", err)
		return
	}
	var wg sync.WaitGroup
	for _, probe := range probes {
		wg.Add(1)
		go func(p store.GlobalProxyProbe) {
			defer wg.Done()
			check, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			proxy, err := workflow.PrepareVendorProxy(rotateOAuthProxySession(p.URL, 1, 1), p.SessionConfig)
			ok := err == nil
			if ok {
				if s.globalProxyProbe != nil {
					ok = s.globalProxyProbe(check, proxy)
				} else {
					ok = workflow.TestProxyLoginTransport(check, proxy)
				}
			}
			if ctx.Err() != nil {
				return
			}
			if err = s.store.CompleteGlobalProxyProbe(p, ok, time.Now()); err != nil {
				slog.Warn("保存代理恢复检测失败", "error", err)
			}
		}(probe)
	}
	wg.Wait()
}

func proxyWaiting(until int64) error {
	at := time.UnixMilli(until)
	if until == 0 || at.Before(time.Now()) {
		at = time.Now().Add(5 * time.Second)
	}
	return &globalProxyWaiting{Until: at}
}

// Select the configured primary first; backups only follow an unavailable
// primary. Least-used mode retains its pool strategy but excludes cooling rows.
func (s *Server) acquireHealthyGlobalProxy(excluded map[string]struct{}) (*oauthProxyLease, error) {
	settings := s.store.Settings()
	profiles := s.store.Proxies()
	cfg := settings.ProxyFailover.Normalized()
	type candidate struct{ url, label string }
	candidates := []candidate{}
	seen := map[string]bool{}
	add := func(url, label string) {
		url = strings.TrimSpace(url)
		if url != "" && !seen[url] {
			seen[url] = true
			candidates = append(candidates, candidate{url, label})
		}
	}
	global := normalizeOAuthProxyMode(settings.OAuthProxyMode) == "global"
	if global || len(profiles) == 0 {
		label := "全局主用代理"
		for _, p := range profiles {
			if p.URL == settings.ProxyURL {
				label = p.Name
				break
			}
		}
		add(settings.ProxyURL, label)
		for _, id := range cfg.BackupIDs {
			for _, p := range profiles {
				if p.ID == id {
					add(p.URL, p.Name)
					break
				}
			}
		}
	} else {
		for _, p := range profiles {
			add(p.URL, p.Name)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("请先配置全局代理；OpenAI OAuth 禁止直连")
	}
	s.oauthProxyMu.Lock()
	defer s.oauthProxyMu.Unlock()
	if s.oauthProxyActive == nil {
		s.oauthProxyActive = map[string]int{}
	}
	type choice struct {
		candidate
		generation int64
	}
	ready := []choice{}
	var soon int64
	for _, p := range candidates {
		h, ok, e := s.store.ReadyGlobalProxy(p.url, time.Now())
		if e != nil {
			return nil, fmt.Errorf("读取代理冷却状态失败: %w", e)
		}
		if !ok {
			next := h.NextAt
			if next == 0 {
				next = h.Until
			}
			if soon == 0 || next < soon {
				soon = next
			}
			continue
		}
		ready = append(ready, choice{p, h.Generation})
		if global {
			break
		}
	}
	if len(ready) == 0 {
		return nil, proxyWaiting(soon)
	}
	if !global {
		available := []choice{}
		for _, p := range ready {
			if _, blocked := excluded[p.url]; !blocked {
				available = append(available, p)
			}
		}
		if len(available) > 0 {
			ready = available
		}
		minimum := int(^uint(0) >> 1)
		least := []choice{}
		for _, p := range ready {
			n := s.oauthProxyActive[p.url]
			if n < minimum {
				minimum = n
				least = []choice{p}
			} else if n == minimum {
				least = append(least, p)
			}
		}
		ready = []choice{least[int(s.oauthProxyCursor%uint64(len(least)))]}
		s.oauthProxyCursor++
	}
	selected := ready[0]
	s.oauthProxyActive[selected.url]++
	lease := &oauthProxyLease{url: selected.url, key: selected.url, label: selected.label, activeCount: s.oauthProxyActive[selected.url], healthGeneration: selected.generation}
	for _, p := range profiles {
		if p.URL == selected.url {
			lease.sessionConfig = p.ProxySessionConfig
			break
		}
	}
	lease.releaseFn = func() { s.releaseOAuthProxy(lease.key) }
	return lease, nil
}
