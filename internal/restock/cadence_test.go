package restock

import (
	"testing"
	"time"
)

func TestCadenceCustomSettings(t *testing.T) {
	c := DefaultConfig()
	c.CheckSeconds, c.QuotaSeconds, c.QuotaConcurrency = 1, 10, 8
	c.CooldownSeconds, c.EmergencyCooldownSeconds = 0, 120
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.CheckSeconds, c.QuotaSeconds = 5000, 5000
	c.CooldownSeconds, c.EmergencyCooldownSeconds = 100000, 200000
	if err := c.Validate(); err != nil {
		t.Fatal("former limits still enforced", err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.CheckSeconds = 0 },
		func(c *Config) { c.QuotaSeconds = -1 },
		func(c *Config) { c.QuotaConcurrency = 0 },
		func(c *Config) { c.CooldownSeconds = -1 },
		func(c *Config) { c.EmergencyCooldownSeconds = -1 },
		func(c *Config) { c.QuotaSeconds = int(1 << 62) },
	} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid cadence accepted")
		}
	}
}

func TestCadenceShortQuotaFreshness(t *testing.T) {
	c := DefaultConfig()
	if c.QuotaFreshness() != 120*time.Second || c.QuotaObservationAge() != 180*time.Second {
		t.Fatal("default freshness changed")
	}
	c.QuotaSeconds, c.CheckSeconds = 1, 5
	w := Window{Seconds: 604800, At: time.Now().Add(-4 * time.Second), ResetAt: time.Now().Add(time.Hour).Unix()}
	if !w.Fresh(time.Now(), c.QuotaFreshness()) {
		t.Fatal("successful result expired before monitor read")
	}
	w.At = time.Now().Add(-time.Minute)
	if w.Fresh(time.Now(), c.QuotaFreshness()) {
		t.Fatal("stale result accepted")
	}
}
