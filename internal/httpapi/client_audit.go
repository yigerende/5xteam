package httpapi

import (
	"chatgpt-space-merge/internal/clientaudit"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) startClientAudit() error {
	db, e := clientaudit.OpenDatabase(s.store.ClientAuditPath())
	if e != nil {
		return e
	}
	s.clientAudit = clientaudit.NewEngine(db, clientaudit.Postgres{}, s.store.ClientAuditSettings)
	s.clientAudit.Start()
	return nil
}
func (s *Server) registerClientAudit(mux *http.ServeMux) {
	register := func(path string, handler http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if s.clientAudit == nil {
				writeAPI(w, 503, nil, "对账数据库暂不可用，Space其他功能不受影响")
				return
			}
			handler(w, r)
		})
	}
	register("GET /api/client-audit/balances", s.clientAuditBalances)
	register("GET /api/client-audit/settings", s.clientAuditSettings)
	register("PUT /api/client-audit/settings", s.clientAuditSaveSettings)
	register("POST /api/client-audit/test", s.clientAuditTest)
	register("POST /api/client-audit/backfill", s.clientAuditBackfill)
	register("POST /api/client-audit/stop", s.clientAuditStop)
	register("GET /api/client-audit/progress", s.clientAuditProgress)
	register("GET /api/client-audit/status", s.clientAuditStatus)
	register("POST /api/client-audit/sync", s.clientAuditSync)
	register("GET /api/client-audit/report", s.clientAuditReport)
	register("GET /api/client-audit/detail", s.clientAuditDetail)
	register("GET /api/client-audit/export", s.clientAuditExport)
}
func (s *Server) clientAuditSettings(w http.ResponseWriter, r *http.Request) {
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取对账配置失败")
		return
	}
	writeAPI(w, 200, c, "")
}

type auditSettingsInput struct {
	clientaudit.Config
	DBPassword string `json:"db_password"`
}

