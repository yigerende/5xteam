package clientaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Explicit projections exclude secrets, request bodies, IPs and user agents.
// Never SELECT row_to_json(*) from account/key/user tables.
var projections = map[string]string{
	"usage":        `id,user_id,api_key_id,account_id,request_id,model,created_at,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,input_cost,output_cost,cache_creation_cost,cache_read_cost,total_cost,actual_cost,rate_multiplier,billing_type,subscription_id,group_id,duration_ms,first_token_ms,service_tier,billing_mode,inbound_endpoint,upstream_endpoint,image_count,video_count`,
	"billing":      `id,api_key_id,request_id,created_at`,
	"funding":      `id,used_by AS user_id,type,value,status,created_at,used_at`,
	"affiliate":    `id,user_id,action,amount,source_user_id,source_order_id,balance_after,aff_quota_after,created_at`,
	"payment":      `id,user_id,amount,pay_amount,bonus_amount,order_type,status,refund_amount,refund_at,paid_at,completed_at,created_at,updated_at`,
	"user":         `id,email,role,status,balance,frozen_balance,total_recharged,created_at,updated_at,deleted_at`,
	"key":          `id,user_id,name,group_id,status,quota,quota_used,rate_limit_5h,rate_limit_1d,rate_limit_7d,usage_5h,usage_1d,usage_7d,window_5h_start,window_1d_start,window_7d_start,created_at,updated_at,deleted_at`,
	"subscription": `id,user_id,group_id,starts_at,expires_at,status,daily_window_start,weekly_window_start,monthly_window_start,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,created_at,updated_at,deleted_at`,
	"audit":        `id,created_at,actor_user_id,actor_role,action,method,path,status_code`,
}
var tables = map[string]string{"usage": "usage_logs", "billing": "usage_billing_dedup", "funding": "redeem_codes", "affiliate": "user_affiliate_ledger", "payment": "payment_orders", "user": "users", "key": "api_keys", "subscription": "user_subscriptions", "audit": "audit_logs"}

type Cursor struct {
	At      string `json:"at"`
	Until   int64  `json:"until"`
	ID      int64  `json:"id"`
	Through int64  `json:"through"`
}
type Batch struct {
	Rows     []json.RawMessage
	Next     Cursor
	Complete bool
	At       int64
}
type IncrementalSource interface {
	Read(context.Context, Config, Secrets, string, Cursor, time.Time) (Batch, error)
	Current(context.Context, Config, Secrets) (map[string][]json.RawMessage, int64, error)
	Test(context.Context, Config, Secrets) (map[string]any, error)
}

func (Postgres) Read(ctx context.Context, c Config, s Secrets, entity string, cur Cursor, end time.Time) (Batch, error) {
	out := Batch{Rows: []json.RawMessage{}, Next: cur}
	from, parseErr := time.Parse(time.RFC3339Nano, cur.At)
	if parseErr != nil {
		return out, errors.New("同步游标时间无效")
	}
	table, ok := tables[entity]
	if !ok || entity == "user" || entity == "key" || entity == "subscription" {
		return out, errors.New("不支持的数据类别")
	}
	clock := "created_at"
	if entity == "funding" {
		clock = "used_at"
	}
	if entity == "payment" {
		clock = "updated_at"
	}
	conn, tx, e := connect(ctx, c, s)
	if e != nil {
		return out, e
	}
	defer closeSource(conn, tx)
	// Monetary NUMERIC values are preserved as JSON numbers and decoded using
	// decimal strings locally. The row cap bounds memory and transaction time.
	sql := fmt.Sprintf(`SELECT coalesce(json_agg(t),'[]') FROM(SELECT %s FROM %s WHERE %s >= $1::timestamptz AND %s < $2 AND (%s > $1::timestamptz OR id > $3) ORDER BY %s,id LIMIT $4)t`, projections[entity], table, clock, clock, clock, clock)
	if e = queryJSON(ctx, tx, sql, &out.Rows, from, end, cur.ID, c.BatchSize); e != nil {
		return out, e
	}
	if len(out.Rows) > 0 {
		var r map[string]json.RawMessage
		if e = json.Unmarshal(out.Rows[len(out.Rows)-1], &r); e != nil {
			return out, e
		}
		_ = json.Unmarshal(r["id"], &out.Next.ID)
		_ = json.Unmarshal(r[clock], &out.Next.At)
	}
	out.Complete = len(out.Rows) < c.BatchSize
	out.At = end.Unix()
	if out.Complete {
		out.Next = Cursor{At: end.Format(time.RFC3339Nano), Through: end.Unix()}
	}
	return out, nil
}
func (Postgres) Current(ctx context.Context, c Config, s Secrets) (map[string][]json.RawMessage, int64, error) {
	out := map[string][]json.RawMessage{}
	conn, tx, e := connect(ctx, c, s)
	if e != nil {
		return out, 0, e
	}
	defer closeSource(conn, tx)
	var now time.Time
	if e = tx.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&now); e != nil {
		return out, 0, sourceError(e)
	}
	// These are small configuration tables; one snapshot avoids updated_at races.
	for _, entity := range []string{"user", "key", "subscription"} {
		var rows []json.RawMessage
		if e = queryJSON(ctx, tx, fmt.Sprintf("SELECT coalesce(json_agg(t),'[]') FROM(SELECT %s FROM %s ORDER BY id LIMIT 100001)t", projections[entity], tables[entity]), &rows); e != nil {
			return out, 0, e
		}
		if len(rows) > 100000 {
			return out, 0, errors.New("账号或Key超过单次快照上限，请使用分区同步版本")
		}
		out[entity] = rows
	}
	return out, now.Unix(), nil
}
