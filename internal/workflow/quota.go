package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIResetCreditInfo is the small, sanitized projection needed by the UI.
// Upstream credit IDs are intentionally not exposed because this project only
// displays the remaining count (it does not consume credits).
type OpenAIResetCreditInfo struct {
	AvailableCount int       `json:"available_count"`
	FetchedAt      time.Time `json:"fetched_at"`
}

// QueryOpenAIResetCredits follows Sub2API's two-step query: /wham/usage is the
// primary snapshot and /wham/rate-limit-reset-credits provides authoritative
// reset-credit details when available.
func (c *Client) QueryOpenAIResetCredits(ctx context.Context, accessToken, accountID string) (OpenAIResetCreditInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return OpenAIResetCreditInfo{}, fmt.Errorf("Access Token 不能为空")
	}
	if strings.TrimSpace(accountID) == "" {
		return OpenAIResetCreditInfo{}, fmt.Errorf("缺少 chatgpt_account_id")
	}
	_, usageBody, err := c.quotaRequest(ctx, http.MethodGet, "/wham/usage", accessToken, accountID, nil)
	if err != nil {
		return OpenAIResetCreditInfo{}, err
	}
	result := OpenAIResetCreditInfo{AvailableCount: quotaCountFromPayload(usageBody), FetchedAt: time.Now()}
	// The detail endpoint is best-effort in Sub2API: if it is unavailable, the
	// count from /wham/usage is still useful and should not make the whole query
	// fail.
	if _, detailBody, detailErr := c.quotaRequest(ctx, http.MethodGet, "/wham/rate-limit-reset-credits", accessToken, accountID, nil); detailErr == nil {
		if count, ok := authoritativeQuotaCount(detailBody); ok {
			result.AvailableCount = count
		}
	}
	return result, nil
}

func (c *Client) quotaRequest(ctx context.Context, method, path, token, accountID string, payload any) (int, []byte, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("openai-beta", "codex-1")
	req.Header.Set("oai-language", "zh-CN")
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("sec-fetch-site", "none")
	req.Header.Set("sec-fetch-mode", "no-cors")
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("priority", "u=4, i")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/136.0.0.0 Safari/537.36")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var status int
	var data []byte
	if c.python != "" && strings.HasPrefix(c.baseURL, "https://") && strings.TrimSpace(c.settings.ProxyURL) != "" {
		status, data, err = c.browserDo(ctx, req, body)
	} else {
		var resp *http.Response
		resp, err = c.http.Do(req)
		if err == nil {
			defer resp.Body.Close()
			status = resp.StatusCode
			data, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		}
	}
	if err != nil {
		return status, nil, friendlyNetworkError(err)
	}
	if status < 200 || status >= 300 {
		message := responseMessage(data)
		if message == "" {
			message = http.StatusText(status)
		}
		return status, data, fmt.Errorf("HTTP %d: %s", status, message)
	}
	return status, data, nil
}

func quotaCountFromPayload(data []byte) int {
	count, _ := authoritativeQuotaCount(data)
	return count
}

func authoritativeQuotaCount(data []byte) (int, bool) {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return 0, false
	}
	return findQuotaCount(value)
}

func findQuotaCount(value any) (int, bool) {
	switch item := value.(type) {
	case []any:
		available := 0
		seen := false
		creditList := len(item) > 0
		for _, child := range item {
			if obj, ok := child.(map[string]any); ok {
				// A top-level detail response is commonly an array of credit
				// objects. Mark that shape even when every credit is redeemed so
				// an authoritative zero can override the /wham/usage fallback.
				if _, hasStatus := obj["status"]; !hasStatus {
					if _, hasResetType := obj["reset_type"]; !hasResetType {
						if _, hasResetTypeCamel := obj["resetType"]; !hasResetTypeCamel {
							creditList = false
						}
					}
				}
				status := strings.ToLower(strings.TrimSpace(fmt.Sprint(obj["status"])))
				resetType := strings.ToLower(strings.TrimSpace(fmt.Sprint(obj["reset_type"])))
				if (status == "" || status == "available") && (resetType == "" || resetType == "codex_rate_limits") {
					seen = true
					available++
				}
			}
		}
		if seen || creditList {
			return available, true
		}
		for _, child := range item {
			if count, ok := findQuotaCount(child); ok {
				return count, true
			}
		}
	case map[string]any:
		// An explicit available_count is authoritative over the optional credits
		// array. Do this in a separate pass because Go map iteration is random.
		for key, child := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if normalized == "availablecount" {
				switch number := child.(type) {
				case float64:
					if number >= 0 {
						return int(number), true
					}
				case string:
					var parsed int
					if _, err := fmt.Sscanf(strings.TrimSpace(number), "%d", &parsed); err == nil && parsed >= 0 {
						return parsed, true
					}
				}
			}
		}
		for _, child := range item {
			if count, ok := findQuotaCount(child); ok {
				return count, true
			}
		}
	}
	return 0, false
}