func decodeAuditInput(w http.ResponseWriter, r *http.Request) (auditSettingsInput, error) {
	in := auditSettingsInput{Config: clientaudit.DefaultConfig()}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	e := json.NewDecoder(r.Body).Decode(&in)
	return in, e
}
func (s *Server) clientAuditSaveSettings(w http.ResponseWriter, r *http.Request) {
	in, e := decodeAuditInput(w, r)
	if e != nil {
		writeAPI(w, 400, nil, "配置格式无效")
		return
	}
	c, e := s.store.SaveClientAuditSettings(in.Config, clientaudit.Secrets{DBPassword: in.DBPassword})
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	writeAPI(w, 200, c, "")
}
func (s *Server) clientAuditTest(w http.ResponseWriter, r *http.Request) {
	in, e := decodeAuditInput(w, r)
	if e != nil {
		writeAPI(w, 400, nil, "连接配置格式无效")
		return
	}
	if e = in.Config.Validate(); e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	_, secrets, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取密钥失败")
		return
	}
	if in.DBPassword != "" {
		secrets.DBPassword = in.DBPassword
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.QuerySeconds+5)*time.Second)
	defer cancel()
	result, e := (clientaudit.Postgres{}).Test(ctx, in.Config, secrets)
	if e != nil {
		writeAPI(w, 502, nil, e.Error())
		return
	}
	writeAPI(w, 200, result, "")
}
func (s *Server) clientAuditStatus(w http.ResponseWriter, r *http.Request) {
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	v, e := s.clientAudit.Status(c)
	if e != nil {
		writeAPI(w, 500, nil, "读取同步状态失败")
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) clientAuditSync(w http.ResponseWriter, r *http.Request) {
	c, _, e := s.store.ClientAuditSettings()
	if e != nil || c.DBHost == "" {
		writeAPI(w, 400, nil, "请先保存数据库只读连接")
		return
	}
	accepted := s.clientAudit.Trigger()
	writeAPI(w, 202, map[string]any{"accepted": accepted, "message": map[bool]string{true: "已加入后台同步", false: "已有同步正在执行"}[accepted]}, "")
}
func auditPeriod(r *http.Request) (int64, int64, error) {
	q := r.URL.Query()
	return clientaudit.Period(q.Get("mode"), q.Get("date"), q.Get("start"), q.Get("end"), time.Now())
}
func (s *Server) clientAuditReport(w http.ResponseWriter, r *http.Request) {
	a, b, e := auditPeriod(r)
	if e != nil {
		writeAPI(w, 400, nil, "时间范围无效")
		return
	}
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, e := s.clientAudit.DB.Report(ctx, c, a, b, q.Get("query"), q.Get("state"), page, size)
	if e != nil {
		writeAPI(w, 500, nil, "读取本地对账结果失败或超时")
		return
	}
	writeAPI(w, 200, result, "")
}
func (s *Server) clientAuditDetail(w http.ResponseWriter, r *http.Request) {
	a, b, e := auditPeriod(r)
	if e != nil {
		writeAPI(w, 400, nil, "时间范围无效")
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, e := s.clientAudit.DB.Detail(ctx, c, id, a, b, before, r.URL.Query().Get("entity"), r.URL.Query().Get("filter") == "positive")
	if e != nil {
		writeAPI(w, 500, nil, "读取本地明细失败")
		return
	}
	writeAPI(w, 200, map[string]any{"items": rows, "page_size": 100}, "")
}
func (s *Server) clientAuditExport(w http.ResponseWriter, r *http.Request) {
	a, b, e := auditPeriod(r)
	if e != nil {
		writeAPI(w, 400, nil, "时间范围无效")
		return
	}
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// A bounded export reads local data only. CSV cells cannot execute spreadsheet formulas.
	safe := func(v string) string {
		if len(v) > 0 && (v[0] == '=' || v[0] == '+' || v[0] == '-' || v[0] == '@') {
			return "'" + v
		}
		return v
	}
	result, e := s.clientAudit.DB.Report(ctx, c, a, b, r.URL.Query().Get("query"), r.URL.Query().Get("state"), 1, -1)
	if e != nil {
		writeAPI(w, 500, nil, "生成导出失败")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=client-reconciliation.csv")
	_, _ = w.Write([]byte{0xef, 0xbb, 0xbf})
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"用户ID", "邮箱", "请求数", "本期消费", "入账", "当前余额", "状态", "原因"})
	for _, row := range result.Rows {
		_ = cw.Write([]string{fmt.Sprint(row.ID), safe(row.Email), fmt.Sprint(row.Requests), row.Cost, row.Credits, row.Balance, row.State, safe(fmt.Sprint(row.Reasons))})
	}
}

func (s *Server) clientAuditProgress(w http.ResponseWriter, r *http.Request) {
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	v, e := s.clientAudit.DB.Progress(ctx, c)
	if e != nil {
		writeAPI(w, 500, nil, "读取进度失败")
		return
	}
	writeAPI(w, 200, v, "")
}
func (s *Server) clientAuditStop(w http.ResponseWriter, r *http.Request) {
	s.clientAudit.Stop()
	writeAPI(w, 200, map[string]string{"message": "已停止本轮，断点保留；关闭自动同步可暂停后续轮次"}, "")
}
func (s *Server) clientAuditBackfill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date string `json:"date"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeAPI(w, 400, nil, "日期无效")
		return
	}
	t, e := time.ParseInLocation("2006-01-02", in.Date, clientaudit.Beijing)
	if e != nil {
		writeAPI(w, 400, nil, "日期无效")
		return
	}
	c, _, e := s.store.ClientAuditSettings()
	if e != nil || c.DBHost == "" {
		writeAPI(w, 400, nil, "请先保存只读连接")
		return
	}
	if e = s.clientAudit.Backfill(c, t.Unix()); e != nil {
		writeAPI(w, 409, nil, e.Error())
		return
	}
	writeAPI(w, 202, map[string]string{"message": "已安排历史补录，已有明细按ID去重；独立快照重新建立"}, "")
}

func (s *Server) clientAuditBalances(w http.ResponseWriter, r *http.Request) {
	c, _, e := s.store.ClientAuditSettings()
	if e != nil {
		writeAPI(w, 500, nil, "读取配置失败")
		return
	}
	at, _ := strconv.ParseInt(r.URL.Query().Get("at"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	v, e := s.clientAudit.DB.Balances(ctx, c, at, r.URL.Query().Get("query"))
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	if r.URL.Query().Get("export") != "csv" {
		writeAPI(w, 200, v, "")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=user-balance-snapshot.csv")
	w.Write([]byte{0xef, 0xbb, 0xbf})
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"快照时间（Unix秒）", "用户ID", "邮箱", "余额USD", "冻结USD", "累计充值字段USD"})
	for _, x := range v["items"].([]map[string]any) {
		email := x["email"].(string)
		if len(email) > 0 && (email[0] == '=' || email[0] == '+' || email[0] == '-' || email[0] == '@') {
			email = "'" + email
		}
		cw.Write([]string{fmt.Sprint(v["at"]), fmt.Sprint(x["id"]), email, fmt.Sprint(x["balance"]), fmt.Sprint(x["frozen"]), fmt.Sprint(x["recharged"])})
	}
}
