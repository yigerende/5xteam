package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
)

const gptInfoAttempts = 3
const gptInfoBrowserScript = `import sys,json
from curl_cffi import CurlOpt, requests
p=json.load(sys.stdin)
try:
    with requests.Session(impersonate="chrome136",curl_options={CurlOpt.NOPROXY:""}) as s:
        s.proxies={"http":p["proxy"],"https":p["proxy"]}
        r=s.get(p["url"],headers=p["headers"],timeout=20,allow_redirects=False)
        print(json.dumps({"status":r.status_code,"body":r.text[:262144],"retry_after":r.headers.get("retry-after",""),"content_type":r.headers.get("content-type",""),"cf_mitigated":r.headers.get("cf-mitigated",""),"cf_ray":r.headers.get("cf-ray","")}))
except Exception:
    print(json.dumps({"status":0,"body":"","error":"Browser request failed; check global proxy, curl_cffi and network"}))
`

type gptInfoResponse struct {
	Status      int    `json:"status"`
	Body        string `json:"body"`
	RetryAfter  string `json:"retry_after"`
	Error       string `json:"error"`
	ContentType string `json:"content_type"`
	CFMitigated string `json:"cf_mitigated"`
	CFRay       string `json:"cf_ray"`
}
type gptInfoRequest func(context.Context, string) gptInfoResponse

// RefreshAccountInfo never exchanges or refreshes tokens. Both requests use the
// same saved AT and the configured global proxy, with independent bounded retries.
func (c *Client) RefreshAccountInfo(ctx context.Context, token string) model.MailGPTInfoResult {
	token = strings.TrimSpace(ExtractAccessToken(token))
	if strings.TrimSpace(token) == "" {
		return failedGPTInfo("请先获取 AT")
	}
	if strings.TrimSpace(c.settings.ProxyURL) == "" {
		return failedGPTInfo("请先配置全局代理，GPT 信息查询禁止直连")
	}
	if c.python == "" {
		return failedGPTInfo("未找到 Python，请安装 Python 和 curl_cffi")
	}
	return refreshGPTInfo(ctx, token, func(ctx context.Context, path string) gptInfoResponse {
		return c.requestGPTInfo(ctx, token, path)
	}, waitGPTInfo)
}

func failedGPTInfo(message string) model.MailGPTInfoResult {
	return model.MailGPTInfoResult{Check: model.GPTInfoCheck{
		Status: "failed", CheckedAt: time.Now().UTC(), Error: message,
		Plan: model.GPTInfoRequestStatus{Error: message}, Me: model.GPTInfoRequestStatus{Error: message},
	}}
}

func (c *Client) requestGPTInfo(ctx context.Context, token, path string) gptInfoResponse {
	claims, _ := DecodeUserInfo(token)
	deviceID := stablePlanDeviceID(firstNonEmptyString(claims.Email, claims.AccountID, token[:minInt(32, len(token))]))
	req, err := c.newAccountPlanRequest(ctx, path, token, deviceID)
	if err != nil {
		return gptInfoResponse{Error: "账号信息请求地址无效"}
	}
	headers := make(map[string]string, len(req.Header))
	for key := range req.Header {
		headers[key] = req.Header.Get(key)
	}
	payload := map[string]any{
		"url": req.URL.String(), "proxy": c.settings.ProxyURL, "headers": headers,
	}
	raw, _ := json.Marshal(payload)
	// Also bound process startup/exit; curl's own network timeout is 20 seconds.
	requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(requestCtx, c.python, "-c", gptInfoBrowserScript)
	cmd.Stdin = bytes.NewReader(raw)
	output, err := cmd.Output()
	if err != nil {
		return gptInfoResponse{Error: "浏览器请求失败，请检查 Python、curl_cffi 和全局代理"}
	}
	var response gptInfoResponse
	if json.Unmarshal(output, &response) != nil {
		return gptInfoResponse{Error: "浏览器请求返回格式无效"}
	}
	return response
}

