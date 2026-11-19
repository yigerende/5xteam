package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/financecalc"
	"chatgpt-space-merge/internal/store"
)

type ejhFinanceReportData struct {
	fields   map[string]json.RawMessage
	events   []map[string]any
	accounts []map[string]any
	revision string
}
type ejhFinanceReportJob struct {
	done chan struct{}
	key  string
	data *ejhFinanceReportData
	err  error
}
type ejhFinanceReports struct {
	mu    sync.Mutex
	cache map[string]*ejhFinanceReportData
	order []string
	job   *ejhFinanceReportJob
}

// One bounded calculation per application, shared by concurrent readers. A
// cancelled browser leaves immediately without cancelling other readers.
func (c *ejhFinanceReports) read(ctx context.Context, key string, calculate func(context.Context) (*ejhFinanceReportData, error)) (*ejhFinanceReportData, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if data := c.cache[key]; data != nil {
			c.mu.Unlock()
			return data, nil
		}
		job := c.job
		if job == nil {
			job = &ejhFinanceReportJob{key: key, done: make(chan struct{})}
			c.job = job
			go func() {
				work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				defer cancel()
				job.data, job.err = calculate(work)
				c.mu.Lock()
				if job.err == nil {
					if c.cache == nil {
						c.cache = map[string]*ejhFinanceReportData{}
					}
					c.cache[key] = job.data
					c.order = append(c.order, key)
					for len(c.order) > 8 {
						delete(c.cache, c.order[0])
						c.order = c.order[1:]
					}
				}
				c.job = nil
				close(job.done)
				c.mu.Unlock()
			}()
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-job.done:
		}
		if job.key == key {
			return job.data, job.err
		}
	}
}

func (s *Server) buildEJHFinanceReport(ctx context.Context, mode, date, revision string) (*ejhFinanceReportData, error) {
	reader, err := s.store.ProListReader(ctx)
	if err != nil {
		return nil, err
	}
	// Both financial sources share the same read-only WAL snapshot. Release the
	// database before starting calculation; no business mutex or issuer request.
	data, history, err := func() (map[string]any, map[string]any, error) {
		defer reader.Close()
		data, err := (&Server{store: reader}).readEJHDashboard(false)
		if err != nil {
			return nil, nil, err
		}
		days, err := store.EJHRecords[store.EJHConsumptionDay](reader, "finance_usage_day")
		if err != nil {
			return nil, nil, err
		}
		var recording ejhFinanceRecording
		if err = reader.EJHGet("finance_recording", "latest", &recording); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		return data, map[string]any{"consumption": data["consumption_history"], "usage_days": days, "recharges": data["recharge_history"], "external_transfers": data["external_transfer_history"], "recording": recording}, nil
	}()
	if err != nil {
		return nil, err
	}
	raw, err := financecalc.Calculate(ctx, map[string]any{"data": data, "history": history, "mode": mode, "date": date})
	if err != nil {
		return nil, err
	}
	report := &ejhFinanceReportData{revision: revision}
	if err = json.Unmarshal(raw, &report.fields); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(report.fields["events"], &report.events); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(report.fields["openedAccountRows"], &report.accounts); err != nil {
		return nil, err
	}
	delete(report.fields, "events")
	delete(report.fields, "openedAccountRows")
	for key, value := range map[string]any{"recording": history["recording"], "server_time": data["server_time"], "page_refresh_seconds": data["page_refresh_seconds"], "recharge_ledger": data["recharge_ledger"], "revision": revision} {
		report.fields[key], _ = json.Marshal(value)
	}
	return report, nil
}

