package clientaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ReportRow struct {
	ID              int64    `json:"id"`
	Email           string   `json:"email"`
	Requests        int64    `json:"requests"`
	Cost            string   `json:"cost"`
	Credits         string   `json:"credits"`
	Balance         string   `json:"balance"`
	Opening         *string  `json:"opening"`
	Closing         *string  `json:"closing"`
	Difference      *string  `json:"difference"`
	State           string   `json:"state"`
	Reasons         []string `json:"reasons"`
	Issues          int64    `json:"issues"`
	From            int64    `json:"snapshot_from"`
	To              int64    `json:"snapshot_to"`
	BaselineCost    string   `json:"snapshot_cost"`
	BaselineCredits string   `json:"snapshot_credits"`
}
type Report struct {
	Start      int64       `json:"start"`
	End        int64       `json:"end"`
	Status     Status      `json:"sync"`
	Rows       []ReportRow `json:"rows"`
	Total      int         `json:"total"`
	Requests   int64       `json:"requests"`
	Cost       string      `json:"cost"`
	Credits    string      `json:"credits"`
	Abnormal   int         `json:"abnormal"`
	Unknown    int         `json:"unknown"`
	Consistent int         `json:"consistent"`
	Difference string      `json:"difference"`
	Notes      []string    `json:"notes"`
}

func Period(mode, date, start, end string, now time.Time) (int64, int64, error) {
	day, e := time.ParseInLocation("2006-01-02", date, Beijing)
	if e != nil {
		day = time.Date(now.In(Beijing).Year(), now.In(Beijing).Month(), now.In(Beijing).Day(), 0, 0, 0, 0, Beijing)
		if date != "" {
			return 0, 0, e
		}
	}
	a, b := day, day.AddDate(0, 0, 1)
	switch mode {
	case "day", "":
	case "week":
		n := (int(day.Weekday()) + 6) % 7
		a = day.AddDate(0, 0, -n)
		b = a.AddDate(0, 0, 7)
	case "month":
		a = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, Beijing)
		b = a.AddDate(0, 1, 0)
	case "all":
		a = time.Unix(0, 0)
		b = now
	case "custom":
		a, e = time.ParseInLocation("2006-01-02T15:04", start, Beijing)
		if e != nil {
			return 0, 0, e
		}
		b, e = time.ParseInLocation("2006-01-02T15:04", end, Beijing)
		if e != nil {
			return 0, 0, e
		}
	default:
		return 0, 0, fmt.Errorf("时间范围无效")
	}
	if !b.After(a) {
		return 0, 0, fmt.Errorf("结束时间必须晚于开始时间")
	}
	return a.Unix(), b.Unix(), nil
}

var labels = map[string]string{"negative_value": "费用/用量出现负值", "zero_charge_usage": "有记录用量但零费用", "missing_request_id": "缺少请求编号", "price_difference": "费用与原价×倍率不符（需排除免费Fast等规则）", "missing_billing": "使用记录未匹配计费凭据", "missing_usage": "计费凭据未匹配使用记录", "wrong_user": "请求和Key用户归属不一致", "affiliate_review": "有返利资金操作，需核实资金方向", "payment_review": "有支付/退款操作，需与入账记录核对"}

