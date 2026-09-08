package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

const accountPlanCheckPath = "/accounts/check/v4-2023-04-27"

// CheckAccountPlan mirrors turb's pure-protocol accounts/check request and
// normalizes the fields displayed by Pro management.
func (c *Client) CheckAccountPlan(ctx context.Context, token string) model.AccountPlanCheckResult {
	token = strings.TrimSpace(ExtractAccessToken(token))
	result := model.AccountPlanCheckResult{CheckedAt: time.Now()}
	if token == "" {
		result.Error = "AT 为空"
		return result
	}
	claims, _ := DecodeUserInfo(token)
	requestPath := accountPlanCheckPath + "?timezone_offset_min=-"
	const maxAttempts = 2
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.AttemptCount = attempt
		status, data, retryAfter, err := c.requestAccountPlan(ctx, requestPath, token, stablePlanDeviceID(firstNonEmptyString(claims.Email, claims.AccountID, token[:minInt(32, len(token))])))
		result.HTTPStatus = status
		if err == nil && status >= 200 && status < 300 {
			var payload map[string]any
			if json.Unmarshal(data, &payload) == nil {
				parsed, parseErr := parseAccountPlanResponse(payload, claims)
				if parseErr == nil {
					parsed.OK, parsed.HTTPStatus, parsed.CheckedAt, parsed.AttemptCount = true, status, time.Now(), attempt
					return parsed
				}
				err = parseErr
			} else {
				err = fmt.Errorf("响应不是 JSON 对象")
			}
		}
		if err != nil {
			result.Error = err.Error()
		} else if status == http.StatusUnauthorized {
			result.Error = "AT 已过期或失效，请重新获取 AT"
		} else {
			result.Error = fmt.Sprintf("HTTP %d: %s", status, limitText(strings.TrimSpace(string(data)), 300))
		}
		if attempt >= maxAttempts || !retryablePlanCheck(status, err) {
			break
		}
		wait := 1500 * time.Millisecond
		if retryAfter > 0 {
			wait = retryAfter
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			result.Error = friendlyNetworkError(ctx.Err()).Error()
			return result
		}
	}
	result.CheckedAt = time.Now()
	return result
}

func (c *Client) requestAccountPlan(ctx context.Context, path, token, deviceID string) (int, []byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("oai-device-id", deviceID)
	req.Header.Set("oai-language", "zh-CN")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	setOpenAITargetHeaders(req, path)
	var status int
	var data []byte
	if c.python != "" && strings.HasPrefix(c.baseURL, "https://") && strings.TrimSpace(c.settings.ProxyURL) != "" {
		status, data, err = c.browserDoWithRedirects(ctx, req, nil, false)
	} else {
		client := *c.http
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		var response *http.Response
		response, err = client.Do(req)
		if err == nil {
			defer response.Body.Close()
			status = response.StatusCode
			data, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
			if delay, parseErr := strconv.ParseFloat(strings.TrimSpace(response.Header.Get("Retry-After")), 64); parseErr == nil && delay > 0 {
				return status, data, time.Duration(delay * float64(time.Second)), err
			}
		}
	}
	if err != nil {
		return status, data, 0, friendlyNetworkError(err)
	}
	if len(data) > maxResponseBytes {
		data = data[:maxResponseBytes]
	}
	return status, data, 0, nil
}

func parseAccountPlanResponse(payload map[string]any, claims model.UserInfo) (model.AccountPlanCheckResult, error) {
	accounts, ok := payload["accounts"].(map[string]any)
	if !ok {
		return model.AccountPlanCheckResult{}, fmt.Errorf("响应缺少 accounts 对象")
	}
	var item map[string]any
	accountKey := ""
	if claims.AccountID != "" {
		item, _ = accounts[claims.AccountID].(map[string]any)
		if item != nil {
			accountKey = claims.AccountID
		}
	}
	if item == nil {
		if fallback, exists := accounts["default"].(map[string]any); exists {
			item, accountKey = fallback, "default"
		}
	}
	if item == nil {
		for key, value := range accounts {
			if key == "default" {
				continue
			}
			if candidate, exists := value.(map[string]any); exists {
				item, accountKey = candidate, key
				break
			}
		}
	}
	if item == nil {
		return model.AccountPlanCheckResult{}, fmt.Errorf("未找到可解析的账号条目")
	}
	account := childMap(item, "account")
	entitlement := childMap(item, "entitlement")
	planType := firstNonEmptyString(textValue(account["plan_type"]), claims.PlanType)
	subscriptionPlan := textValue(entitlement["subscription_plan"])
	isFree := strings.EqualFold(planType, "free") || strings.EqualFold(subscriptionPlan, "chatgptfreeplan")
	plusCampaign := childMap(childMap(item, "eligible_promo_campaigns"), "plus")
	return model.AccountPlanCheckResult{
		AccountID:             firstNonEmptyString(textValue(account["account_id"]), accountKey, claims.AccountID),
		CurrentPlanType:       planType,
		SubscriptionPlan:      subscriptionPlan,
		HasActiveSubscription: boolValue(entitlement["has_active_subscription"]),
		PlusTrialEligible:     isFree && len(plusCampaign) > 0,
		ExpiresAt:             textValue(entitlement["expires_at"]),
		RenewsAt:              textValue(entitlement["renews_at"]),
		BillingPeriod:         textValue(entitlement["billing_period"]),
		BillingCurrency:       textValue(entitlement["billing_currency"]),
	}, nil
}

func childMap(parent map[string]any, key string) map[string]any {
	if parent == nil {
		return nil
	}
	value, _ := parent[key].(map[string]any)
	return value
}

func textValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stablePlanDeviceID(seed string) string {
	digest := sha256.Sum256([]byte("chatgpt-plan:" + strings.ToLower(strings.TrimSpace(seed))))
	raw := append([]byte(nil), digest[:16]...)
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func retryablePlanCheck(status int, err error) bool {
	if err != nil && status == 0 {
		return true
	}
	return status == 408 || status == 409 || status == 425 || status == 429 || status >= 500
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
