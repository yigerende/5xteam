package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

const (
	proxyProbeMaxBodyBytes   = int64(1 << 20)
	proxyQualityMaxBodyBytes = int64(8 << 10)
	proxyQualityUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
	openAIQualityURL         = "https://api.openai.com/v1/models"
)

var (
	proxyCFRayPattern         = regexp.MustCompile(`(?i)cf-ray[:\s=]+([a-z0-9-]+)`)
	proxyCRayPattern          = regexp.MustCompile(`(?i)cRay:\s*'([a-z0-9-]+)'`)
	proxyHTMLChallengeMarkers = []string{
		"window._cf_chl_opt",
		"just a moment",
		"enable javascript and cookies to continue",
		"__cf_chl_",
		"challenge-platform",
	}
)

type proxyExitInfo struct {
	IP          string
	Country     string
	CountryCode string
	Region      string
	City        string
}

type proxyProbeTarget struct {
	URL    string
	Parser func([]byte) (proxyExitInfo, error)
}

var proxyProbeTargets = []proxyProbeTarget{
	{URL: "http://ip-api.com/json/?lang=zh-CN", Parser: parseIPAPIExit},
	{URL: "http://api64.ipify.org?format=json", Parser: parseIPifyExit},
}

func proxyHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	proxy, err := ValidateProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func closeProxyIdleConnections(client *http.Client) {
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

// TestProxyExit reproduces Sub2API's IP management connectivity probe. The
// public IP service request itself passes through the tested proxy.
func TestProxyExit(ctx context.Context, proxyURL string, timeout time.Duration) model.ProxyTestResult {
	result := model.ProxyTestResult{CheckedAt: time.Now()}
	client, err := proxyHTTPClient(proxyURL, timeout)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer closeProxyIdleConnections(client)
	exit, latency, probeErr := probeProxyExit(ctx, client)
	result.LatencyMS = latency
	if probeErr != nil {
		proxy, _ := ValidateProxyURL(proxyURL)
		result.Message = redactProxySecret(friendlyNetworkError(probeErr).Error(), proxy)
		return result
	}
	result.Reachable = true
	result.HTTPStatus = http.StatusOK
	result.Message = "代理出口可达"
	result.IPAddress = exit.IP
	result.Country = exit.Country
	result.CountryCode = exit.CountryCode
	result.Region = exit.Region
	result.City = exit.City
	return result
}

func probeProxyExit(ctx context.Context, client *http.Client) (proxyExitInfo, int64, error) {
	var lastErr error
	var lastLatency int64
	for _, target := range proxyProbeTargets {
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		lastLatency = time.Since(started).Milliseconds()
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, proxyProbeMaxBodyBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("IP 探测返回 HTTP %d", resp.StatusCode)
			continue
		}
		if int64(len(body)) > proxyProbeMaxBodyBytes {
			lastErr = errors.New("IP 探测响应过大")
			continue
		}
		exit, parseErr := target.Parser(body)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		return exit, lastLatency, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的 IP 探测地址")
	}
	return proxyExitInfo{}, lastLatency, fmt.Errorf("所有 IP 探测均失败：%w", lastErr)
}

func parseIPAPIExit(body []byte) (proxyExitInfo, error) {
	var payload struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Query       string `json:"query"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		Region      string `json:"region"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return proxyExitInfo{}, fmt.Errorf("解析 ip-api 响应失败：%w", err)
	}
	if !strings.EqualFold(payload.Status, "success") || strings.TrimSpace(payload.Query) == "" {
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = "未返回出口 IP"
		}
		return proxyExitInfo{}, errors.New(message)
	}
	region := strings.TrimSpace(payload.RegionName)
	if region == "" {
		region = strings.TrimSpace(payload.Region)
	}
	return proxyExitInfo{IP: payload.Query, Country: payload.Country, CountryCode: payload.CountryCode, Region: region, City: payload.City}, nil
}

func parseIPifyExit(body []byte) (proxyExitInfo, error) {
	var payload struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return proxyExitInfo{}, fmt.Errorf("解析 ipify 响应失败：%w", err)
	}
	if strings.TrimSpace(payload.IP) == "" {
		return proxyExitInfo{}, errors.New("ipify 未返回出口 IP")
	}
	return proxyExitInfo{IP: strings.TrimSpace(payload.IP)}, nil
}

