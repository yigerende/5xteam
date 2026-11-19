package restock

import (
	"errors"
	"math"
	"time"
)

const QuotaRequestTimeout = 10 * time.Second

func (c Config) validateCadence() error {
	if c.CheckSeconds < 1 || c.QuotaSeconds < 1 || c.QuotaConcurrency < 1 {
		return errors.New("检查周期、额度刷新周期和额度查询并发数须为正整数")
	}
	if c.CooldownSeconds < 0 || c.EmergencyCooldownSeconds < 0 {
		return errors.New("冷却时间须为非负整数，0 表示不冷却")
	}
	// Reject only values that cannot be represented by the duration arithmetic.
	// These are numeric representation bounds, not business timing limits.
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	if int64(c.CheckSeconds) > maxSeconds-10 || int64(c.QuotaSeconds) > maxSeconds/3 || int64(c.CooldownSeconds) > maxSeconds || int64(c.EmergencyCooldownSeconds) > maxSeconds {
		return errors.New("运行节奏数值超出时间表示范围")
	}
	return nil
}

// A short refresh interval must still allow a request and the next monitor
// tick to complete; otherwise a valid result can expire before it is read.
func (c Config) QuotaFreshness() time.Duration {
	return max(time.Duration(c.QuotaSeconds)*2*time.Second, time.Duration(c.CheckSeconds)*time.Second+QuotaRequestTimeout)
}

func (c Config) QuotaObservationAge() time.Duration {
	return max(time.Duration(c.QuotaSeconds)*3*time.Second, c.QuotaFreshness())
}
