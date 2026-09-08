package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestParseAccountPlanResponseSelectsJWTAccountAndPlusTrial(t *testing.T) {
	payload := map[string]any{"accounts": map[string]any{
		"default": map[string]any{"account": map[string]any{"plan_type": "plus"}},
		"account-1": map[string]any{
			"account":                  map[string]any{"account_id": "account-1", "plan_type": "free"},
			"entitlement":              map[string]any{"subscription_plan": "chatgptfreeplan", "has_active_subscription": false},
			"eligible_promo_campaigns": map[string]any{"plus": map[string]any{"id": "trial-1"}},
		},
	}}
	result, err := parseAccountPlanResponse(payload, model.UserInfo{AccountID: "account-1", PlanType: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account-1" || result.CurrentPlanType != "free" || !result.PlusTrialEligible {
		t.Fatalf("unexpected exact-account result: %+v", result)
	}
}

func TestParseAccountPlanResponseFallsBackAndReadsPaidDetails(t *testing.T) {
	item := map[string]any{
		"account": map[string]any{"account_id": "paid-account", "plan_type": "pro"},
		"entitlement": map[string]any{
			"subscription_plan": "chatgptproplan", "has_active_subscription": true,
			"expires_at": "2026-10-01T00:00:00Z", "renews_at": "2026-09-30T00:00:00Z",
			"billing_period": "monthly", "billing_currency": "USD",
		},
	}
	for name, accounts := range map[string]map[string]any{
		"default":           {"default": item},
		"first non-default": {"paid-account": item},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := parseAccountPlanResponse(map[string]any{"accounts": accounts}, model.UserInfo{})
			if err != nil {
				t.Fatal(err)
			}
			if result.CurrentPlanType != "pro" || !result.HasActiveSubscription || result.BillingPeriod != "monthly" || result.BillingCurrency != "USD" || result.ExpiresAt == "" || result.RenewsAt == "" {
				t.Fatalf("unexpected paid result: %+v", result)
			}
		})
	}
}

func TestCheckAccountPlanUsesConfiguredProxyHeadersAndRetries429(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Header.Get("Authorization") == "" || r.Header.Get("oai-device-id") == "" {
			t.Errorf("missing auth/device headers: %#v", r.Header)
		}
		if got := r.Header.Get("x-openai-target-path"); got != "/backend-api/accounts/check/v4-2023-04-27" {
			t.Errorf("target header = %q", got)
		}
		if r.URL.Query().Get("timezone_offset_min") != "-" {
			t.Errorf("timezone query = %q", r.URL.RawQuery)
		}
		if call == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": map[string]any{"default": map[string]any{
			"account":     map[string]any{"account_id": "account-1", "plan_type": "plus"},
			"entitlement": map[string]any{"subscription_plan": "chatgptplusplan", "has_active_subscription": true},
		}}})
	}))
	defer proxy.Close()

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request bypassed configured proxy")
	}))
	defer target.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = target.URL
	settings.ProxyURL = proxy.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	result := client.CheckAccountPlan(context.Background(), makeToken("user-1", "account-1", "one@example.com"))
	if !result.OK || result.CurrentPlanType != "plus" || result.AttemptCount != 2 || calls.Load() != 2 {
		t.Fatalf("unexpected retry result: %+v calls=%d", result, calls.Load())
	}
}

func TestCheckAccountPlanDoesNotRetryUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = server.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	result := client.CheckAccountPlan(context.Background(), makeToken("user-1", "account-1", "one@example.com"))
	if result.OK || result.HTTPStatus != http.StatusUnauthorized || result.AttemptCount != 1 || result.Error == "" {
		t.Fatalf("unexpected unauthorized result: %+v", result)
	}
}
