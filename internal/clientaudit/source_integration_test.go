package clientaudit

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed source_schema_test.sql
var testSchema string

func TestPostgresReadOnlyIntegration(t *testing.T) {
	dsn := os.Getenv("CLIENT_AUDIT_TEST_DB")
	if dsn == "" {
		t.Skip("isolated PostgreSQL fixture required")
	}
	pc, err := pgx.ParseConfig(dsn)
	if err != nil || pc.Database != "space_audit_test" {
		t.Fatal("refusing to create fixtures outside space_audit_test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err = admin.Exec(ctx, testSchema); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, sql := range []string{
		`INSERT INTO users(id,email,balance,frozen_balance,created_at,updated_at)VALUES(1,'fixture@example.invalid',10,0,$1,$1)`,
		`INSERT INTO api_keys(id,user_id,name,created_at,updated_at)VALUES(2,1,'fixture-key',$1,$1)`,
		`INSERT INTO usage_logs(id,user_id,api_key_id,request_id,created_at,actual_cost,total_cost,rate_multiplier,billing_type) VALUES(1,1,2,'fixture',$1,0.0000000001,0.0000000001,1,0)`,
		`INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,created_at)VALUES('fixture',2,$1)`,
		`INSERT INTO redeem_codes(id,used_by,type,value,status,created_at,used_at)VALUES(1,1,'balance',10,'used',$1,$1)`,
	} {
		if _, err = admin.Exec(ctx, sql, at); err != nil {
			t.Fatal(err)
		}
	}
	c := DefaultConfig()
	c.DBHost = pc.Host
	c.DBPort = int(pc.Port)
	c.DBUser = pc.User
	c.DBName = pc.Database
	c.SSLMode = "disable"
	sec := Secrets{DBPassword: pc.Password}
	src := Postgres{}
	if _, err = src.Test(ctx, c, sec); err != nil {
		t.Fatal(err)
	}
	b, err := src.Read(ctx, c, sec, "usage", Cursor{At: at.Add(-time.Minute).Format(time.RFC3339Nano)}, at.Add(time.Minute))
	if err != nil || len(b.Rows) != 1 || !b.Complete {
		t.Fatal(b, err)
	}
	if units(decodeMap(b.Rows[0])["actual_cost"]).String() != "0.0000000001" {
		t.Fatal("source numeric precision lost")
	}
	b, err = src.Read(ctx, c, sec, "funding", Cursor{At: at.Add(-time.Minute).Format(time.RFC3339Nano)}, at.Add(time.Minute))
	if err != nil || len(b.Rows) != 1 {
		t.Fatal(b, err)
	}
	m, _, err := src.Current(ctx, c, sec)
	if err != nil || len(m["user"]) != 1 || len(m["key"]) != 1 {
		t.Fatal(m, err)
	}
	match, err := src.Verify(ctx, c, sec, []Candidate{{Entity: "usage", Request: "fixture", Key: 2}})
	if err != nil || !match[0].Found {
		t.Fatal(match, err)
	}
	conn, tx, err := connect(ctx, c, sec)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSource(conn, tx)
	var ro string
	if err = tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&ro); err != nil || ro != "on" {
		t.Fatal(ro, err)
	}
	_, err = tx.Exec(ctx, "UPDATE users SET balance=999 WHERE id=1")
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "25006" {
		t.Fatal("write not rejected", err)
	}
	var bal string
	if err = admin.QueryRow(ctx, "SELECT balance::text FROM users WHERE id=1").Scan(&bal); err != nil || bal != "10" {
		t.Fatal("source changed", bal, err)
	}
}
