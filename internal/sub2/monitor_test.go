package sub2

import (
	"testing"
	"time"
)

func TestMonitorReadyPreservesAccountAvailabilityGuards(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Minute), now.Add(-time.Minute)
	expired, expires := now.Unix(), future.Unix()
	for _, tc := range []struct {
		name   string
		change func(*MonitorAccount)
		want   bool
	}{
		{"active", func(a *MonitorAccount) {}, true},
		{"partial_models", func(a *MonitorAccount) { a.Credentials.Models = map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"} }, true},
		{"wrong_platform", func(a *MonitorAccount) { a.Platform = "anthropic" }, false},
		{"disabled", func(a *MonitorAccount) { a.Status = "disabled" }, false},
		{"error", func(a *MonitorAccount) { a.Status = "error" }, false},
		{"unschedulable", func(a *MonitorAccount) { a.Schedulable = false }, false},
		{"no_capacity", func(a *MonitorAccount) { a.Concurrency = 0 }, false},
		{"expired", func(a *MonitorAccount) { a.ExpiresAt = &expired }, false},
		{"not_expired", func(a *MonitorAccount) { a.ExpiresAt = &expires }, true},
		{"rate_limited", func(a *MonitorAccount) { a.RateLimitResetAt = &future }, false},
		{"overloaded", func(a *MonitorAccount) { a.OverloadUntil = &future }, false},
		{"temporary", func(a *MonitorAccount) { a.TempUntil = &future }, false},
		{"recovered", func(a *MonitorAccount) { a.RateLimitResetAt = &past; a.OverloadUntil = &past; a.TempUntil = &past }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := MonitorAccount{Platform: "openai", Status: "active", Schedulable: true, Concurrency: 60}
			tc.change(&a)
			if got := a.Ready(now); got != tc.want {
				t.Fatalf("ready=%v want=%v", got, tc.want)
			}
		})
	}
}
