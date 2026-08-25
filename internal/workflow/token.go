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
