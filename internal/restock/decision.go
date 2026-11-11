package restock

import (
	"math"
	"time"
)

// ObserveWindows never interprets resets, stale observations, or plan changes as consumption.
func ObserveWindows(current, previous []Window, samePlan bool, maxAge time.Duration) []Window {
	for i := range current {
		w := &current[i]
		w.Rate, w.Samples = 0, 0
		w.ObservedRate, w.SampleSeconds = nil, 0
		if !samePlan {
			continue
		}
		for _, p := range previous {
			elapsed := w.At.Sub(p.At)
			if p.Seconds != w.Seconds || p.ResetAt != w.ResetAt || elapsed < time.Second || elapsed > maxAge || !validWindow(w.Used, 0) || !validWindow(p.Used, p.Rate) || w.Used < p.Used {
				continue
			}
			rate := (w.Used - p.Used) / elapsed.Minutes()
			w.ObservedRate, w.SampleSeconds = &rate, elapsed.Seconds()
			// React to rising consumption immediately; decay more slowly after a fall.
			w.Rate = math.Max(rate, p.Rate*.6+rate*.4)
			w.Samples = p.Samples + 1
			break
		}
	}
	return current
}
func Pressure(c Config, s Snapshot) bool {
	return pressureAt(c, s, s.At)
}
func availableAccount(a Account, now time.Time) bool {
	// The snapshot already contains only members of the selected monitoring group.
	// Membership in another push group does not remove this account's capacity.
	return a.Ready && !a.SupplyExcluded && (a.ExpiresAt == nil || a.ExpiresAt.After(now))
}
func pressureAt(c Config, s Snapshot, now time.Time) bool {
	if !s.Valid || !s.LoadValid {
		return false
	}
	capacity, used, queue := 0, 0, 0
	for _, a := range s.Accounts {
		if availableAccount(a, now) {
			capacity += a.Capacity
		}
		if availableAccount(a, now) || (a.SupplyExcluded && a.Ready) {
			used += a.Used
		}
		queue += a.Waiting
	}
	return queue > 0 || (used > 0 && used >= c.ConcurrencyTrigger(capacity))
}

