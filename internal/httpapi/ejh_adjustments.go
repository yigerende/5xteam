package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/financecalc"
	"chatgpt-space-merge/internal/store"
)

func (s *Server) readEJHAdjustmentPreview(ctx context.Context) (store.EJHAdjustmentEvidence, error) {
	var out store.EJHAdjustmentEvidence
	reader, err := s.store.ProListReader(ctx)
	if err != nil {
		return out, err
	}
	version, err := reader.EJHAccountingVersion(ctx)
	reader.Close()
	if err != nil {
		return out, err
	}
	result, err := s.ejh.finance.read(ctx, "adjustment/"+version+"/"+strconv.FormatInt(time.Now().Unix()/60, 10), func(work context.Context) (*ejhFinanceReportData, error) {
		reader, e := s.store.ProListReader(work)
		if e != nil {
			return nil, e
		}
		v, e := reader.EJHAccountingVersion(work)
		if e != nil {
			reader.Close()
			return nil, e
		}
		if v != version {
			reader.Close()
			return nil, store.ErrEJHAdjustmentChanged
		}
		data, e := (&Server{store: reader}).readEJHDashboard(false)
		reader.Close()
		if e != nil {
			return nil, e
		}
		raw, e := financecalc.Calculate(work, map[string]any{"purpose": "reconciliation", "data": data})
		if e != nil {
			return nil, e
		}
		var evidence store.EJHAdjustmentEvidence
		if e = json.Unmarshal(raw, &evidence); e != nil {
			return nil, e
		}
		evidence.Version = version
		evidence.CalculatedAt = time.Now().UTC()
		raw, e = json.Marshal(evidence)
		return &ejhFinanceReportData{fields: map[string]json.RawMessage{"adjustment": raw}}, e
	})
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(result.fields["adjustment"], &out)
	return out, err
}
func (s *Server) ejhAdjustmentPreview(w http.ResponseWriter, r *http.Request) {
	out, err := s.readEJHAdjustmentPreview(r.Context())
	if err != nil {
		ejhAdjustmentError(w, err)
		return
	}
	writeAPI(w, 200, out, "")
}
func ejhAdjustmentError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrEJHAdjustmentChanged) {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	writeAPI(w, 500, nil, "读取或保存平账失败，请重新查询记录后重试")
}
func (s *Server) ejhAdjustments(w http.ResponseWriter, r *http.Request) {
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		ejhAdjustmentError(w, err)
		return
	}
	defer reader.Close()
	if id := r.URL.Query().Get("request_id"); id != "" {
		out, e := reader.EJHAdjustmentByRequest(id)
		if errors.Is(e, sql.ErrNoRows) {
			writeAPI(w, 404, nil, "平账记录不存在")
			return
		}
		if e != nil {
			ejhAdjustmentError(w, e)
			return
		}
		writeAPI(w, 200, out, "")
		return
	}
	page, size := 1, 10
	for key, target := range map[string]*int{"page": &page, "page_size": &size} {
		if v := r.URL.Query().Get(key); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 1000000 {
				writeAPI(w, 400, nil, "分页参数无效")
				return
			}
			*target = n
		}
	}
	if size > 100 {
		writeAPI(w, 400, nil, "每页最多 100 条")
		return
	}
	summary, err := reader.EJHAdjustmentSummary()
	if err != nil {
		ejhAdjustmentError(w, err)
		return
	}
	items, err := reader.EJHAdjustments(r.Context(), page, size)
	if err != nil {
		ejhAdjustmentError(w, err)
		return
	}
	writeAPI(w, 200, map[string]any{"items": items, "summary": summary, "total": summary.Count, "page": page, "page_size": size}, "")
}
func (s *Server) saveEJHAdjustment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID     string `json:"request_id"`
		Note          string `json:"note"`
		ExpectedGap   string `json:"expected_gap"`
		ExpectedTotal string `json:"expected_total"`
		Amount        string `json:"amount"`
	}
	if decodeJSON(w, r, &in, 8<<10) != nil {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.RequestID == "" || in.Note == "" || in.ExpectedGap == "" || in.ExpectedTotal == "" {
		writeAPI(w, 400, nil, "请预览核对金额并填写平账原因")
		return
	}
	// The application has one authenticated administrator (see /api/auth/me).
	const actor = "admin"
	previous, err := s.store.EJHAdjustmentByRequest(in.RequestID)
	if err == nil {
		if previous.Actor != actor || previous.Note != in.Note || (in.Amount != "" && previous.Amount != in.Amount) || previous.GapBefore != in.ExpectedGap || previous.TotalBefore != in.ExpectedTotal {
			writeAPI(w, 409, nil, "请求编号已用于另一笔平账")
			return
		}
		writeAPI(w, 200, previous, "")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		ejhAdjustmentError(w, err)
		return
	}
	evidence, err := s.readEJHAdjustmentPreview(r.Context())
	if err != nil {
		ejhAdjustmentError(w, err)
		return
	}
	if evidence.GapBefore == "" || evidence.RawGap == "" {
		writeAPI(w, 422, nil, "核对金额未完整，请先补齐数据")
		return
	}
	if evidence.GapBefore != in.ExpectedGap || evidence.TotalBefore != in.ExpectedTotal {
		ejhAdjustmentError(w, store.ErrEJHAdjustmentChanged)
		return
	}
	amount := in.Amount
	if amount == "" {
		amount = evidence.SuggestedAmount
	}
	// Explicit amounts support a documented historical adjustment; the normal
	// one-click action always uses the current server-calculated opposite gap.
	out, err := s.store.AppendEJHAdjustment(r.Context(), evidence, in.RequestID, actor, in.Note, amount)
	if err != nil {
		if errors.Is(err, store.ErrEJHAdjustmentChanged) {
			ejhAdjustmentError(w, err)
		} else {
			writeAPI(w, 400, nil, err.Error())
		}
		return
	}
	writeAPI(w, 200, out, "")
}
