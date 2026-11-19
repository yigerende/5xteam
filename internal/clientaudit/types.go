// Package clientaudit observes a downstream billing database. It never writes to
// the source: every query runs in a bounded PostgreSQL READ ONLY transaction.
package clientaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	BackupIntervalSeconds int    `json:"backup_interval_seconds"`
	BackupRetentionDays   int    `json:"backup_retention_days"`
	Enabled               bool   `json:"enabled"`
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	DBHost                string `json:"db_host"`
	DBPort                int    `json:"db_port"`
	DBName                string `json:"db_name"`
	DBUser                string `json:"db_user"`
	DBPasswordPresent     bool   `json:"db_password_present"`
	SSLMode               string `json:"ssl_mode"`
	IntervalSeconds       int    `json:"interval_seconds"`
	HistoryDays           int    `json:"history_days"`
	SegmentMinutes        int    `json:"segment_minutes"`
	RoundSeconds          int    `json:"round_seconds"`
	QuerySeconds          int    `json:"query_seconds"`
	PauseMS               int    `json:"pause_ms"`
	OverlapMinutes        int    `json:"overlap_minutes"`
	SettlementSeconds     int    `json:"settlement_seconds"`
	RetrySeconds          int    `json:"retry_seconds"`
	RetentionDays         int    `json:"retention_days"`
	RefreshSeconds        int    `json:"refresh_seconds"`
	DifferenceUSD         string `json:"difference_usd"`
	PersistenceSeconds    int    `json:"persistence_seconds"`
	CheckBilling          bool   `json:"check_billing"`
	CheckCharges          bool   `json:"check_charges"`
	CheckFunding          bool   `json:"check_funding"`
	CheckBalance          bool   `json:"check_balance"`
	CheckQuota            bool   `json:"check_quota"`
	BatchSize             int    `json:"batch_size"`
	MaxBatches            int    `json:"max_batches"`
	DetailDays            int    `json:"detail_days"`
}
type Secrets struct {
	DBPassword string `json:"db_password"`
}

func DefaultConfig() Config {
	return Config{BackupIntervalSeconds: 3600, BackupRetentionDays: 180, Name: "Sub2API 客户端", DBPort: 5432, SSLMode: "require", IntervalSeconds: 60, HistoryDays: 1, SegmentMinutes: 10, RoundSeconds: 45, QuerySeconds: 12, PauseMS: 100, OverlapMinutes: 10, SettlementSeconds: 180, RetrySeconds: 60, RetentionDays: 180, RefreshSeconds: 30, DifferenceUSD: "0.000001", PersistenceSeconds: 300, CheckBilling: true, CheckCharges: true, CheckFunding: true, CheckBalance: true, CheckQuota: true, BatchSize: 2000, MaxBatches: 60, DetailDays: 30}
}
func (c Config) Validate() error {
	if c.URL != "" {
		u, e := url.Parse(c.URL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("请输入有效的客户端 http/https 地址，不含账号、查询参数或片段")
		}
	}
	if c.Enabled && (strings.TrimSpace(c.DBHost) == "" || c.DBName == "" || c.DBUser == "") {
		return errors.New("自动对账需要配置数据库只读连接")
	}
	if c.SSLMode != "require" && c.SSLMode != "verify-full" && c.SSLMode != "disable" {
		return errors.New("数据库 TLS 模式不支持")
	}
	checks := []struct {
		name        string
		v, min, max int
	}{{"余额备份间隔", c.BackupIntervalSeconds, 60, 86400}, {"余额备份保留天数", c.BackupRetentionDays, 1, 3650}, {"端口", c.DBPort, 1, 65535}, {"同步间隔", c.IntervalSeconds, 10, 86400}, {"历史天数", c.HistoryDays, 1, 3650}, {"分段分钟", c.SegmentMinutes, 1, 60}, {"每轮时间", c.RoundSeconds, 5, 300}, {"查询超时", c.QuerySeconds, 1, 60}, {"批次间隔", c.PauseMS, 0, 60000}, {"重扫分钟", c.OverlapMinutes, 1, 1440}, {"结算等待", c.SettlementSeconds, 0, 86400}, {"失败重试间隔", c.RetrySeconds, 10, 86400}, {"保留天数", c.RetentionDays, 1, 3650}, {"页面刷新", c.RefreshSeconds, 5, 3600}, {"持续差额时间", c.PersistenceSeconds, 0, 86400}}
	for _, x := range checks {
		if x.v < x.min || x.v > x.max {
			return fmt.Errorf("%s须为%d—%d", x.name, x.min, x.max)
		}
	}
	if 60%c.SegmentMinutes != 0 {
		return errors.New("分段分钟请选1、2、3、4、5、6、10、12、15、20、30、60")
	}
	if c.RetentionDays < c.HistoryDays {
		return errors.New("保留天数不能小于首次历史天数")
	}
	if c.BatchSize < 100 || c.BatchSize > 10000 || c.MaxBatches < 1 || c.MaxBatches > 1000 {
		return errors.New("单批行数100—10000，每轮批数1—1000")
	}
	if c.DetailDays < c.HistoryDays || c.DetailDays > c.RetentionDays {
		return errors.New("明细保留天数须介于首次历史天数和汇总保留天数之间")
	}
	if c.OverlapMinutes < c.SegmentMinutes {
		return errors.New("重扫范围不能小于单个分段")
	}
	if m, e := ParseMoney(c.DifferenceUSD); e != nil || m < 0 {
		return errors.New("金额容差须为非负数，最多10位小数")
	}
	return nil
}
func (c Config) SourceID() string {
	s := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(c.DBHost)) + fmt.Sprint(":", c.DBPort) + "/" + c.DBName))
	return hex.EncodeToString(s[:12])
}

// All accounting math uses source-quantized 1e-10 USD integer units, never floats.
type Money int64

func ParseMoney(s string) (Money, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	p := strings.Split(s, ".")
	if len(p) > 2 || p[0] == "" {
		return 0, errors.New("invalid amount")
	}
	frac := ""
	if len(p) == 2 {
		frac = p[1]
	}
	if len(frac) > 10 {
		return 0, errors.New("amount precision")
	}
	all := p[0] + frac + strings.Repeat("0", 10-len(frac))
	var n int64
	for _, r := range all {
		if r < '0' || r > '9' || n > (1<<63-1-int64(r-'0'))/10 {
			return 0, errors.New("invalid amount")
		}
		n = n*10 + int64(r-'0')
	}
	if neg {
		n = -n
	}
	return Money(n), nil
}
func (m Money) String() string {
	n := int64(m)
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("%s%d.%010d", sign, n/10000000000, n%10000000000)
}

type Status struct {
	Source      string `json:"source"`
	Running     bool   `json:"running"`
	LastAttempt int64  `json:"last_attempt"`
	LastSuccess int64  `json:"last_success"`
	NextRun     int64  `json:"next_run"`
	Start       int64  `json:"start"`
	Through     int64  `json:"through"`
	Target      int64  `json:"target"`
	CycleIndex  int    `json:"cycle_index"`
	Records     int64  `json:"records"`
	Segments    int    `json:"segments"`
	Error       string `json:"error"`
	DBBytes     int64  `json:"db_bytes"`
	Coverage    string `json:"coverage"`
}

var Beijing = time.FixedZone("Asia/Shanghai", 8*3600)
