package workflow

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// ExtractAccessToken accepts either a raw JWT or a Session JSON object (the
// format exported by chatgpt.com/sub2api). It deliberately only returns the
// accessToken/access_token field and never persists or logs the surrounding
// session payload.
func ExtractAccessToken(value string) string {
	value = strings.TrimSpace(value)
	if strings.Count(value, ".") == 2 && !strings.HasPrefix(value, "{") && !strings.HasPrefix(value, "[") {
		return value
	}
	var parsed any
	if json.Unmarshal([]byte(value), &parsed) != nil {
		return value
	}
	if token := findAccessToken(parsed); token != "" {
		return token
	}
	return value
}

func ExtractRefreshToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || (!strings.HasPrefix(value, "{") && !strings.HasPrefix(value, "[")) {
		return value
	}
	var parsed any
	if json.Unmarshal([]byte(value), &parsed) != nil {
		return value
	}
	return findCredentialToken(parsed, []string{"refreshToken", "refresh_token"})
}

func findCredentialToken(value any, keys []string) string {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if token, ok := item[key].(string); ok && strings.TrimSpace(token) != "" {
				return strings.TrimSpace(token)
			}
		}
		for _, child := range item {
			if token := findCredentialToken(child, keys); token != "" {
				return token
			}
		}
	case []any:
		for _, child := range item {
			if token := findCredentialToken(child, keys); token != "" {
				return token
			}
		}
	}
	return ""
}

func findAccessToken(value any) string {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"accessToken", "access_token"} {
			if token, ok := item[key].(string); ok && strings.TrimSpace(token) != "" {
				return strings.TrimSpace(token)
			}
		}
		for _, child := range item {
			if token := findAccessToken(child); token != "" {
				return token
			}
		}
	case []any:
		for _, child := range item {
			if token := findAccessToken(child); token != "" {
				return token
			}
		}
	}
	return ""
}

func DecodeUserInfo(token string) (model.UserInfo, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return model.UserInfo{}, errors.New("不是有效的 JWT（应包含三段）")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return model.UserInfo{}, errors.New("JWT payload 无法解码")
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return model.UserInfo{}, errors.New("JWT payload 不是有效 JSON")
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	info := model.UserInfo{
		UserID: stringClaim(auth, "chatgpt_user_id"), AccountID: stringClaim(auth, "chatgpt_account_id"),
		PlanType: stringClaim(auth, "chatgpt_plan_type"), Email: stringClaim(profile, "email"), Name: stringClaim(profile, "name"),
	}
	if info.UserID == "" {
		return model.UserInfo{}, fmt.Errorf("JWT 中缺少 chatgpt_user_id")
	}
	if info.Email == "" {
		return model.UserInfo{}, fmt.Errorf("JWT 中缺少 email")
	}
	return info, nil
}

func stringClaim(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func AccessTokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return time.Time{}, false
	}
	expires, ok := claims["exp"].(float64)
	if !ok || expires <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(expires), 0), true
}