func Evaluate(c Config, s Snapshot, tasks []Task, limits Limits, confirmations, newSlots int, lead float64, now time.Time) (d Decision) {
	d = Decision{At: now, Version: c.Version, Snapshot: s, Config: c, Limits: limits, Confirmations: confirmations, LeadMinutes: lead, TriggerMinutes: float64(c.TriggerMinutes), TargetMinutes: float64(c.TargetMinutes), Warnings: []string{}}
	d.Trace = &DecisionTrace{Version: 1, NewAccountCapacity: newSlots, EffectiveNewAccountCapacity: c.EffectiveCapacity(newSlots), StopStage: "data"}
	// Keep forecasts, demand and collection errors visible while suppressing
	// every trigger, including startup, replacement and emergency demand.
	defer func() {
		if wait := WaitForBatch(s, tasks); wait != nil {
			d.BatchWait = wait
			d.Suggested, d.Reason = 0, wait.Message
			d.Trace.StopStage = "batch"
		}
		if reason := c.NoRestockReason(now); reason != "" {
			if !d.DemandKnown && d.Reason != "" {
				d.Warnings = append(d.Warnings, d.Reason)
			}
			d.NoRestockActive = true
			d.Suggested, d.Reason = 0, reason
			d.Trace.StopStage = "daily_pause"
		}
		d.Trace.EvaluationReason = d.Reason
	}()
	if c.Early {
		d.TriggerMinutes = math.Max(d.TriggerMinutes, lead+float64(c.MarginMinutes))
		d.TargetMinutes = math.Max(d.TargetMinutes, d.TriggerMinutes+1)
	}
	for _, a := range s.Accounts {
		if availableAccount(a, now) {
			d.Usable++
			d.ReportedCapacity += a.Capacity
		}
		if availableAccount(a, now) || (a.SupplyExcluded && a.Ready) {
			d.Used += a.Used
		}
		d.Waiting += a.Waiting
	}
	d.Capacity = c.EffectiveCapacity(d.ReportedCapacity)
	d.Trace.ConcurrencyTrigger = c.ConcurrencyTrigger(d.ReportedCapacity)
	timely, groupInflight := 0, 0
	for _, t := range tasks {
		if t.Active() {
			d.Inflight++
			if t.Binding == s.Binding && t.GroupID == s.GroupID {
				groupInflight++
			}
			if t.Predictable() && t.Binding == s.Binding && t.GroupID == s.GroupID && !t.ETA.After(now.Add(time.Duration(lead*float64(time.Minute)))) {
				timely++
			}
		}
	}
	d.Trace.TimelyInflight, d.Trace.GroupInflight = timely, groupInflight
	block := func(reason string) Decision { d.Suggested = 0; d.Reason = reason; return d }
	if !s.Valid || s.At.IsZero() || now.Sub(s.At) > time.Duration(c.FreshSeconds)*time.Second || s.At.After(now.Add(5*time.Second)) {
		return block(first(s.Error, "监控快照缺失或过期，暂停新增"))
	}
	faults := 0
	for _, a := range s.Accounts {
		if a.Fault {
			faults++
		}
	}
	if faults > 0 && faults*100 >= len(s.Accounts)*c.FaultPercent {
		return block("监控组大量账号异常或过载，先排查上游；暂停新增")
	}
	if !s.LoadValid {
		return block(first(s.Error, "并发采集完整性未知，暂停新增"))
	}
	if newSlots < 1 {
		return block("推送设置的单账号并发无效")
	}
	d.DemandKnown = true
	exit := ForecastExits(c, s, tasks, newSlots, lead, now)
	d.Exit = &exit
	if c.ExitEnabled && exit.Confirmed {
		d.ExitNeed = exit.Need
	}
	if c.ExitEnabled && float64(c.ExitMinutes) < lead+float64(c.MarginMinutes) {
		d.Warnings = append(d.Warnings, "退出前补货提前时间小于预计就绪耗时＋安全余量，可能来不及接替；仍采用手动配置时间")
	}
	if c.ExitEnabled && exit.TransientGap {
		d.Warnings = append(d.Warnings, "账号退出时可能短暂容量不足；新增数量按任务能够到位时的缺口计算，不因晚到重复补货")
	}
	d.Trace.StopStage = "rules"
	d.ForceMerge = ForceMergeSummary(c, s, tasks, now)
	if d.ForceMerge.Triggered {
		d.ForceMergeNeed = len(d.ForceMerge.Sources)
	}
	if d.ForceMerge.Unknown > 0 {
		d.Warnings = append(d.Warnings, "部分 EJH 账号时限或首次推送记录不明，已排除，不按 0 分钟触发")
	}
	arrival := now.Add(time.Duration(lead * float64(time.Minute)))
	futureCapacity := 0
	for _, a := range s.Accounts {
		if usableAt(a, arrival, now, time.Duration(c.QuotaSeconds*2)*time.Second) {
			futureCapacity += a.Capacity
		}
	}
	if c.ConcurrencyEnabled && confirmations >= c.Confirmations && pressureAt(c, s, now) {
		d.Trace.ConcurrencyTriggered = true
		_, targetPercent := c.CapacityThresholds(d.ReportedCapacity)
		d.ConcurrencyNeed = max(0, int(math.Ceil((float64(d.Used+d.Waiting)*100/float64(targetPercent)-c.EffectiveCapacity(futureCapacity+timely*newSlots))/c.EffectiveCapacity(newSlots))))
	}
	d.Trace.FutureCapacity = c.EffectiveCapacity(futureCapacity)
	timeWithinHorizon := lead < float64(c.ForecastMinutes) && d.TargetMinutes <= float64(c.ForecastMinutes)
	if c.TimeEnabled && !timeWithinHorizon {
		d.Warnings = append(d.Warnings, "预计就绪或补后目标超出预测上限，暂停时间侧新增；并发和最低备用仍独立生效")
	}
	d.Money = ForecastMoney(c, s, now)
	d.Coverage = d.Money.Coverage
	if d.Money.State == "ready" {
		minutes := *d.Money.RemainingUSD / *d.Money.RateUSD
		if moneyNumber(minutes) {
			d.SupplyMinutes = &minutes
		}
	}
	if c.TimeEnabled && timeWithinHorizon && d.SupplyMinutes != nil && *d.SupplyMinutes <= d.TriggerMinutes {
		d.Trace.TimeTriggered = true
		if *d.SupplyMinutes < lead {
			d.Warnings = append(d.Warnings, "新号就绪前可能出现额度缺口")
		}
		var warning string
		d.TimeNeed, warning, d.Trace.MoneySizing = moneyTimeNeed(c, s, d.Money, tasks, d.TargetMinutes, lead, now)
		if warning != "" {
			d.Warnings = append(d.Warnings, warning)
		}
	}

	d.ReserveNeed = max(0, c.Reserve-d.Usable-timely)
	// An empty group has no consumption samples or pressure to confirm. All
	// matching active intents count for deduplication, even without an ETA.
	emptyGroup := len(s.Accounts) == 0
	if s.MonitorOAuthCount != nil {
		emptyGroup = *s.MonitorOAuthCount == 0
	}
	if s.MonitorOAuthError != "" {
		emptyGroup = false
		d.Warnings = append(d.Warnings, s.MonitorOAuthError)
	}
	if emptyGroup && c.EmptyGroupCount > 0 {
		d.EmptyGroupNeed = max(0, c.EmptyGroupCount-groupInflight)
	}
	need := max(d.TimeNeed, d.ConcurrencyNeed, d.ReserveNeed, d.EmptyGroupNeed, d.ForceMergeNeed, d.ExitNeed)
	riskReplacement := PaymentRiskPending(s, tasks)
	if riskReplacement {
		need = 1
	}
	d.Trace.NetNeed = need
	if need == 0 {
		if emptyGroup && c.EmptyGroupCount > 0 && groupInflight > 0 {
			return block("空分组启动数量已由同组在途覆盖，等待原任务入组；结果未知仍保留占用")
		}
		return block("没有已确认的净缺口；继续采样并跟踪在途")
	}
	if limits.Failures >= c.FailureLimit {
		d.Trace.StopStage = "breaker"
		return block("连续失败已达熔断阈值，处理原因后在配置页重置熔断")
	}
	cooldown := c.CooldownSeconds
	if d.Waiting >= c.EmergencyQueue || (c.ExitEnabled && d.ExitNeed > 0 && d.Exit != nil && d.Exit.Emergency) {
		cooldown = c.EmergencyCooldownSeconds
	}
	d.Trace.CooldownSeconds = &cooldown
	if limits.LastBatch != nil && now.Sub(*limits.LastBatch) < time.Duration(cooldown)*time.Second {
		d.Trace.StopStage = "cooldown"
		d.Trace.CooldownRemainingSeconds = int(math.Ceil((time.Duration(cooldown)*time.Second - now.Sub(*limits.LastBatch)).Seconds()))
		return block("正常／紧急冷却尚未结束")
	}
	d.Trace.StopStage = "limits"
	d.Trace.Caps = []DecisionCap{{"batch", c.MaxBatch}, {"inflight", c.MaxInflight - d.Inflight}, {"pool", c.PoolLimit - len(s.Accounts) - d.Inflight}, {"hour", c.HourLimit - limits.HourCount}, {"day", c.DayLimit - limits.DayCount}}
	hard := min(c.MaxBatch, c.MaxInflight-d.Inflight, c.PoolLimit-len(s.Accounts)-d.Inflight, c.HourLimit-limits.HourCount, c.DayLimit-limits.DayCount)
	if c.HourBudget > 0 {
		remaining := int(max(int64(0), c.HourBudget-limits.HourCost) / max(int64(1), c.UnitCost))
		hard = min(hard, remaining)
		d.Trace.Caps = append(d.Trace.Caps, DecisionCap{"hour_budget", remaining})
	}
	if c.DayBudget > 0 {
		remaining := int(max(int64(0), c.DayBudget-limits.DayCost) / max(int64(1), c.UnitCost))
		hard = min(hard, remaining)
		d.Trace.Caps = append(d.Trace.Caps, DecisionCap{"day_budget", remaining})
	}
	d.Trace.HardLimit = &hard
	if riskReplacement {
		hard = min(hard, 1)
	}
	strictMinimum := !c.AllowSmall && !d.EmptyGroupOnly() && !riskReplacement
	d.Trace.StrictMinimum = strictMinimum
	if strictMinimum && hard < c.MinBatch {
		return block("在途、池容量或预算余量不足最小批量")
	}
	if strictMinimum {
		need = max(need, c.MinBatch)
	}
	d.Trace.AfterMinimum = &need
	d.Suggested = max(0, min(need, hard))
	if d.Suggested == 0 {
		return block("达到在途、账号池、小时／日数量或预算上限")
	}
	d.Trace.StopStage = "complete"
	d.Reason = "时间、并发、退出前补货、备用、空分组与强制合并接替净缺口取最大值，各规则已扣除适用在途"
	if d.EmptyGroupOnly() {
		d.Reason = "监控分组为 0 个账号，按空分组启动数量补货；已扣除同组全部在途"
	}
	if riskReplacement {
		d.Reason = "原账号支付风控且订单已确认失败，仅安排一个新账号替补；替补再次风控将暂停自动补货"
	}
	if !c.Enabled {
		d.Reason = "总开关关闭，仅展示建议"
	} else if c.Mode == "observe" {
		d.Reason = "观察模式，仅计算建议"
	}
	return d
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func usableAt(a Account, t, now time.Time, maxAge time.Duration) bool {
	if a.SupplyExcluded || !a.Ready || (a.ExpiresAt != nil && !a.ExpiresAt.After(t)) {
		return false
	}
	for _, w := range a.Windows {
		if w.Fresh(now, maxAge) && w.ResetAt > t.Unix() && w.Used+w.Rate*math.Max(0, t.Sub(w.At).Minutes()) >= 100 {
			return false
		}
	}
	return true
}

func validWindow(used, rate float64) bool {
	return !math.IsNaN(used) && !math.IsInf(used, 0) && !math.IsNaN(rate) && !math.IsInf(rate, 0) && used >= 0 && used <= 100 && rate >= 0
}
