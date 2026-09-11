package httpapi

import (
	"strings"

	"chatgpt-space-merge/internal/model"
)

// This value is local to one job, including all of its proxy retries.
type oauthLoginSelection struct {
	credentials model.MailAccountCredentials
	configured  string
	selected    string
	fallback    string
	reason      string
}

func selectOAuthLogin(mode string, credentials model.MailAccountCredentials) oauthLoginSelection {
	selection := oauthLoginSelection{credentials: credentials, configured: "email_otp", selected: "email_otp"}
	if mode != "password_totp" {
		return selection
	}
	selection.configured = mode
	password := strings.TrimSpace(credentials.GptPassword) != ""
	totp := strings.TrimSpace(credentials.TotpSecret) != ""
	switch {
	case password && totp:
		selection.selected = "password_totp"
	case !password && !totp:
		selection.fallback, selection.reason = "missing_password_and_totp", "缺少 ChatGPT 密码和 OpenAI TOTP 密钥，使用邮箱验证码登录"
	case !password:
		selection.fallback, selection.reason = "missing_password", "缺少 ChatGPT 密码，使用邮箱验证码登录"
	default:
		selection.fallback, selection.reason = "missing_totp", "缺少 OpenAI TOTP 密钥，使用邮箱验证码登录"
	}
	return selection
}

func (selection oauthLoginSelection) details() map[string]any {
	return map[string]any{
		"configured_login_mode": selection.configured, "selected_login_mode": selection.selected,
		"login_fallback_reason": selection.fallback, "login_method_reason": selection.reason,
	}
}

func (selection oauthLoginSelection) apply(payload map[string]any) {
	for key, value := range selection.details() {
		payload[key] = value
	}
	payload["login_mode"] = selection.selected
	// Email OTP may still be followed by a server-requested TOTP challenge.
	payload["gpt_password"], payload["totp_secret"] = "", selection.credentials.TotpSecret
	if selection.selected == "password_totp" {
		payload["gpt_password"] = selection.credentials.GptPassword
	}
}

var oauthLoginDetailKeys = []string{
	"configured_login_mode", "selected_login_mode", "login_fallback_reason", "login_method_reason",
	"login_method", "login_method_label", "login_auth_status",
}
