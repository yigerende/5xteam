package httpapi

import (
	"context"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProMergeV2NetworkRecoveryUsesSeparateBudgetAndCleansOnlyAtEnd(t *testing.T) {
	for _, succeeds := range []bool{true, false} {
		f := mergeV2Fixture(t)
		cfg, _, _, _ := f.s.store.ProSettings()
		cfg.RetryCount, cfg.RetryIntervalSeconds = 2, 10
		if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, ""); err != nil {
			t.Fatal(err)
		}
		calls := 0
		f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
			calls++
			if succeeds && calls == 4 {
				return mockMergeV2Submit(ctx)
			}
			return map[string]any{"success": false, "retryable": true, "error": "curl: (28) Operation timed out"}, nil
		}
		for n := 1; n <= 6; n++ {
			p, err := f.s.performProMergeV2(t.Context(), f.email)
			if succeeds && n == 4 {
				if err != nil || !p.MergeV2Completed() {
					t.Fatal(p.ProMergeV2, err)
				}
				break
			}
			r := p.ProMergeV2.Recovery
			if err == nil || calls != n || f.calls["remove"] != 0 || r.NextAt == nil {
				t.Fatal("network attempt not retained", n, calls, p.ProMergeV2, err)
			}
			if n < 6 {
				if r.Phase != "login" || !r.NetworkFailure || r.Limit(cfg) != 6 || time.Until(*r.NextAt) < 59*time.Second {
					t.Fatal("wrong network budget/delay", r)
				}
				if err := f.s.store.RecoverProWorkflows(); err != nil {
					t.Fatal(err)
				}
			} else if r.Phase != "cleanup" {
				t.Fatal("exhaustion must clean up", r)
			}
			dueRecovery(t, f)
		}
		if !succeeds {
			p, err := f.s.performProMergeV2(t.Context(), f.email)
			if err == nil || !p.ProMergeV2.Recovery.Exhausted || p.MergeV2Completed() || f.calls["remove"] != 1 || calls != 6 {
				t.Fatal("unbounded retry or false success", p.ProMergeV2, err)
			}
		}
	}
}

func TestProMergeV2NetworkSettingsValidation(t *testing.T) {
	defaultSettings := model.DefaultProSettings().MergeV2RecoveryConfig()
	if defaultSettings.NetworkRetryCount() != 5 || defaultSettings.NetworkDelay() != 60 {
		t.Fatal(defaultSettings)
	}
	for _, n := range []int{-1, 0, 20, 21} {
		c := defaultSettings
		c.NetworkRetries = &n
		if (c.Validate() == nil) != (n >= 0 && n <= 20) {
			t.Fatal("invalid retries accepted", n)
		}
	}
}