// TestProxyOpenAIQuality follows Sub2API's unauthenticated OpenAI quality
// check: 401 means reachable, 429 is a warning, and Cloudflare challenges are
// reported separately from ordinary failures.
func TestProxyOpenAIQuality(ctx context.Context, proxyURL string, timeout time.Duration) model.ProxyOpenAIQualityResult {
	result := model.ProxyOpenAIQualityResult{Status: "fail", Score: 100, Grade: "A", CheckedAt: time.Now()}
	client, err := proxyHTTPClient(proxyURL, timeout)
	if err != nil {
		result.Message = err.Error()
		finalizeProxyOpenAIQuality(&result)
		return result
	}
	defer closeProxyIdleConnections(client)

	exit, baseLatency, err := probeProxyExit(ctx, client)
	result.BaseLatencyMS = baseLatency
	if err != nil {
		proxy, _ := ValidateProxyURL(proxyURL)
		result.Message = "出口探测失败：" + redactProxySecret(friendlyNetworkError(err).Error(), proxy)
		finalizeProxyOpenAIQuality(&result)
		return result
	}
	result.ExitIP = exit.IP
	result.Country = exit.Country
	result.CountryCode = exit.CountryCode
	result.Region = exit.Region
	result.City = exit.City

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openAIQualityURL, nil)
	if err != nil {
		result.Message = "构建 OpenAI 请求失败：" + err.Error()
		finalizeProxyOpenAIQuality(&result)
		return result
	}
	req.Header.Set("Accept", "application/json,text/html,*/*")
	req.Header.Set("User-Agent", proxyQualityUserAgent)
	started := time.Now()
	resp, requestErr := client.Do(req)
	result.LatencyMS = time.Since(started).Milliseconds()
	if requestErr != nil {
		proxy, _ := ValidateProxyURL(proxyURL)
		result.Message = "OpenAI 请求失败：" + redactProxySecret(friendlyNetworkError(requestErr).Error(), proxy)
		finalizeProxyOpenAIQuality(&result)
		return result
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, proxyQualityMaxBodyBytes+1))
	if readErr != nil {
		result.Message = "读取 OpenAI 响应失败：" + readErr.Error()
		finalizeProxyOpenAIQuality(&result)
		return result
	}
	if int64(len(body)) > proxyQualityMaxBodyBytes {
		body = body[:proxyQualityMaxBodyBytes]
	}
	result.Status, result.Message, result.CFRay = classifyOpenAIProxyResponse(resp.StatusCode, resp.Header, body)
	finalizeProxyOpenAIQuality(&result)
	return result
}

func classifyOpenAIProxyResponse(statusCode int, headers http.Header, body []byte) (status, message, cfRay string) {
	if isCloudflareProxyChallenge(statusCode, headers, body) {
		return "challenge", "命中 Cloudflare challenge", extractProxyCFRay(headers, body)
	}
	if statusCode == http.StatusUnauthorized {
		return "pass", "HTTP 401（OpenAI 目标可达）", ""
	}
	if statusCode == http.StatusTooManyRequests {
		return "warn", "OpenAI 返回 429，可能存在频控", ""
	}
	return "fail", fmt.Sprintf("OpenAI 返回非预期状态码 HTTP %d", statusCode), ""
}

func isCloudflareProxyChallenge(statusCode int, headers http.Header, body []byte) bool {
	if statusCode != http.StatusForbidden && statusCode != http.StatusTooManyRequests {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(headers.Get("cf-mitigated")), "challenge") {
		return true
	}
	preview := strings.ToLower(string(body))
	for _, marker := range proxyHTMLChallengeMarkers {
		if strings.Contains(preview, marker) {
			return true
		}
	}
	contentType := strings.ToLower(strings.TrimSpace(headers.Get("content-type")))
	return strings.Contains(contentType, "text/html") &&
		(strings.Contains(preview, "<html") || strings.Contains(preview, "<!doctype html")) &&
		(strings.Contains(preview, "cloudflare") || strings.Contains(preview, "challenge"))
}

func extractProxyCFRay(headers http.Header, body []byte) string {
	if ray := strings.TrimSpace(headers.Get("cf-ray")); ray != "" {
		return ray
	}
	preview := string(body)
	if matches := proxyCFRayPattern.FindStringSubmatch(preview); len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	if matches := proxyCRayPattern.FindStringSubmatch(preview); len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

func finalizeProxyOpenAIQuality(result *model.ProxyOpenAIQualityResult) {
	deduction := 22
	switch result.Status {
	case "pass":
		deduction = 0
	case "warn":
		deduction = 10
	case "challenge":
		deduction = 30
	}
	result.Score = 100 - deduction
	switch {
	case result.Score >= 90:
		result.Grade = "A"
	case result.Score >= 75:
		result.Grade = "B"
	case result.Score >= 60:
		result.Grade = "C"
	case result.Score >= 40:
		result.Grade = "D"
	default:
		result.Grade = "F"
	}
	labels := map[string]string{"pass": "通过", "warn": "告警", "challenge": "挑战", "fail": "失败"}
	result.Summary = fmt.Sprintf("OpenAI %s，评分 %d，等级 %s", labels[result.Status], result.Score, result.Grade)
}