func (d *Database) Report(ctx context.Context, c Config, a, b int64, query, state string, page, size int) (Report, error) {
	source := c.SourceID()
	status, e := d.State(source)
	out := Report{Start: a, End: b, Status: status, Rows: []ReportRow{}, Notes: []string{}}
	if e != nil {
		return out, e
	}
	tx, e := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	people := map[int64]*ReportRow{}
	get := func(id int64) *ReportRow {
		if people[id] == nil {
			people[id] = &ReportRow{ID: id, Email: fmt.Sprintf("用户 #%d", id), State: "unknown", Reasons: []string{}, Cost: "0.0000000000", Credits: "0.0000000000", Balance: "—"}
		}
		return people[id]
	}
	rows, e := tx.QueryContext(ctx, "SELECT id,payload FROM audit_records WHERE source=? AND entity='user'", source)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id int64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return out, e
		}
		p, x := unpack(raw)
		if x != nil {
			rows.Close()
			return out, x
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(p, &m)
		r := get(id)
		r.Email = str(m, "email")
		r.Balance = units(m["balance"]).String()
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	// Day-aligned periods use persistent rollups; exact custom ranges use retained
	// request metadata. The UI exposes missing history instead of estimating it.
	aligned := a == 0 || (a == dayStart(a) && b == dayStart(b))
	sqlText := "SELECT user_id,sum(requests),sum(cost),sum(credit) FROM audit_daily WHERE source=? AND day>=? AND day<? GROUP BY user_id"
	if !aligned {
		sqlText = "SELECT user_id,sum(CASE WHEN entity='usage' THEN 1 ELSE 0 END),sum(cost),sum(credit) FROM audit_records WHERE source=? AND at>=? AND at<? AND entity IN('usage','funding','affiliate') GROUP BY user_id"
	}
	rows, e = tx.QueryContext(ctx, sqlText, source, a, b)
	if e != nil {
		return out, e
	}
	var totalCost, totalCredits Money
	for rows.Next() {
		var id, n int64
		var cost, credit Money
		if e = rows.Scan(&id, &n, &cost, &credit); e != nil {
			rows.Close()
			return out, e
		}
		r := get(id)
		r.Requests = n
		r.Cost = cost.String()
		r.Credits = credit.String()
		out.Requests += n
		totalCost += cost
		totalCredits += credit
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	out.Cost = totalCost.String()
	out.Credits = totalCredits.String()
	oldest := dayStart(time.Now().Unix()) - int64(c.DetailDays-1)*86400
	checksComplete := status.Error == "" && status.Through > 0 && (a == 0 || a >= status.Start) && (a == 0 || a >= oldest) && status.Through >= min(b, time.Now().Unix()-int64(c.SettlementSeconds))-int64(c.IntervalSeconds+c.RoundSeconds)
	if status.Coverage != "complete" || status.Error != "" {
		out.Notes = append(out.Notes, "历史数据仍在补录，未匹配记录暂不能判为漏扣。")
	}
	if a != 0 && a < status.Start {
		out.Notes = append(out.Notes, "所选范围早于同步起点，仅展示已同步部分。")
	}
	if a == 0 || a < oldest {
		out.Notes = append(out.Notes, "部分明细超出保留范围；汇总仍保留，逐笔核对覆盖不完整。")
	}
	out.Notes = append(out.Notes, "余额对账使用独立快照；持续消费、最近结算或缺少快照时标待核实。快照区间与所选时间不同会单独标注。", "零充值不等于无资金，套餐、赠送、退款、返利和管理员调整需按来源区分。")
	// Aggregate flags locally; only the user's detail drawer decompresses rows.
	rows, e = tx.QueryContext(ctx, "SELECT user_id,flags,count(*) FROM audit_records WHERE source=? AND at>=? AND at<? AND entity<>'user' AND flags<>'' GROUP BY user_id,flags", source, a, b)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id, n int64
		var flags string
		_ = rows.Scan(&id, &flags, &n)
		r := get(id)
		for _, f := range strings.Split(flags, ",") {
			if f == "" {
				continue
			}
			if (f == "affiliate_review" || f == "payment_review") && !c.CheckFunding {
				continue
			}
			if f != "affiliate_review" && f != "payment_review" && !c.CheckCharges {
				continue
			}
			r.Issues += n
			r.Reasons = append(r.Reasons, fmt.Sprintf("%s：%d条", labels[f], n))
		}
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	if c.CheckBilling && checksComplete {
		for _, pair := range [][3]string{{"usage", "billing", "missing_billing"}, {"billing", "usage", "missing_usage"}} {
			q := `SELECT coalesce(nullif(r.user_id,0),k.user_id,0),count(*),coalesce(v.found,-1) FROM audit_records r LEFT JOIN audit_verifications v ON v.source=r.source AND v.entity=r.entity AND v.key_id=r.key_id AND v.request_id=r.request_id LEFT JOIN audit_records k ON k.source=r.source AND k.entity='key' AND k.id=r.key_id WHERE r.source=? AND r.entity=? AND r.at>=? AND r.at<? AND coalesce(v.found,0)=0 AND r.request_id<>'' AND NOT EXISTS(SELECT 1 FROM audit_records o WHERE o.source=r.source AND o.entity=? AND o.key_id=r.key_id AND o.request_id=r.request_id) GROUP BY 1,3`
			rows, e = tx.QueryContext(ctx, q, source, pair[0], max(max(a, oldest)+int64(c.OverlapMinutes)*60, status.Start+int64(c.OverlapMinutes)*60), min(b, status.Through-int64(c.SettlementSeconds)), pair[1])
			if e != nil {
				return out, e
			}
			for rows.Next() {
				var id, n, verified int64
				if e = rows.Scan(&id, &n, &verified); e != nil {
					rows.Close()
					return out, e
				}
				r := get(id)
				r.Issues += n
				note := "源端查询仍缺失"
				if verified < 0 {
					note = "等待源端定点核实/排除归档"
				}
				r.Reasons = append(r.Reasons, fmt.Sprintf("%s：%d条（%s）", labels[pair[2]], n, note))
			}
			if e = rows.Err(); e != nil {
				rows.Close()
				return out, e
			}
			rows.Close()
		}
	}
	if c.CheckBilling {
		rows, e = tx.QueryContext(ctx, `SELECT r.user_id,count(*) FROM audit_records r JOIN audit_records k ON k.source=r.source AND k.entity='key' AND k.id=r.key_id WHERE r.source=? AND r.entity='usage' AND r.at>=? AND r.at<? AND r.user_id<>k.user_id GROUP BY r.user_id`, source, a, b)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var id, n int64
			if e = rows.Scan(&id, &n); e != nil {
				rows.Close()
				return out, e
			}
			r := get(id)
			r.Issues += n
			r.Reasons = append(r.Reasons, fmt.Sprintf("请求与Key用户归属不一致：%d条", n))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
	}
	if c.CheckQuota && checksComplete {
		// A snapshot documents a quota already exhausted, followed by settled usage.
		// The next changed snapshot bounds the observation (admin resets are allowed).
		rows, e = tx.QueryContext(ctx, `WITH limits AS (
    SELECT id,at,payload,lead(at,1,?) OVER(PARTITION BY id ORDER BY at) AS until_at FROM audit_object_snapshots WHERE source=? AND entity='key'
  ) SELECT json_extract(k.payload,'$.user_id'),count(DISTINCT r.id) FROM limits k JOIN audit_records r ON r.source=? AND r.entity='usage' AND r.key_id=k.id AND r.at>=k.at+? AND r.at<k.until_at AND r.at>=? AND r.at<? WHERE cast(json_extract(k.payload,'$.quota') AS REAL)>0 AND cast(json_extract(k.payload,'$.quota_used') AS REAL)>=cast(json_extract(k.payload,'$.quota') AS REAL) GROUP BY 1`, status.Through, source, source, c.SettlementSeconds, a, min(b, status.Through))
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var id, n int64
			if e = rows.Scan(&id, &n); e != nil {
				rows.Close()
				return out, e
			}
			r := get(id)
			r.Issues += n
			r.Reasons = append(r.Reasons, fmt.Sprintf("Key额度耗尽快照后仍有%d条用量，需排除在途结算/人工调整", n))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
	}
	tolerance, _ := ParseMoney(c.DifferenceUSD)
	var totalDiff Money
	for _, r := range people {
		if c.CheckBalance {
			type snap struct {
				at                            int64
				balance, frozen, cost, credit Money
			}
			var first, last snap
			q := `SELECT at,balance,frozen,cost,credit FROM audit_snapshots WHERE source=? AND user_id=? AND complete=1 AND at>=? AND at<? ORDER BY at %s LIMIT 1`
			x := tx.QueryRowContext(ctx, fmt.Sprintf(q, "ASC"), source, r.ID, a, b).Scan(&first.at, &first.balance, &first.frozen, &first.cost, &first.credit)
			y := tx.QueryRowContext(ctx, fmt.Sprintf(q, "DESC"), source, r.ID, a, b).Scan(&last.at, &last.balance, &last.frozen, &last.cost, &last.credit)
			if x == nil && y == nil && last.at-first.at >= int64(c.PersistenceSeconds) && checksComplete {
				diff := last.balance + last.frozen - first.balance - first.frozen + (last.cost - first.cost) - (last.credit - first.credit)
				open, close, delta := first.balance.String(), last.balance.String(), diff.String()
				r.Opening = &open
				r.Closing = &close
				r.Difference = &delta
				r.From = first.at
				r.To = last.at
				r.BaselineCost = (last.cost - first.cost).String()
				r.BaselineCredits = (last.credit - first.credit).String()
				if diff > tolerance || diff < -tolerance {
					r.Issues++
					r.Reasons = append(r.Reasons, "独立余额快照与已同步收支不符，需核实延迟入账及资金来源")
					if diff < 0 {
						totalDiff -= diff
					} else {
						totalDiff += diff
					}
				} else {
					if a >= status.Start && a >= oldest {
						r.State = "consistent"
					}
				}
			} else {
				r.Reasons = append(r.Reasons, "独立稳定快照不足或仍有结算，尚不能证明余额已对平")
			}
		}
		if r.Issues > 0 {
			r.State = "review"
		}
		if !checksComplete && r.State == "consistent" {
			r.State = "unknown"
		}
		if r.State == "review" {
			out.Abnormal++
		} else if r.State == "consistent" {
			out.Consistent++
		} else {
			out.Unknown++
		}
		if query != "" && !strings.Contains(strings.ToLower(r.Email+fmt.Sprint(r.ID)), strings.ToLower(query)) {
			continue
		}
		if state != "" && r.State != state {
			continue
		}
		out.Rows = append(out.Rows, *r)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		rank := map[string]int{"review": 0, "unknown": 1, "consistent": 2}
		if rank[a.State] != rank[b.State] {
			return rank[a.State] < rank[b.State]
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.ID < b.ID
	})
	out.Total = len(out.Rows)
	out.Difference = totalDiff.String()
	if size == -1 {
		size = len(out.Rows) + 1
	} else if size < 1 || size > 200 {
		size = 20
	}
	if page < 1 {
		page = 1
	}
	lo := min((page-1)*size, len(out.Rows))
	hi := min(lo+size, len(out.Rows))
	out.Rows = out.Rows[lo:hi]
	return out, nil
}
func (d *Database) Detail(ctx context.Context, c Config, user, a, b, before int64, entity string, positive ...bool) ([]map[string]any, error) {
	switch entity {
	case "usage", "funding", "billing", "affiliate", "payment", "audit", "key", "subscription", "user":
	default:
		entity = "usage"
	}
	q := `SELECT id,payload FROM audit_records WHERE source=? AND entity=? AND (?=0 OR user_id=?) AND at>=? AND at<? AND (?=0 OR id<?) ORDER BY id DESC LIMIT 100`
	if entity == "user" && len(positive) > 0 && positive[0] {
		q = strings.Replace(q, "ORDER BY id DESC", "AND flags='active_positive' ORDER BY id DESC", 1)
	}
	rows, e := d.db.QueryContext(ctx, q, c.SourceID(), entity, user, user, a, b, before, before)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		p, x := unpack(raw)
		if x != nil {
			return nil, x
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(string(p)))
		dec.UseNumber()
		if e = dec.Decode(&m); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
