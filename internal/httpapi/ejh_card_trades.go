package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
	"net/http"
	"sort"
	"time"
)

// Detail pagination is read-only. Complete collections are saved by the
// background updater so browsing a partial page cannot change attribution.
func (s *Server) queryEJHCardTrades(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start    string `json:"start"`
		End      string `json:"end"`
		Page     int    `json:"page"`
		PageSize int    `json:"page_size"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	// Read a consistent, request-local snapshot without taking the issuer's
	// mutation lock or waiting for the application's shared write connection.
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取 EJH 明细配置失败")
		return
	}
	defer reader.Close()
	view := &Server{store: reader}
	if in.PageSize == 0 {
		in.PageSize = view.defaultPageSize()
	}
	switch in.PageSize {
	case 10, 50, 100, 500:
	default:
		writeAPI(w, 400, nil, "每页条数必须为 10、50、100 或 500")
		return
	}
	zone := time.FixedZone("CST", 8*3600)
	start, e1 := time.ParseInLocation("2006-01-02", in.Start, zone)
	end, e2 := time.ParseInLocation("2006-01-02", in.End, zone)
	end = end.Add(24*time.Hour - time.Second)
	if e1 != nil || e2 != nil || end.Before(start) || end.Sub(start) > 90*24*time.Hour || in.Page < 1 || in.Page > 10000 {
		writeAPI(w, 400, nil, "请选择 1～90 天的日期范围和有效页码")
		return
	}
	var card ejh.CardRecord
	if err := reader.EJHGet("card", r.PathValue("id"), &card); err != nil || card.Remote.OrderNo == "" {
		writeAPI(w, 404, nil, "未找到绑定卡台的银行卡")
		return
	}
	if card.Remote.Status == "2" {
		records, err := store.EJHRecords[ejh.TradeRecord](reader, "trade")
		if err != nil {
			writeAPI(w, 500, nil, err.Error())
			return
		}
		items := []ejh.Trade{}
		queriedAt := card.SyncedAt
		for _, t := range records {
			if t.CardID == card.ID && t.SyncedAt.After(queriedAt) {
				queriedAt = t.SyncedAt
			}
			at := ejh.TradeAt(t.Trade)
			if t.CardID == card.ID && !at.Before(start) && !at.After(end) {
				v := t.Trade
				v.Number = maskEJH(v.Number)
				items = append(items, v)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			a, b := ejh.TradeAt(items[i]), ejh.TradeAt(items[j])
			if a.Equal(b) {
				return items[i].TradeNo < items[j].TradeNo
			}
			return a.After(b)
		})
		offset := (in.Page - 1) * in.PageSize
		if offset > len(items) {
			offset = len(items)
		}
		limit := offset + in.PageSize
		if limit > len(items) {
			limit = len(items)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeAPI(w, 200, map[string]any{"items": items[offset:limit], "page": in.Page, "page_size": in.PageSize, "has_more": limit < len(items), "queried_at": queriedAt, "cached": true}, "")
		return
	}
	cfg, key, err := view.ejhConfig()
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	// Do not retain the SQLite snapshot during potentially slow issuer I/O.
	reader.Close()
	client := s.ejhClient()
	// EJH pages stay at 100 rows. Slice small display pages and combine
	// larger display pages sequentially; browsing never saves partial data.
	offset := (in.Page - 1) * in.PageSize
	remotePage, skip := offset/100+1, offset%100
	items := []ejh.Trade{}
	hasMore := false
	seen := map[string]bool{}
	for len(items) < in.PageSize {
		var out struct {
			Trades []ejh.Trade `json:"tradeInfos"`
		}
		err = client.Call(r.Context(), cfg, key, "card.trade.info", map[string]any{"orderNo": card.Remote.OrderNo, "tradeTime": start.Format("2006-01-02 15:04:05"), "endTime": end.Format("2006-01-02 15:04:05"), "page": remotePage, "size": 100}, &out)
		if err != nil {
			writeAPI(w, 502, nil, err.Error())
			return
		}
		if out.Trades == nil || len(out.Trades) > 100 {
			writeAPI(w, 502, nil, "卡台未返回有效交易列表，保留已有记录")
			return
		}
		for _, t := range out.Trades {
			id := store.EJHID("trade", t.OrderNo+"|"+t.TradeNo+"|"+t.Type)
			if t.OrderNo != card.Remote.OrderNo || t.TradeNo == "" || t.Type == "" || seen[id] {
				writeAPI(w, 502, nil, "卡台交易归属或流水编号异常，保留已有记录")
				return
			}
			seen[id] = true
		}
		begin := min(skip, len(out.Trades))
		end := min(len(out.Trades), begin+in.PageSize-len(items))
		items = append(items, out.Trades[begin:end]...)
		hasMore = end < len(out.Trades) || len(out.Trades) == 100
		if !hasMore || len(items) == in.PageSize {
			break
		}
		remotePage++
		skip = 0
	}
	for i := range items {
		items[i].Number = maskEJH(items[i].Number)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, 200, map[string]any{"items": items, "page": in.Page, "page_size": in.PageSize, "has_more": hasMore, "queried_at": time.Now()}, "")
}
