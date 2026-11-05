package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
)

// Only display projections are cached. All existing mutation/admission handlers
// continue reading and checking the authoritative store inside their own locks.
type ejhViewCache struct {
	mu       sync.Mutex
	revision string
	data     map[string]any
	inflight *ejhViewRead
}

type ejhViewRead struct {
	done     chan struct{}
	data     map[string]any
	revision string
	err      error
}

func (s *Server) ejhDisplaySnapshot(ctx context.Context) (map[string]any, string, error) {
	return s.ejh.views.read(ctx, s.store.ReadRevision, func(ctx context.Context) (map[string]any, error) {
		reader, err := s.store.ProListReader(ctx)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return (&Server{store: reader}).readEJHDashboard(false)
	})
}

func (c *ejhViewCache) read(ctx context.Context, version func(context.Context) (string, error), collect func(context.Context) (map[string]any, error)) (map[string]any, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	revision, err := version(ctx)
	if err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	if c.data != nil && c.revision == revision {
		data := c.data
		c.mu.Unlock()
		return data, revision, nil
	}
	job := c.inflight
	if job == nil {
		job = &ejhViewRead{done: make(chan struct{}), revision: revision}
		c.inflight = job
		// Share one saved-data read even if navigation cancels the first request.
		// Cancelled waiters leave immediately rather than queueing more projections.
		go func() {
			readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			job.data, job.err = collect(readCtx)
			c.mu.Lock()
			if job.err == nil {
				c.data, c.revision = job.data, revision
			}
			// Keep the version from BEFORE collection. A concurrent commit must
			// invalidate this snapshot on the next request, never mark it fresh.
			c.inflight = nil
			close(job.done)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-job.done:
		// A read requested after a completed mutation cannot reuse a collection
		// that started before that mutation. Recheck once the shared read ends.
		if job.err == nil && job.revision != revision {
			return c.read(ctx, version, collect)
		}
		return job.data, job.revision, job.err
	}
}

func (s *Server) ejhRevision(w http.ResponseWriter, r *http.Request) {
	revision, err := s.store.ReadRevision(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取数据版本失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, 200, map[string]any{"revision": revision}, "")
}

// Financial inputs remain unpaginated and use exactly the legacy dashboard
// projection. Audits and OAI workflow traces are not financial inputs.
func (s *Server) ejhOverview(w http.ResponseWriter, r *http.Request) {
	data, revision, err := s.ejhDisplaySnapshot(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取余额总览失败")
		return
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		if k != "audit" {
			out[k] = v
		}
	}
	cases := append([]ejh.Case{}, data["cases"].([]ejh.Case)...)
	for i := range cases {
		cases[i].ProOAIConfirmations = nil
		cases[i].ProOAIAvailableModes = nil
	}
	out["cases"], out["revision"] = cases, revision
	if r.URL.Query().Get("format") == "delta" {
		reply, err := s.ejh.overview.response(out, revision, r.URL.Query().Get("since"))
		if err != nil {
			writeAPI(w, 500, nil, "读取余额总览失败")
			return
		}
		writeProAccountList(w, r, reply)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeProAccountList(w, r, out)
}

func ejhDisplayState(c ejhDisplayCard) string {
	if !c.Linked {
		return "unlinked"
	}
	if c.Remote.Status == "2" {
		return "2"
	}
	pending := ""
	if c.Local != nil {
		pending = c.Local.IssuerCancelStatus
	}
	if c.Remote.Status == "8" || pending == "processing" {
		return "8"
	}
	if pending == "submitting" || pending == "unknown" {
		return "cancel_pending"
	}
	if c.NormalPending || c.Local != nil && c.Local.NormalPending {
		return "normal_pending"
	}
	if c.Error != "" {
		return "review"
	}
	if c.Remote.Locked == "1" {
		return "5"
	}
	if strings.Contains("|1|3|4|5|6|7|", "|"+c.Remote.Status+"|") && c.Remote.Status != "" {
		return c.Remote.Status
	}
	return "unknown"
}

func ejhDisplayLifecycle(c ejhDisplayCard) string {
	if c.Remote.Status == "2" || c.Remote.Status == "8" || c.Local != nil && c.Local.IssuerCancelStatus != "" {
		return "closed"
	}
	if c.NormalPending {
		return "normal_pending"
	}
	return "normal"
}

func ejhDisplayCaseState(c ejh.Case, policies map[string]bool) string {
	id := c.EJHCardID
	if id == "" {
		id = c.CardID
	}
	if policies[id] && c.State != "unopened" {
		return "no_return"
	}
	return c.State
}

func ejhSearch(value any, query string) bool {
	if query == "" {
		return true
	}
	raw, _ := json.Marshal(value)
	return strings.Contains(strings.ToLower(string(raw)), query)
}

func ejhDisplayTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, value, time.FixedZone("CST", 8*3600)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func ejhTradeDisplayTime(t ejh.TradeRecord) time.Time {
	posted, initiated := ejhDisplayTime(t.Trade.CreatedAt), ejhDisplayTime(t.Trade.TradeTime)
	if t.Trade.IsReturn() && !posted.IsZero() {
		return posted
	}
	if !initiated.IsZero() {
		return initiated
	}
	return posted
}

func (s *Server) ejhView(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	section := q.Get("section")
	if section == "" {
		section = "cards"
	}
	if section != "cards" && section != "cases" && section != "trades" && section != "operations" && section != "audit" && section != "card" && section != "balance_targets" && section != "pending_targets" {
		writeAPI(w, 400, nil, "读取分区无效")
		return
	}
	closureState := q.Get("closure_state")
	if closureState != "" && closureState != "processing" && closureState != "completed" {
		writeAPI(w, 400, nil, "销卡分类无效")
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size == 0 {
		size = 10
	}
	if size < 1 || size > 500 {
		writeAPI(w, 400, nil, "每页条数须为1～500")
		return
	}
	data, revision, err := s.ejhDisplaySnapshot(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取卡台数据失败")
		return
	}
	cards := data["cards"].([]ejhDisplayCard)
	cases := data["cases"].([]ejh.Case)
	trades := data["trades"].([]ejh.TradeRecord)
	ops := data["operations"].([]ejh.Operation)
	policies := map[string]bool{}
	for _, p := range data["payment_policies"].([]store.CardPaymentPolicy) {
		policies[p.CardID] = p.Mode == "payment_only"
	}
	for _, c := range cases {
		if c.ProDead != nil {
			id := c.EJHCardID
			if id == "" {
				id = c.CardID
			}
			policies[id] = false
		}
	}
	byCard := map[string][]ejh.Case{}
	counts := map[string]int{"unopened": 0, "opened": 0, "waiting": 0, "returned": 0, "no_return": 0}
	for _, c := range cases {
		counts[ejhDisplayCaseState(c, policies)]++
		byCard[c.CardID] = append(byCard[c.CardID], c)
		if c.EJHCardID != "" && c.EJHCardID != c.CardID {
			byCard[c.EJHCardID] = append(byCard[c.EJHCardID], c)
		}
	}
	lifecycle := map[string]int{"normal": 0, "normal_pending": 0, "closed": 0}
	for _, c := range cards {
		lifecycle[ejhDisplayLifecycle(c)]++
	}
	out := map[string]any{"cards": []ejhDisplayCard{}, "cases": []ejh.Case{}, "trades": []ejh.TradeRecord{}, "operations": []ejh.Operation{}, "audit": []map[string]any{}, "payment_policies": data["payment_policies"], "refund_fees": data["refund_fees"], "reversal_fee_percent": data["reversal_fee_percent"], "revision": revision, "counts": counts, "lifecycle_counts": lifecycle, "card_total": len(cards), "case_counts": map[string]int{"normal": counts["opened"] + counts["waiting"] + counts["returned"] + counts["no_return"], "unopened": counts["unopened"]}, "server_time": data["server_time"], "sync": data["sync"], "auto_cancel": data["auto_cancel"]}
	if section == "balance_targets" || section == "pending_targets" {
		targets := []ejhDisplayCard{}
		for _, c := range cards {
			if section == "pending_targets" {
				platform := c.Platform
				if platform == "" {
					platform = "oai"
				}
				if c.NormalPending && ejhDisplayLifecycle(c) == "normal_pending" && (q.Get("platform") == "" || platform == q.Get("platform")) {
					targets = append(targets, c)
				}
				continue
			}
			if c.Linked && !c.NormalPending && c.Remote.Status != "2" && c.Remote.Status != "8" && ejhDisplayState(c) != "8" {
				targets = append(targets, c)
			}
		}
		out["cards"] = targets
		writeProAccountList(w, r, out)
		return
	}
	selected := []any{}
	closureCounts := map[string]int{"processing": 0, "completed": 0}
	out["closure_counts"] = closureCounts
	query := strings.ToLower(strings.TrimSpace(q.Get("q")))
	status := q.Get("status")
	switch section {
	case "cards", "card":
		sorted := append([]ejhDisplayCard{}, cards...)
		created := func(c ejhDisplayCard) time.Time {
			if c.Local != nil && !c.Local.CreatedAt.IsZero() {
				return c.Local.CreatedAt
			}
			return c.CreatedAt
		}
		rank := func(c ejhDisplayCard) int {
			if c.Local != nil && c.Local.PendingAccounts > 0 {
				return 0
			}
			rows := byCard[c.ID]
			if len(rows) == 0 {
				if c.Local != nil && c.Local.OpenedAccounts > 0 {
					return 1
				}
				return 0
			}
			n := 4
			for _, v := range rows {
				p, ok := map[string]int{"unopened": 0, "opening": 0, "opened": 1, "waiting": 2, "returned": 3, "no_return": 4}[ejhDisplayCaseState(v, policies)]
				if !ok {
					p = 4
				}
				if p < n {
					n = p
				}
			}
			return n
		}
		closedAt := func(c ejhDisplayCard) time.Time {
			var byID, byOrder time.Time
			for _, o := range ops {
				if o.Method != "cancel" || o.Status != "completed" || o.UpdatedAt.Year() <= 1970 {
					continue
				}
				if o.CardID == c.ID && o.UpdatedAt.After(byID) {
					byID = o.UpdatedAt
				}
				if o.OrderNo != "" && o.OrderNo == c.Remote.OrderNo && o.UpdatedAt.After(byOrder) {
					byOrder = o.UpdatedAt
				}
			}
			if !byID.IsZero() {
				return byID
			}
			return byOrder
		}
		group := func(c ejhDisplayCard) int {
			v := ejhDisplayState(c)
			if v == "8" || v == "cancel_pending" {
				return 0
			}
			if v == "2" {
				return 1
			}
			return 2
		}
		sort.SliceStable(sorted, func(i, j int) bool {
			a, b := sorted[i], sorted[j]
			if q.Get("lifecycle") == "closed" {
				if group(a) != group(b) {
					return group(a) < group(b)
				}
				if group(a) == 1 && !closedAt(a).Equal(closedAt(b)) {
					return closedAt(a).After(closedAt(b))
				}
			} else if rank(a) != rank(b) {
				return rank(a) < rank(b)
			}
			if !created(a).Equal(created(b)) {
				return created(a).After(created(b))
			}
			return a.ID < b.ID
		})
		for _, c := range sorted {
			target := q.Get("card_id")
			if target != "" && c.ID != target {
				continue
			}
			if section == "card" {
				if target == "" {
					writeAPI(w, 400, nil, "缺少卡片编号")
					return
				}
				selected = append(selected, c)
				continue
			}
			platform := c.Platform
			if platform == "" {
				for _, o := range ops {
					if o.Method == "open" && (o.CardID == c.ID || o.OrderNo != "" && o.OrderNo == c.Remote.OrderNo) {
						platform = o.Platform
						break
					}
				}
				if platform == "" {
					platform = "oai"
				}
			}
			if target == "" && (q.Get("platform") != "" && platform != q.Get("platform") || q.Get("lifecycle") != "" && ejhDisplayLifecycle(c) != q.Get("lifecycle")) {
				continue
			}
			if state := q.Get("card_state"); state != "" && ejhDisplayState(c) != state {
				continue
			}
			matched := ejhSearch(c, query)
			for _, v := range byCard[c.ID] {
				matched = matched || strings.Contains(strings.ToLower(v.Email), query)
			}
			if !matched {
				continue
			}
			if state := q.Get("return_state"); state != "" {
				found := state == "none" && len(byCard[c.ID]) == 0
				for _, v := range byCard[c.ID] {
					found = found || ejhDisplayCaseState(v, policies) == state
				}
				if !found {
					continue
				}
			}
			closure := ""
			switch ejhDisplayState(c) {
			case "8", "cancel_pending":
				closure = "processing"
			case "2":
				closure = "completed"
			}
			if closure != "" {
				closureCounts[closure]++
			}
			if target == "" && q.Get("lifecycle") == "closed" && closureState != "" && closure != closureState {
				continue
			}
			selected = append(selected, c)
		}
	case "cases":
		for _, c := range cases {
			state := ejhDisplayCaseState(c, policies)
			if q.Get("category") == "unopened" {
				if state != "unopened" {
					continue
				}
			} else if state != "opened" && state != "waiting" && state != "returned" && state != "no_return" {
				continue
			}
			if status != "" && state != status {
				continue
			}
			if ejhSearch(c, query) {
				selected = append(selected, c)
			}
		}
	case "trades":
		sorted := append([]ejh.TradeRecord{}, trades...)
		sort.SliceStable(sorted, func(i, j int) bool {
			a, b := sorted[i], sorted[j]
			at, bt := ejhTradeDisplayTime(a), ejhTradeDisplayTime(b)
			if !at.Equal(bt) {
				return at.After(bt)
			}
			at, bt = ejhDisplayTime(a.Trade.CreatedAt), ejhDisplayTime(b.Trade.CreatedAt)
			if !at.Equal(bt) {
				return at.After(bt)
			}
			return a.ID < b.ID
		})
		for _, v := range sorted {
			if !ejhSearch(v, query) {
				continue
			}
			if status != "" {
				match := false
				switch status {
				case "unmatched":
					match = v.Trade.IsReturn() && v.OrderID == ""
				case "returned":
					match = v.Confirmed
				case "pending":
					match = v.Trade.IsReturn() && !v.Confirmed
				default:
					match = !v.Trade.IsReturn()
				}
				if !match {
					continue
				}
			}
			selected = append(selected, v)
		}
	case "operations":
		for _, v := range ops {
			if ejhSearch(v, query) {
				selected = append(selected, v)
			}
		}
	case "audit":
		reader, e := s.store.ProListReader(r.Context())
		if e != nil {
			writeAPI(w, 500, nil, "读取审计失败")
			return
		}
		defer reader.Close()
		rows, e := store.EJHRecords[map[string]any](reader, "match_audit")
		if e != nil {
			writeAPI(w, 500, nil, "读取审计失败")
			return
		}
		visible := map[string]bool{}
		for _, c := range cards {
			visible[c.ID] = true
		}
		for _, v := range rows {
			include := false
			for _, key := range []string{"before", "after"} {
				if obj, ok := v[key].(map[string]any); ok {
					if trade, ok := obj["trade"].(map[string]any); ok {
						delete(trade, "cardNo")
					}
					id, _ := obj["card_id"].(string)
					include = include || visible[id]
				}
			}
			if include && ejhSearch(v, query) {
				selected = append(selected, v)
			}
		}
	}
	total := len(selected)
	last := max(1, (total+size-1)/size)
	page = min(page, last)
	start := min((page-1)*size, total)
	items := selected[start:min(start+size, total)]
	out["items"], out["total"], out["page"], out["page_size"] = items, total, page, size
	cardIDs := map[string]bool{}
	caseIDs := map[string]bool{}
	for _, item := range items {
		switch v := item.(type) {
		case ejhDisplayCard:
			cardIDs[v.ID] = true
		case ejh.Case:
			cardIDs[v.CardID] = true
			cardIDs[v.EJHCardID] = true
			caseIDs[v.ID] = true
		case ejh.TradeRecord:
			cardIDs[v.CardID] = true
			caseIDs[v.OrderID] = true
		}
	}
	delete(cardIDs, "")
	delete(caseIDs, "")
	depsCases := []ejh.Case{}
	for _, c := range cases {
		if cardIDs[c.CardID] || cardIDs[c.EJHCardID] || caseIDs[c.ID] {
			depsCases = append(depsCases, c)
		}
	}
	depsCards := []ejhDisplayCard{}
	orders := map[string]bool{}
	for _, c := range cards {
		if cardIDs[c.ID] {
			depsCards = append(depsCards, c)
			orders[c.Remote.OrderNo] = true
		}
	}
	// Fee allocations can span multiple cards. Keep every referenced trade so
	// a paginated card keeps the exact same allocated fee as the full ledger.
	feeTradeIDs := map[string]bool{}
	for _, fee := range data["refund_fees"].([]store.EJHRefundFee) {
		for _, id := range fee.TradeIDs {
			feeTradeIDs[id] = true
		}
	}
	depsTrades := []ejh.TradeRecord{}
	for _, t := range trades {
		if cardIDs[t.CardID] || feeTradeIDs[t.ID] {
			depsTrades = append(depsTrades, t)
		}
	}
	depsOps := []ejh.Operation{}
	for _, o := range ops {
		if cardIDs[o.CardID] || o.OrderNo != "" && orders[o.OrderNo] {
			depsOps = append(depsOps, o)
		}
	}
	out["cards"], out["cases"], out["trades"], out["operations"] = depsCards, depsCases, depsTrades, depsOps
	// Main rows and dependent collections are separate: a page of cases must not
	// render every other case needed for that card's accounting/admission hints.
	w.Header().Set("Cache-Control", "no-store")
	writeProAccountList(w, r, out)
}

func (s *Server) ejhOperationProgress(w http.ResponseWriter, r *http.Request) {
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(ids) > 50 {
		writeAPI(w, 400, nil, "最多查询50条操作")
		return
	}
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取操作进度失败")
		return
	}
	defer reader.Close()
	rows := []ejh.Operation{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		var op ejh.Operation
		if err = reader.EJHGet("operation", id, &op); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			writeAPI(w, 500, nil, fmt.Sprintf("读取操作 %s 失败", id))
			return
		}
		op.Result = nil
		rows = append(rows, op)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, 200, map[string]any{"items": rows}, "")
}
