package gptpay

import (
	"encoding/json"
	"fmt"
	"strings"
)

func SupportedPlan(provider, plan string) bool {
	if Provider(provider) == XUPay {
		return plan == "pro5" || plan == "pro20"
	}
	if Provider(provider) == CMSNav {
		return plan == "pro5" || plan == "pro20" || plan == "pro50"
	}
	switch plan {
	case "go", "plus", "pro5", "pro20", "pro50", "pro5_up", "pro20_up", "pro50_up", "codex_point":
		return Provider(provider) == Tokenseek
	}
	return false
}

func TokenseekCountry(country string) bool {
	switch country {
	case "PH", "EG", "CL", "US", "JP":
		return true
	}
	return false
}

// Unknown subscription state remains unknown; the supplier performs the final
// eligibility check. Reject explicitly incompatible sessions before reserving a card.
func ValidatePlanSession(cfg Settings, session Session) error {
	if Provider(cfg.Provider) != Tokenseek {
		return nil
	}
	plan := strings.ToLower(strings.TrimSpace(session.Account.PlanType))
	if plan == "" {
		return nil
	}
	if strings.HasSuffix(cfg.PlanCode, "_up") && plan != "plus" && plan != "chatgptplus" {
		return fmt.Errorf("Plus 升级仅支持当前 Plus 订阅，当前 Session 套餐为 %s", plan)
	}
	if cfg.PlanCode == "codex_point" && (plan == "free" || plan == "chatgptfree") {
		return fmt.Errorf("Codex 点数要求已有有效订阅，Free 账号不可购买")
	}
	return nil
}

// Only new orders copy the chosen country into the wire input. Legacy Tokenseek
// snapshots have an unused top-level Country; replay must continue omitting it.
func NewSnapshot(cfg Settings, key string, card CardSecret, session Session) Snapshot {
	input := CreateInput{PlanCode: cfg.PlanCode, CardSecret: card, Session: session}
	input.Session.SessionToken = ""
	sessionToken := session.SessionToken
	{
		var web map[string]json.RawMessage
		if json.Unmarshal(session.Raw, &web) == nil {
			for _, name := range []string{"sessionToken", "session_token"} {
				var token string
				if json.Unmarshal(web[name], &token) == nil && strings.TrimSpace(token) != "" {
					sessionToken = token
					break
				}
			}
		}
	}
	if Provider(cfg.Provider) == Tokenseek {
		input.Country = cfg.Country
		input.CreditQuantity = cfg.CreditQuantity
	}
	snapshot := Snapshot{Provider: cfg.Provider, Country: cfg.Country, URL: cfg.URL, APIKey: key, Input: input, WebSessionToken: sessionToken}
	if Provider(cfg.Provider) == XUPay {
		snapshot.XUPayProductID = map[string]string{"pro5": "pro5x", "pro20": "pro20x"}[cfg.PlanCode]
	}
	if Provider(cfg.Provider) == CMSNav || Provider(cfg.Provider) == XUPay {
		accepted := cfg.TermsAccepted
		snapshot.TermsAccepted = &accepted
	}
	return snapshot
}
