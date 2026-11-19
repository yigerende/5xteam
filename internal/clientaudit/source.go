package clientaudit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type Postgres struct{}

func connect(ctx context.Context, c Config, s Secrets) (*pgx.Conn, pgx.Tx, error) {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort)), Path: "/" + c.DBName, User: url.UserPassword(c.DBUser, s.DBPassword)}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	pc, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, nil, errors.New("数据库连接参数无效")
	}
	pc.ConnectTimeout = time.Duration(c.QuerySeconds) * time.Second
	pc.RuntimeParams["application_name"] = "space-client-audit-readonly"
	pc.RuntimeParams["default_transaction_read_only"] = "on"
	pc.RuntimeParams["statement_timeout"] = strconv.Itoa(c.QuerySeconds * 1000)
	pc.RuntimeParams["lock_timeout"] = "1000"
	pc.RuntimeParams["idle_in_transaction_session_timeout"] = "20000"
	pc.RuntimeParams["timezone"] = "Asia/Shanghai"
	conn, err := pgx.ConnectConfig(ctx, pc)
	if err != nil {
		return nil, nil, errors.New("数据库连接失败，请检查地址、TLS、账号及网络")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		conn.Close(context.Background())
		return nil, nil, errors.New("无法建立只读数据库事务")
	}
	if _, err = tx.Exec(ctx, "SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL jit=off; SET LOCAL work_mem='16MB'"); err != nil {
		tx.Rollback(ctx)
		conn.Close(ctx)
		return nil, nil, err
	}
	return conn, tx, nil
}
func closeSource(conn *pgx.Conn, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
	_ = conn.Close(ctx)
}
func sourceError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("只读查询超时；已保留进度，请调小分段或稍后重试")
	}
	return errors.New("只读查询失败：字段、读取权限或查询超时，请测试连接；原数据未改动")
}
func queryJSON(ctx context.Context, tx pgx.Tx, sql string, out any, args ...any) error {
	var raw []byte
	if err := tx.QueryRow(ctx, sql, args...).Scan(&raw); err != nil {
		return sourceError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}
func (Postgres) Test(ctx context.Context, c Config, s Secrets) (map[string]any, error) {
	conn, tx, e := connect(ctx, c, s)
	if e != nil {
		return nil, e
	}
	defer closeSource(conn, tx)
	var ro string
	var super bool
	var v string
	if e = tx.QueryRow(ctx, "SELECT current_setting('transaction_read_only'), r.rolsuper, version() FROM pg_roles r WHERE rolname=current_user").Scan(&ro, &super, &v); e != nil {
		return nil, sourceError(e)
	}
	if ro != "on" {
		return nil, errors.New("拒绝使用非只读事务")
	}
	var cols int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND ((table_name='usage_logs' AND column_name IN('request_id','api_key_id','actual_cost','billing_type','rate_multiplier')) OR (table_name='usage_billing_dedup' AND column_name IN('request_id','api_key_id')) OR (table_name='users' AND column_name IN('balance','frozen_balance')))").Scan(&cols); e != nil {
		return nil, sourceError(e)
	}
	if cols != 9 {
		return nil, errors.New("客户端数据库缺少必要的计费字段，当前版本不能完整核对")
	}
	for _, sql := range schemaQueries() {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return nil, sourceError(err)
		}
		rows.Close()
	}
	return map[string]any{"read_only": true, "schema_supported": true, "role_is_superuser": super, "message": "只读连接和计费字段检查通过；未发送模型请求", "warning": map[bool]string{true: "当前数据库账号权限较大，建议使用已有的专用只读账号。Space仍强制只读事务。", false: ""}[super]}, nil
}
