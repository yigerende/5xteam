package httpapi

import (
	"strings"

	"chatgpt-space-merge/internal/model"
)

// proxyLogDetails returns a secret-free proxy summary for account lifecycle
// diagnostics. It intentionally stores only the saved proxy name and a
// credential-less endpoint, so exported account logs can explain routing
// without leaking proxy usernames/passwords.
func (s *Server) proxyLogDetails(settings model.Settings, source string) map[string]any {
	proxyURL := strings.TrimSpace(settings.ProxyURL)
	details := map[string]any{
		"configured": proxyURL != "",
		"source":     strings.TrimSpace(source),
	}
	if details["source"] == "" {
		details["source"] = "global"
	}
	if proxyURL == "" {
		details["name"] = "未配置代理"
		return details
	}
	details["endpoint"] = oauthProxyEndpoint(proxyURL)
	details["name"] = "全局代理"
	for _, proxy := range s.store.Proxies() {
		if strings.TrimSpace(proxy.URL) == proxyURL {
			details["proxy_id"] = proxy.ID
			details["name"] = proxy.Name
			break
		}
	}
	return details
}

func (s *Server) adminProxyLogDetails(settings model.Settings, admin model.AdminAccountProfile) map[string]any {
	source := "global"
	if strings.TrimSpace(admin.ProxyID) != "" {
		source = "admin_dedicated"
	}
	details := s.proxyLogDetails(settings, source)
	if strings.TrimSpace(admin.ProxyID) != "" {
		details["proxy_id"] = admin.ProxyID
		for _, proxy := range s.store.Proxies() {
			if proxy.ID == admin.ProxyID {
				details["name"] = proxy.Name
				break
			}
		}
	}
	return details
}