func waitGPTInfo(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func refreshGPTInfo(ctx context.Context, token string, request gptInfoRequest, wait func(context.Context, time.Duration) error) model.MailGPTInfoResult {
	claims, _ := DecodeUserInfo(token)
	var planPayload, mePayload map[string]any
	var result model.MailGPTInfoResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		planPayload, result.Check.Plan = fetchGPTInfo(ctx, accountPlanCheckPath+"?timezone_offset_min=-480", request, wait)
	}()
	go func() {
		defer wg.Done()
		mePayload, result.Check.Me = fetchGPTInfo(ctx, "/me", request, wait)
	}()
	wg.Wait()
	if result.Check.Plan.OK {
		plan, err := parseGPTInfoPlan(planPayload, claims)
		if err != nil {
			result.Check.Plan.OK, result.Check.Plan.Error = false, err.Error()
		} else {
			result.Plan, result.Check.PlanSource = plan, "entitlement"
		}
	}
	if result.Check.Me.OK {
		if email := textValue(mePayload["email"]); email != "" && claims.Email != "" && !strings.EqualFold(email, claims.Email) {
			result.Check.Me.OK, result.Check.Me.Error = false, "账号信息与 AT 中的邮箱不匹配"
		} else {
			result.CreatedAtOpenAI = parseGPTCreated(mePayload["created"])
			if result.CreatedAtOpenAI == nil {
				result.Check.Me.Error = "接口未返回有效的 GPT 创建时间，保留历史数据"
			}
			if !result.Plan.OK {
				if plan := firstNonEmptyString(textValue(mePayload["plan_type"]), textValue(mePayload["chatgpt_plan_type"]), textValue(childMap(mePayload, "account")["plan_type"])); plan != "" {
					result.Plan = model.AccountPlanCheckResult{OK: true, CurrentPlanType: plan}
					result.Check.PlanSource = "me"
				}
			}
		}
	}
	if !result.Plan.OK && claims.PlanType != "" {
		result.Plan.CurrentPlanType, result.Check.PlanSource = claims.PlanType, "jwt"
	}
	result.Check.CheckedAt = time.Now().UTC()
	result.Plan.CheckedAt = result.Check.CheckedAt
	result.Plan.HTTPStatus, result.Plan.AttemptCount = result.Check.Plan.HTTPStatus, result.Check.Plan.Attempts
	if result.Check.PlanSource == "me" {
		result.Plan.HTTPStatus, result.Plan.AttemptCount = result.Check.Me.HTTPStatus, result.Check.Me.Attempts
	}
	switch {
	case result.Check.Plan.OK && result.Check.Me.OK && result.CreatedAtOpenAI != nil:
		result.Check.Status = "success"
	case result.Check.Plan.OK || result.Check.Me.OK:
		result.Check.Status = "partial"
	default:
		result.Check.Status = "failed"
	}
	var messages []string
	if result.Check.Plan.Error != "" {
		messages = append(messages, "套餐: "+result.Check.Plan.Error)
	}
	if result.Check.Me.Error != "" {
		messages = append(messages, "账号信息: "+result.Check.Me.Error)
	}
	result.Check.Error = strings.Join(messages, "；")
	return result
}