func (s *Server) ejhFinanceReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode, date, section := q.Get("mode"), q.Get("date"), q.Get("section")
	if section == "" {
		section = "report"
	}
	if mode != "day" && mode != "week" && mode != "month" && mode != "all" {
		writeAPI(w, 400, nil, "统计周期无效")
		return
	}
	at, err := time.Parse("2006-01-02", date)
	if err != nil || at.Year() < 2000 || at.Year() > 2100 {
		writeAPI(w, 400, nil, "统计日期无效")
		return
	}
	if section != "report" && section != "events" && section != "accounts" {
		writeAPI(w, 400, nil, "统计明细无效")
		return
	}
	page, size := 1, 10
	if section != "report" {
		if q.Has("page") {
			page, err = strconv.Atoi(q.Get("page"))
			if err != nil || page < 1 || page > 1000000 {
				writeAPI(w, 400, nil, "页码无效")
				return
			}
		}
		if q.Has("page_size") {
			size, err = strconv.Atoi(q.Get("page_size"))
			if err != nil || size < 1 || size > 500 {
				writeAPI(w, 400, nil, "每页条数须为1～500")
				return
			}
		}
	}
	revision, err := s.store.EJHFinanceRevision(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取成本版本失败")
		return
	}
	prefix := mode + "/" + date + "/"
	// Detail pages remain tied to the exact summary the user is looking at.
	var report *ejhFinanceReportData
	if section != "report" && q.Get("revision") != "" {
		s.ejh.finance.mu.Lock()
		report = s.ejh.finance.cache[prefix+q.Get("revision")]
		s.ejh.finance.mu.Unlock()
		if report == nil && q.Get("revision") != revision {
			writeAPI(w, 409, nil, "统计数据已更新，请刷新统计后查看明细")
			return
		}
	}
	if report == nil {
		report, err = s.ejh.finance.read(r.Context(), prefix+revision, func(ctx context.Context) (*ejhFinanceReportData, error) {
			return s.buildEJHFinanceReport(ctx, mode, date, revision)
		})
	}
	if err != nil {
		writeAPI(w, 500, nil, "读取成本统计失败，请稍后重试")
		return
	}
	if section == "report" {
		writeProAccountList(w, r, report.fields)
		return
	}
	rows := report.events
	if section == "accounts" {
		rows = report.accounts
	}
	filtered := make([]map[string]any, 0, len(rows))
	search := strings.ToLower(strings.TrimSpace(q.Get("query")))
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, row := range rows {
		if section == "events" {
			kind := q.Get("kind")
			refund, _ := row["refundPrincipal"].(bool)
			if kind != "" && (kind == "refund_credit" && !refund || kind != "refund_credit" && (row["kind"] != kind || refund)) {
				continue
			}
			if day := q.Get("day"); day != "" {
				period, _ := row["month"].(string)
				if period != "" {
					if mode != "all" {
						period += "（日期未记录）"
					}
				} else if milliseconds, ok := row["at"].(float64); ok {
					period = time.UnixMilli(int64(milliseconds)).In(zone).Format("2006-01-02")
					if mode == "all" {
						period = period[:7]
					}
				} else {
					period = "未分期历史"
				}
				if period != day {
					continue
				}
			}
		}
		fields := []string{"email", "card", "reference", "note"}
		if section == "accounts" {
			fields = []string{"email", "plan", "last4"}
		}
		text := ""
		for _, field := range fields {
			if v, ok := row[field].(string); ok {
				text += " " + v
			}
		}
		if section == "accounts" {
			plan, _ := row["plan"].(string)
			text += " " + map[string]string{"pro5": "Pro 5x", "pro5x": "Pro 5x", "pro20": "Pro 20x", "pro20x": "Pro 20x", "pro50": "Pro 50x", "pro50x": "Pro 50x", "pro5_up": "Plus 升级 Pro 5x", "pro20_up": "Plus 升级 Pro 20x", "pro50_up": "Plus 升级 Pro 50x"}[plan]
		}
		if search != "" && !strings.Contains(strings.ToLower(text), search) {
			continue
		}
		filtered = append(filtered, row)
	}
	pages := (len(filtered) + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	end := start + size
	if end > len(filtered) {
		end = len(filtered)
	}
	writeProAccountList(w, r, map[string]any{"items": filtered[start:end], "total": len(filtered), "page": page, "page_size": size, "revision": report.revision})
}
