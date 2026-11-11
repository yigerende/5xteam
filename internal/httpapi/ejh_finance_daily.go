package httpapi

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
)

// Project the same retained financial totals into account and card details.
// Daily reconciliation can capture late requests after the last Pro cost poll;
// it must not update operational Pro counters or lose revenue on deletion.
func applyEJHFinancialConsumption(cases []ejh.Case, history []store.EJHConsumption) {
	totals := map[string]float64{}
	seen := map[string]bool{}
	for _, row := range history {
		if seen[row.ID] || math.IsNaN(row.Total) || math.IsInf(row.Total, 0) || row.Total < 0 {
			continue
		}
		seen[row.ID] = true
		key := strings.ToLower(strings.TrimSpace(row.Email))
		if key != "" {
			totals[key] += row.Total
		}
	}
	for i := range cases {
		amount, ok := totals[strings.ToLower(strings.TrimSpace(cases[i].Email))]
		if ok {
			cases[i].ConsumptionState = ""
		}
		if ok && (cases[i].ConsumptionTotal == nil || amount > *cases[i].ConsumptionTotal) {
			cases[i].ConsumptionTotal = &amount
		}
	}
}

func ejhDailyDue(row store.EJHConsumption, now time.Time) bool {
	if row.DownstreamID < 1 || row.StartDay == "" || row.EndDay == "" || row.SourceAt == nil {
		return false
	}
	if row.DailyAttemptAt != nil && now.Sub(*row.DailyAttemptAt) < time.Minute {
		return false
	}
	if row.DailyError != "" && row.DailyAttemptAt != nil && now.Sub(*row.DailyAttemptAt) < 5*time.Minute {
		return false
	}
	if row.DailyCheckedAt == nil || row.DailySourceAt == nil || row.SourceAt.After(*row.DailySourceAt) {
		return true
	}
	// Refresh a completed day once more after midnight to retain final requests
	// that finished after the Pro account's last quota/cost poll.
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	end, err := time.ParseInLocation("2006-01-02", row.EndDay, zone)
	return err == nil && row.DailyCheckedAt.Before(end.AddDate(0, 0, 1).Add(10*time.Minute)) && now.Sub(*row.DailyCheckedAt) >= 5*time.Minute
}

func (s *Server) syncEJHDailyConsumption(parent context.Context) {
	if s.sub2 == nil {
		return
	}
	settings, password, _, err := s.store.ProSettings()
	if err != nil || password == "" {
		return
	}
	rows, err := store.EJHRecords[store.EJHConsumption](s.store, "finance_usage")
	if err != nil {
		return
	}
	// Oldest attempt first prevents an unreachable account from starving others.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].DailyAttemptAt, rows[j].DailyAttemptAt
		if a == nil {
			return b != nil
		}
		return b != nil && a.Before(*b)
	})
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	count := 0
	for _, row := range rows {
		if ctx.Err() != nil || count >= 12 {
			return
		}
		if !ejhDailyDue(row, time.Now()) {
			continue
		}
		count++
		failure := ""
		if row.ConnectionIdentity != proCostConnectionIdentity(settings.Sub2) {
			failure = "原 Sub2 连接与当前配置不同，保留历史数据"
		}
		zone := time.FixedZone("Asia/Shanghai", 8*3600)
		start, e1 := time.ParseInLocation("2006-01-02", row.StartDay, zone)
		end, e2 := time.ParseInLocation("2006-01-02", row.EndDay, zone)
		if e1 != nil || e2 != nil || end.Before(start) || end.Sub(start) > 90*24*time.Hour {
			failure = "消费日期范围无效或超过 90 天，历史数据待核实"
		}
		amounts := make(map[string]float64)
		if failure == "" {
			for at := start; !at.After(end); at = at.AddDate(0, 0, 1) {
				day := at.Format("2006-01-02")
				value, queryErr := s.sub2.QueryDailyUserCost(ctx, settings.Sub2, password, row.DownstreamID, day)
				if queryErr != nil {
					failure = fmt.Sprintf("%s 每日用户扣费查询失败，保留历史数据", day)
					break
				}
				amounts[day] = value
			}
		}
		if err = s.store.SaveEJHDailyConsumption(row, amounts, time.Now(), failure); err != nil {
			_ = s.store.SaveEJHDailyConsumption(row, nil, time.Now(), err.Error())
		}
	}
}