func fetchGPTInfo(ctx context.Context, path string, request gptInfoRequest, wait func(context.Context, time.Duration) error) (map[string]any, model.GPTInfoRequestStatus) {
	var check model.GPTInfoRequestStatus
	for attempt := 1; attempt <= gptInfoAttempts; attempt++ {
		if ctx.Err() != nil {
			check.Error = "查询已中断"
			break
		}
		response := request(ctx, path)
		check.Attempts, check.HTTPStatus = attempt, response.Status
		check.ContentType = limitText(response.ContentType, 128)
		check.CFMitigated, check.CFRay = limitText(response.CFMitigated, 32), limitText(response.CFRay, 128)
		if response.Status == http.StatusOK {
			var payload map[string]any
			decoder := json.NewDecoder(strings.NewReader(response.Body))
			decoder.UseNumber()
			if decoder.Decode(&payload) == nil && payload != nil && payload["error"] == nil {
				check.OK, check.Error = true, ""
				return payload, check
			}
			check.Error = "HTTP 200 但响应不是有效的账号 JSON"
			break
		}
		retry := response.Status == 0 || response.Status == 408 || response.Status == 429 || response.Status >= 500
		switch response.Status {
		case 0:
			check.Error = "网络请求失败，请检查全局代理或请求超时"
		case 401:
			check.Error = "AT 已过期或失效，请重新获取 AT"
		case 403:
			body := strings.ToLower(response.Body)
			retry = strings.EqualFold(strings.TrimSpace(response.CFMitigated), "challenge") || strings.Contains(body, "cf-") || strings.Contains(body, "cloudflare") || strings.Contains(body, "just a moment")
			check.Error = "HTTP 403，接口拒绝访问（不判定死号）"
			if retry {
				check.Error = "HTTP 403，Cloudflare 验证拦截（不判定死号）"
			}
		default:
			check.Error = fmt.Sprintf("HTTP %d，接口查询失败", response.Status)
		}
		if !retry || attempt == gptInfoAttempts {
			break
		}
		delay := time.Duration(attempt) * time.Second
		if seconds, err := strconv.ParseInt(strings.TrimSpace(response.RetryAfter), 10, 64); err == nil && seconds > 0 {
			delay = time.Duration(min(seconds, 31)) * time.Second
		} else if deadline, err := http.ParseTime(response.RetryAfter); err == nil && time.Until(deadline) > delay {
			delay = time.Until(deadline)
		}
		// Do not retry sooner than Retry-After; leave long cooldowns for a later
		// explicit click rather than keeping a refresh worker for hours.
		if delay > 30*time.Second {
			check.Error += "，请在 Retry-After 冷却后重试"
			break
		}
		if err := wait(ctx, delay); err != nil {
			check.Error = "查询已中断"
			break
		}
	}
	return nil, check
}

func parseGPTCreated(value any) *time.Time {
	seconds, err := strconv.ParseInt(textValue(value), 10, 64)
	if err != nil || seconds <= 0 || seconds > time.Now().Add(24*time.Hour).Unix() {
		return nil
	}
	created := time.Unix(seconds, 0).UTC()
	return &created
}

func parseGPTInfoPlan(payload map[string]any, claims model.UserInfo) (model.AccountPlanCheckResult, error) {
	accounts := childMap(payload, "accounts")
	var item map[string]any
	key := claims.AccountID
	if key != "" {
		item = childMap(accounts, key)
	}
	if len(item) == 0 {
		// A personal default entry is a safe fallback; do not randomly select
		// one of multiple Team memberships returned by accounts/check.
		key, item = "default", childMap(accounts, "default")
	}
	if len(item) == 0 && len(accounts) == 1 {
		for k, value := range accounts {
			key = k
			item, _ = value.(map[string]any)
		}
	}
	if len(item) == 0 {
		return model.AccountPlanCheckResult{}, errors.New("无法确定账号对应的套餐，未任意选择其他空间")
	}
	entitlement := childMap(item, "entitlement")
	subscription := textValue(entitlement["subscription_plan"])
	plan := map[string]string{
		"chatgptfreeplan": "free", "chatgptplusplan": "plus", "chatgptproplan": "pro",
		"chatgptteamplan": "team", "chatgptbusinessplan": "team", "chatgptenterpriseplan": "enterprise",
	}[strings.ToLower(subscription)]
	plan = firstNonEmptyString(plan, textValue(entitlement["plan_type"]), textValue(childMap(item, "account")["plan_type"]), subscription)
	if plan == "" {
		return model.AccountPlanCheckResult{}, errors.New("套餐响应缺少有效套餐字段")
	}
	parsed, err := parseAccountPlanResponse(map[string]any{"accounts": map[string]any{key: item}}, model.UserInfo{AccountID: key})
	if err != nil {
		return parsed, err
	}
	parsed.OK, parsed.CurrentPlanType = true, plan
	return parsed, nil
}
