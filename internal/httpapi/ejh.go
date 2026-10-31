package httpapi

import (
	"chatgpt-space-merge/internal/cardplatform"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

type ejhRuntime struct {
	lifecycle    sync.Mutex
	autoCancelMu sync.Mutex
	mu           sync.Mutex
	client       *ejh.Client
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	syncMu       sync.Mutex
	syncJobs     map[string]*ejhSyncTask
	ctx          context.Context
	syncClosed   bool // guarded by lifecycle; prevents Add racing shutdown Wait
}

func (s *Server) ejhClientLocked() *ejh.Client {
	if s.ejh.client == nil {
		s.ejh.client = ejh.New()
	}
	return s.ejh.client
}
func (s *Server) ejhConfig() (ejh.Settings, string, error) {
	c, k, e := s.store.EJHSettings()
	if e != nil {
		return c, k, e
	}
	// Manual card administration remains available while automation is disabled.
	if !c.SecretPresent {
		return c, k, fmt.Errorf("请先在 EJH 卡台保存接口配置")
	}
	return c, k, nil
}
func (s *Server) ejhRoutes(m *http.ServeMux) {
	m.HandleFunc("PUT /api/ejh/cards/{id}/payment-policy", s.saveCardPaymentPolicy("ejh"))
	m.HandleFunc("PUT /api/ejh/cards/{id}/category", s.ejhCardCategory)
	m.HandleFunc("PUT /api/ejh/refund-fees/{id}", s.saveEJHRefundFee)
	m.HandleFunc("PUT /api/ejh/recharge-ledger", s.saveEJHRechargeLedger)
	m.HandleFunc("PUT /api/ejh/external-transfer", s.saveEJHExternalTransfer)
	m.HandleFunc("PUT /api/ejh/recharge-period", s.saveEJHRechargePeriod)
	m.HandleFunc("DELETE /api/ejh/cases/unopened", s.deleteEJHUnopenedCases)
	m.HandleFunc("DELETE /api/ejh/cases/{id}", s.deleteEJHUnopenedCases)
	m.HandleFunc("PUT /api/ejh/cases/{id}/state", s.setEJHCaseState)
	m.HandleFunc("POST /api/ejh/cards/{id}/trades", s.queryEJHCardTrades)
	m.HandleFunc("GET /api/ejh/occupancy/{id}", s.getEJHOccupancyOrder)
	m.HandleFunc("POST /api/ejh/occupancy/{id}/resolve", s.resolveEJHOccupancy)
	m.HandleFunc("GET /api/ejh/settings", s.getEJHSettings)
	m.HandleFunc("PUT /api/ejh/settings", s.saveEJHSettings)
	m.HandleFunc("POST /api/ejh/test", s.testEJH)
	m.HandleFunc("POST /api/ejh/balance", s.testEJH)
	m.HandleFunc("GET /api/ejh/dashboard", s.ejhDashboard)
	m.HandleFunc("GET /api/ejh/finance", s.ejhFinance)
	m.HandleFunc("POST /api/ejh/sync", s.syncEJH)
	m.HandleFunc("GET /api/ejh/sync/{id}", s.getEJHSyncTask)
	m.HandleFunc("POST /api/ejh/open", s.openEJH)
	m.HandleFunc("POST /api/ejh/cards/{id}/action", s.ejhCardAction)
	m.HandleFunc("POST /api/ejh/cards/{id}/private", s.ejhCardPrivate)
	m.HandleFunc("POST /api/ejh/operations/{id}/resolve", s.resolveEJHOperation)
	m.HandleFunc("PUT /api/ejh/trades/{id}/match", s.matchEJHTrade)
	m.HandleFunc("PUT /api/ejh/cases/{id}/charge", s.linkEJHCharge)
	m.HandleFunc("POST /api/ejh/trades/{id}/unmatch", s.unmatchEJHTrade)
}
func (s *Server) getEJHSettings(w http.ResponseWriter, r *http.Request) {
	reader, e := s.store.ProListReader(r.Context())
	if e != nil {
		writeAPI(w, 500, nil, "读取 EJH 配置失败")
		return
	}
	defer reader.Close()
	c, _, e := reader.EJHSettings()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	writeAPI(w, 200, c, "")
}
func (s *Server) saveEJHSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ejh.Settings
		Secret string `json:"app_secret"`
	}
	// Older clients omit the new switch; preserve the saved/default policy.
	current, _, err := s.store.EJHSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	in.SingleActiveAccount = current.SingleActiveAccount
	in.ReversalFeePercent = current.ReversalFeePercent
	in.PageRefreshSeconds = current.PageRefreshSeconds
	in.AutoCancelEnabled = current.AutoCancelEnabled
	in.AutoCancelIntervalSeconds = current.AutoCancelIntervalSeconds
	if decodeJSON(w, r, &in, 256<<10) != nil {
		return
	}
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	c, e := s.store.SaveEJHSettings(in.Settings, in.Secret)
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	writeAPI(w, 200, c, "")
}
func (s *Server) testEJH(w http.ResponseWriter, r *http.Request) {
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	c, k, e := s.ejhConfig()
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	var out struct {
		AppKey   string `json:"appKey"`
		Balances []struct {
			Currency string `json:"currency"`
			Name     string `json:"currencyName"`
			Balance  string `json:"balance"`
		} `json:"abroadList"`
	}
	if e = s.ejhClientLocked().Call(r.Context(), c, k, "account.balance.info", map[string]any{}, &out); e != nil {
		writeAPI(w, 502, nil, e.Error())
		return
	}
	if out.Balances == nil {
		writeAPI(w, 502, nil, "卡台未返回账户余额，保留上次查询结果")
		return
	}
	seen := map[string]bool{}
	for _, b := range out.Balances {
		_, err := ejh.Money(strings.TrimPrefix(b.Balance, "-"))
		if err != nil || !ejh.ValidCurrency(b.Currency) || seen[b.Currency] {
			writeAPI(w, 502, nil, "卡台余额或币种无效，保留上次查询结果")
			return
		}
		seen[b.Currency] = true
	}
	if e = s.store.EJHPut("balance", "latest", map[string]any{"data": out, "at": time.Now()}); e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	writeAPI(w, 200, out, "")
}
func (s *Server) ejhDashboard(w http.ResponseWriter, r *http.Request) {
	// The dashboard is a pure local projection. A dedicated WAL snapshot keeps
	// it consistent and independent of long-running background store writes.
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取 EJH 数据失败")
		return
	}
	defer reader.Close()
	view := &Server{store: reader}
	view.ejhDashboardSnapshot(w, r)
}

func (s *Server) ejhDashboardSnapshot(w http.ResponseWriter, r *http.Request) {
	cards, e := store.EJHRecords[ejh.CardRecord](s.store, "card")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	ops, e := store.EJHRecords[ejh.Operation](s.store, "operation")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	trades, e := store.EJHRecords[ejh.TradeRecord](s.store, "trade")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	cases, e := s.store.EJHCases()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	ejh.ApplyReversalFeeRates(trades, cases)
	checks, e := store.EJHRecords[ejhAutoCancelCheck](s.store, "auto_cancel_check")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	checkByCard := map[string]ejhAutoCancelCheck{}
	for _, check := range checks {
		checkByCard[check.CardID] = check
	}
	type row struct {
		ejh.CardRecord
		AutoCancelCheck *ejhAutoCancelCheck `json:"auto_cancel_check,omitempty"`
		Local           *gptpay.Card        `json:"local,omitempty"`
		Linked          bool                `json:"issuer_linked"`
	}
	rows := []row{}
	visibleCards := map[string]bool{}
	locals, e := s.store.GPTPayCardsByType("ejh")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	localByID := make(map[string]gptpay.Card, len(locals))
	for _, local := range locals {
		localByID[local.ID] = local
	}
	for _, c := range cards {
		kind, err := s.store.ProCardTypeForNumber(c.Remote.Number)
		if err != nil {
			writeAPI(w, 500, nil, err.Error())
			return
		}
		if kind != "ejh" {
			continue
		}
		c.Remote.Number = maskEJH(c.Remote.Number)
		c.Remote.CVV = ""
		v := row{CardRecord: c, Linked: true}
		if check, ok := checkByCard[c.ID]; ok {
			v.AutoCancelCheck = &check
		}
		if local, ok := localByID[c.ID]; ok {
			v.Local = &local
		}
		rows = append(rows, v)
		visibleCards[c.ID] = true
	}
	for _, local := range locals {
		if local.CardType != "ejh" || visibleCards[local.ID] || local.Source == "ejh" {
			continue
		}
		copy := local
		rows = append(rows, row{CardRecord: ejh.CardRecord{ID: local.ID, Remote: ejh.Card{Number: "**** " + local.Last4, Validity: fmt.Sprintf("%02d%02d", local.ExpMonth, local.ExpYear%100)}}, Local: &copy})
		visibleCards[local.ID] = true
	}
	filteredCases := []ejh.Case{}
	for _, v := range cases {
		if v.CardType == "ejh" && (v.RemovedAt == nil || v.State != "unopened") {
			filteredCases = append(filteredCases, v)
		}
	}
	cases = filteredCases
	filteredTrades := []ejh.TradeRecord{}
	for _, v := range trades {
		if visibleCards[v.CardID] {
			filteredTrades = append(filteredTrades, v)
		}
	}
	trades = filteredTrades
	filteredOps := []ejh.Operation{}
	for _, v := range ops {
		if v.CardID == "" || visibleCards[v.CardID] {
			filteredOps = append(filteredOps, v)
		}
	}
	ops = filteredOps
	for i := range trades {
		trades[i].Trade.Number = maskEJH(trades[i].Trade.Number)
	}
	for i := range ops {
		if ops[i].Result != nil {
			v := *ops[i].Result
			v.Number = maskEJH(v.Number)
			v.CVV = ""
			ops[i].Result = &v
		}
	}
	var syncState ejh.SyncState
	_ = s.store.EJHGet("sync", "latest", &syncState)
	var balance any
	_ = s.store.EJHGet("balance", "latest", &balance)
	audit, e := store.EJHRecords[map[string]any](s.store, "match_audit")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	for _, a := range audit {
		for _, key := range []string{"before", "after"} {
			if obj, ok := a[key].(map[string]any); ok {
				if t, ok := obj["trade"].(map[string]any); ok {
					delete(t, "cardNo")
				}
			}
		}
	}
	filteredAudit := []map[string]any{}
	for _, a := range audit {
		include := false
		for _, key := range []string{"before", "after"} {
			if obj, ok := a[key].(map[string]any); ok {
				if id, ok := obj["card_id"].(string); ok && visibleCards[id] {
					include = true
				}
			}
		}
		if include {
			filteredAudit = append(filteredAudit, a)
		}
	}
	audit = filteredAudit
	ledger, err := s.store.EJHRechargeLedger()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	settings, _, err := s.store.EJHSettings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	usage, err := store.EJHRecords[store.EJHConsumption](s.store, "finance_usage")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	applyEJHFinancialConsumption(cases, usage)
	refundFees, err := s.store.EJHRefundFees()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	merchantFees, feeErr := s.store.ProMerchantFees("ejh")
	if feeErr != nil {
		writeAPI(w, 500, nil, "读取 API 收费失败")
		return
	}
	policies, policyErr := s.store.CardPaymentPolicies("ejh")
	if policyErr != nil {
		writeAPI(w, 500, nil, "读取卡片回款方式失败")
		return
	}
	var autoCancel ejhAutoCancelState
	_ = s.store.EJHGet("auto_cancel", "latest", &autoCancel)
	writeAPI(w, 200, map[string]any{"auto_cancel": autoCancel, "payment_policies": policies, "merchant_fees": merchantFees, "cards": rows, "operations": ops, "trades": trades, "cases": cases, "sync": syncState, "balance": balance, "recharge_ledger": ledger, "refund_fees": refundFees, "reversal_fee_percent": settings.ReversalFeePercent, "page_refresh_seconds": settings.PageRefreshSeconds, "consumption_history": usage, "audit": audit, "server_time": time.Now()}, "")
}
func maskEJH(n string) string {
	if len(n) > 4 {
		return "**** " + n[len(n)-4:]
	}
	return "****"
}
func (s *Server) ejhOpenLocked(ctx context.Context, id string, input ejh.OpenInput, automatic bool, templateIDs ...string) (ejh.Operation, error) {
	c, k, e := s.ejhConfig()
	if e != nil {
		return ejh.Operation{}, e
	}
	if automatic && !c.Enabled {
		return ejh.Operation{}, fmt.Errorf("EJH 卡台已停用，不能自动开卡")
	}
	if e = ejh.ValidateOpen(input, time.Now()); e != nil {
		return ejh.Operation{}, e
	}
	var prior ejh.Operation
	if err := s.store.EJHGet("operation", id, &prior); err == nil {
		old, _ := json.Marshal(prior.Request)
		next, _ := json.Marshal(input)
		if string(old) != string(next) || prior.Method != "open" {
			return prior, fmt.Errorf("请求编号已用于其他参数")
		}
		return prior, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return prior, fmt.Errorf("无法核实既有操作，未提交开卡: %w", err)
	}
	ops, e := store.EJHRecords[ejh.Operation](s.store, "operation")
	if e != nil {
		return prior, e
	}
	for _, o := range ops {
		if o.Method == "open" && (o.Status == "submitting" || o.Status == "unknown" || o.Status == "import_pending") {
			return prior, fmt.Errorf("存在待核实开卡操作，请先核对操作记录，禁止重复扣款")
		}
	}
	op := ejh.Operation{ID: id, Method: "open", Status: "submitting", Amount: input.CardAmount, Currency: input.CardCurrency, Automatic: automatic, CreatedAt: time.Now(), UpdatedAt: time.Now(), Request: input}
	op.RestockTaskID, _ = ctx.Value(proRestockContextKey{}).(string)
	if len(templateIDs) > 0 {
		op.TemplateID = templateIDs[0]
		for _, t := range c.Templates {
			if t.ID == op.TemplateID {
				op.PlanCode = t.PlanCode
				op.Platform, op.TemplateName = t.Platform, t.Name
			}
		}
	}
	if op.TemplateID != "" && op.TemplateName == "" {
		return op, fmt.Errorf("请选择已保存的 EJH 开卡模板")
	}
	if automatic && !cardplatform.IsOAI(op.Platform) {
		return op, fmt.Errorf("Pro 开卡只能使用 OAI 模板")
	}
	if op.Platform == "" {
		op.Platform = "oai"
	}
	if e = s.store.EJHBeginOperation(op); e != nil {
		return op, e
	}
	var card ejh.Card
	e = s.ejhClientLocked().Call(ctx, c, k, "card.open.info", input, &card)
	op.UpdatedAt = time.Now()
	if e != nil {
		op.Status = "unknown"
		op.Error = e.Error()
		var apiErr *ejh.APIError
		if errors.As(e, &apiErr) && definiteEJHReject(apiErr.Code) {
			op.ErrorCode = apiErr.Code
			op.Status = "failed"
		}
	} else {
		op.OrderNo = card.OrderNo
		op.Result = &card
		op.Status = "import_pending"
		if e = s.store.EJHPut("operation", id, op); e != nil {
			s.notifyEJHOpened(op)
			return op, e
		}
		local, ie := s.store.ImportEJHCard(card, true, &op)
		if ie != nil {
			op.Error = ie.Error()
		} else {
			op.CardID = local.ID
			op.Status = "completed"
		}
	}
	if saveErr := s.store.EJHPut("operation", id, op); saveErr != nil {
		notice := op
		notice.Status = "unknown"
		s.notifyEJHOpened(notice)
		return op, saveErr
	}
	s.notifyEJHOpened(op)
	var rejected *ejh.APIError
	if op.Status == "failed" && errors.As(e, &rejected) && (rejected.Code == "100004" || rejected.Code == "800003") {
		return op, fmt.Errorf("%w：%s", errIssuerInsufficientBalance, rejected.Error())
	}
	return op, nil
}
func definiteEJHReject(code string) bool {
	switch code {
	case "000007", "000008", "100002", "100003", "100004", "100005", "100006", "100007", "100009", "100010", "100011", "100012", "100013", "100016", "100017", "100018", "100019", "100022", "100023", "100024", "100029", "800003", "800005", "800009":
		return true
	}
	return false
}
func (s *Server) openEJH(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID         string        `json:"request_id"`
		Input      ejh.OpenInput `json:"input"`
		TemplateID string        `json:"template_id"`
	}
	if decodeJSON(w, r, &in, 32<<10) != nil {
		return
	}
	if !gptPayRequestID.MatchString(in.ID) {
		writeAPI(w, 400, nil, "请求编号无效")
		return
	}
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	op, e := s.ejhOpenLocked(r.Context(), in.ID, in.Input, false, in.TemplateID)
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	op.Result = nil
	writeAPI(w, 200, op, "")
}
func (s *Server) ejhCardPrivate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	card, secret, err := s.store.GPTPayCard(r.PathValue("id"))
	if err != nil {
		writeAPI(w, 400, nil, "请先刷新该卡的完整信息")
		return
	}
	if card.Source != "ejh" && card.CardType != "ejh" {
		writeAPI(w, 404, nil, "EJH 卡片不存在")
		return
	}
	// Copy saved credentials even for closed or disabled cards, without issuer requests.
	writeAPI(w, 200, secret, "")
}

func (s *Server) ejhCardAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action  string `json:"action"`
		Amount  string `json:"amount"`
		ID      string `json:"request_id"`
		Confirm bool   `json:"confirm"`
	}
	if decodeJSON(w, r, &in, 8<<10) != nil {
		return
	}
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	var card ejh.CardRecord
	if e := s.store.EJHGet("card", r.PathValue("id"), &card); e != nil {
		writeAPI(w, 404, nil, "卡片不存在")
		return
	}
	data := map[string]any{"orderNo": card.Remote.OrderNo}
	// The issuer can reject balance queries after cancellation was submitted.
	// Keep the last snapshot; only card.query.info tracks cancellation progress.
	if card.Remote.Status == "8" && in.Action == "balance" {
		w.Header().Set("Cache-Control", "no-store")
		writeAPI(w, 200, map[string]any{"balance": card.Balance, "cached": true, "processing": true, "at": card.BalanceAt}, "")
		return
	}
	// Read closed cards from saved data without querying the issuer.
	if card.Remote.Status == "2" {
		if e := s.store.ReconcileEJHRejectedIncrease(card.ID); e != nil {
			writeAPI(w, 500, nil, "旧提额操作状态更新失败")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch in.Action {
		case "details":
			writeAPI(w, 200, card.Remote, "")
			return
		case "refresh":
			remote := card.Remote
			remote.Number = maskEJH(remote.Number)
			remote.CVV = ""
			writeAPI(w, 200, remote, "")
			return
		case "balance":
			writeAPI(w, 200, map[string]any{"balance": card.Balance, "cached": true, "at": card.BalanceAt}, "")
			return
		case "sync_trades":
			writeAPI(w, 200, map[string]bool{"cached": true}, "")
			return
		}
	}
	c, k, e := s.ejhConfig()
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	switch in.Action {
	case "sync_trades":
		// Save only a complete collection for this card; the paginated viewer is read-only.
		now := time.Now()
		e = s.collectEJHCardTrades(r.Context(), s.ejhClientLocked(), c, k, card, now.AddDate(0, 0, -90), now)
		if e == nil {
			e = s.store.EJHReconcile()
		}
		if e != nil {
			writeAPI(w, 502, nil, e.Error())
			return
		}
		writeAPI(w, 200, map[string]bool{"synced": true}, "")
		return
	case "details", "refresh":
		var remote ejh.Card
		e = s.ejhClientLocked().Call(r.Context(), c, k, "card.query.info", data, &remote)
		if e == nil && remote.OrderNo != card.Remote.OrderNo {
			e = fmt.Errorf("卡台返回订单号不一致，未更新卡片")
		}
		if e == nil {
			_, e = s.store.ImportEJHCard(remote, false)
		}
		if e != nil {
			writeAPI(w, 502, nil, e.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if in.Action == "refresh" {
			remote.Number = maskEJH(remote.Number)
			remote.CVV = ""
		}
		writeAPI(w, 200, remote, "")
		return
	case "balance":
		var out struct {
			Balance string `json:"balance"`
		}
		e = s.ejhClientLocked().Call(r.Context(), c, k, "card.balance.info", data, &out)
		if e == nil {
			_, e = ejh.Money(strings.TrimPrefix(out.Balance, "-"))
		}
		now := time.Now()
		card.BalanceAttemptAt = &now
		if e == nil {
			card.Balance, card.BalanceAt = out.Balance, &now
			card.BalanceError = ""
		} else {
			card.BalanceError = e.Error()
		}
		if saveErr := s.store.EJHPut("card", card.ID, card); saveErr != nil {
			writeAPI(w, 500, nil, "余额查询记录保存失败")
			return
		}
		if e != nil {
			writeAPI(w, 502, nil, e.Error())
			return
		}
		writeAPI(w, 200, out, "")
		return
	case "increase", "cancel":
	default:
		writeAPI(w, 400, nil, "操作不可用")
		return
	}
	if !in.Confirm || !gptPayRequestID.MatchString(in.ID) {
		writeAPI(w, 400, nil, "资金操作需要确认和请求编号")
		return
	}
	if in.Action == "increase" {
		if e = ejh.Positive(in.Amount); e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
		data["cardAmount"] = in.Amount
	}
	var previous ejh.Operation
	if err := s.store.EJHGet("operation", in.ID, &previous); err == nil {
		if previous.Method != in.Action || previous.CardID != card.ID || previous.Amount != in.Amount {
			writeAPI(w, 409, nil, "请求编号已被其他操作占用")
			return
		}
		previous.Result = nil
		writeAPI(w, 200, previous, "")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeAPI(w, 500, nil, "无法核实既有操作，未提交资金操作")
		return
	}
	ops, e := store.EJHRecords[ejh.Operation](s.store, "operation")
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	for _, o := range ops {
		if o.CardID == card.ID && (o.Status == "unknown" || o.Status == "submitting" || o.Status == "processing") {
			writeAPI(w, 409, nil, "该卡有待确认操作，请先核对，禁止重复提额或销卡")
			return
		}
	}
	op := ejh.Operation{ID: in.ID, Method: in.Action, CardID: card.ID, OrderNo: card.Remote.OrderNo, Amount: in.Amount, Currency: card.Remote.CardCurrency, Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if e = s.store.EJHBeginOperation(op); e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	if in.Action == "cancel" {
		op, e = s.submitEJHCancellationLocked(r.Context(), c, k, op)
		if e != nil {
			writeAPI(w, 500, nil, "销卡已提交，查询记录保存失败，请查看操作记录")
			return
		}
		writeAPI(w, 200, op, "")
		return
	}
	var out struct {
		OrderNo string `json:"orderNo"`
		Status  string `json:"status"`
		Balance string `json:"billAmount"`
	}
	e = s.ejhClientLocked().Call(r.Context(), c, k, "card.change.info", data, &out)
	op.Status = "completed"
	if e != nil {
		op.Status, op.Error = "unknown", e.Error()
		var apiErr *ejh.APIError
		if in.Action == "increase" && errors.As(e, &apiErr) && apiErr.Code == "800006" {
			op.Status = "failed"
		}
	} else if out.OrderNo != card.Remote.OrderNo || (in.Action == "cancel" && out.Status != "SUCCESS") {
		op.Status, op.Error = "unknown", "返回状态或订单号不匹配，请核对"
	} else if in.Action == "increase" {
		if _, e = ejh.Money(out.Balance); e != nil {
			op.Status, op.Error = "unknown", "提额未返回有效余额，请核对"
		}
	} else {
		op.Status, op.Note = "processing", "销卡申请已提交，正在查询卡台处理结果"
	}
	op.UpdatedAt = time.Now()
	if e = s.store.EJHPut("operation", op.ID, op); e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	if e = s.store.EJHPut("card", card.ID, card); e != nil {
		writeAPI(w, 500, nil, "操作记录已保存，但卡片状态保存失败，请刷新卡片后核对")
		return
	}
	writeAPI(w, 200, op, "")
}
func (s *Server) resolveEJHOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderNo  string `json:"order_no"`
		Outcome  string `json:"outcome"`
		Evidence string `json:"evidence"`
	}
	if decodeJSON(w, r, &in, 8<<10) != nil {
		return
	}
	if len(strings.TrimSpace(in.Evidence)) < 3 {
		writeAPI(w, 400, nil, "请填写卡台核实依据")
		return
	}
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	var op ejh.Operation
	if s.store.EJHGet("operation", r.PathValue("id"), &op) != nil {
		writeAPI(w, 404, nil, "操作不存在")
		return
	}
	if op.Status == "completed" || op.Status == "resolved" || op.Status == "failed" {
		writeAPI(w, 409, nil, "操作已结束")
		return
	}
	if op.Method == "open" && in.Outcome == "completed" {
		c, k, e := s.ejhConfig()
		if e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
		if in.OrderNo == "" {
			in.OrderNo = op.OrderNo
		}
		if in.OrderNo == "" {
			writeAPI(w, 400, nil, "请填写真实开卡订单号")
			return
		}
		ops, e := store.EJHRecords[ejh.Operation](s.store, "operation")
		if e != nil {
			writeAPI(w, 500, nil, e.Error())
			return
		}
		for _, other := range ops {
			if other.ID != op.ID && other.Method == "open" && other.OrderNo == in.OrderNo {
				writeAPI(w, 409, nil, "这个开卡订单已属于其他操作，不能重复核销")
				return
			}
		}
		var remote ejh.Card
		if e = s.ejhClientLocked().Call(r.Context(), c, k, "card.query.info", map[string]string{"orderNo": in.OrderNo}, &remote); e != nil {
			writeAPI(w, 502, nil, e.Error())
			return
		}
		if remote.OrderNo != in.OrderNo {
			writeAPI(w, 502, nil, "卡台返回订单号不一致")
			return
		}
		local, e := s.store.ImportEJHCard(remote, true, &op)
		if e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
		op.CardID, op.OrderNo, op.Status = local.ID, remote.OrderNo, "completed"
	} else if in.Outcome == "not_executed" {
		op.Status = "failed"
	} else if in.Outcome == "completed" {
		c, k, e := s.ejhConfig()
		if e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
		var remote ejh.Card
		if e = s.ejhClientLocked().Call(r.Context(), c, k, "card.query.info", map[string]string{"orderNo": op.OrderNo}, &remote); e != nil {
			writeAPI(w, 502, nil, e.Error())
			return
		}
		if remote.OrderNo != op.OrderNo {
			writeAPI(w, 502, nil, "卡台返回订单号不一致，仍保留待核实")
			return
		}
		if op.Method == "cancel" && remote.Status != "2" {
			writeAPI(w, 409, nil, "卡台尚未显示已销卡，仍保留待核实")
			return
		}
		if _, e = s.store.ImportEJHCard(remote, false); e != nil {
			writeAPI(w, 502, nil, e.Error())
			return
		}
		op.Status = "resolved"
	} else {
		writeAPI(w, 400, nil, "请选择实际处理结果")
		return
	}
	op.Note, op.Error, op.UpdatedAt, op.Resolution = in.Evidence, "", time.Now(), in.Outcome
	if e := s.store.EJHPut("operation", op.ID, op); e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	s.notifyEJHOpened(op)
	op.Result = nil
	writeAPI(w, 200, op, "")
}

// Retired endpoints return a clear response to old, still-open browser tabs.
func (s *Server) matchEJHTrade(w http.ResponseWriter, r *http.Request) {
	writeAPI(w, http.StatusGone, nil, "已改为自动关联，请刷新页面")
}
func (s *Server) unmatchEJHTrade(w http.ResponseWriter, r *http.Request) {
	writeAPI(w, http.StatusGone, nil, "已改为自动关联，请刷新页面")
}
func (s *Server) validateEJHAutomation(v model.ProSettings) error {
	if v.ForceMergeEnabled && (v.ForceMergeMinutes < 1 || v.ForceMergeMinutes > 525600 || v.TargetAdminID == "") {
		return fmt.Errorf("超时合并需设置 1～525600 分钟及目标母号")
	}
	if v.CardProvider != "" && v.CardProvider != "ejh" && v.CardProvider != "nexovcc" {
		return fmt.Errorf("请选择有效卡台")
	}
	if v.AutoCardProvider() == "nexovcc" {
		return s.validateNexovccAutomation(v)
	}
	if v.EJHAutoOpen {
		if v.EJHDailyOpenLimit < 1 || v.EJHDailyOpenLimit > 1000 {
			return fmt.Errorf("自动开卡每日数量须为 1～1000")
		}
		if e := ejh.Positive(v.EJHDailyAmountLimit); e != nil {
			return e
		}
		cfg, _, e := s.ejhConfig()
		if e != nil {
			return e
		}
		if !cfg.Enabled {
			return fmt.Errorf("请先启用 EJH 卡台")
		}
		for _, t := range cfg.Templates {
			if t.ID == v.EJHTemplateID && cardplatform.IsOAI(t.Platform) {
				return nil
			}
		}
		return fmt.Errorf("请选择已保存的 EJH 开卡模板")
	}
	return nil
}
func (s *Server) ensureEJHCard(ctx context.Context, admissionChecks ...func() error) (gptpay.Card, error) {
	if err := lockProCardPreparation(ctx, &s.ejh.mu, "EJH 正在同步或处理卡片，本次尚未提交；等待卡台空闲后继续"); err != nil {
		return gptpay.Card{}, err
	}
	defer s.ejh.mu.Unlock()
	pro, _, _, e := s.store.ProSettings()
	if e != nil {
		return gptpay.Card{}, e
	}
	pro, strict, e := s.restockCardSettings(ctx, pro)
	if e != nil {
		return gptpay.Card{}, e
	}
	cards, e := s.automaticProCards(pro, strict)
	if e != nil {
		return gptpay.Card{}, e
	}
	for _, card := range cards {
		if store.RestockCardMatches(card, "ejh", strict) {
			return card, nil
		}
	}
	cards, _, e = s.store.GPTPayCards(int(^uint(0)>>1), 0, false)
	if e != nil {
		return gptpay.Card{}, e
	}
	// An abnormal hold is not evidence that more cards are needed. Prefer any
	// usable existing card above; otherwise stop before spending on issuance.
	for _, card := range cards {
		if card.CardType != "ejh" || !card.Enabled || card.IssuerStatus == "2" {
			continue
		}
		for _, occupant := range card.Occupants {
			if occupant.Deleted || occupant.Status == "submission_unknown" || occupant.Status == "manual_review" {
				reason := "支付结果待核实"
				if occupant.Deleted {
					reason = "账号已删除，但原订单占用尚未解除"
				}
				return gptpay.Card{}, cardPreparationWait("EJH 卡尾号 %s 的账号 %s：%s（订单 %s）；请到 EJH 卡台点击“处理占用”，或在账号回款→未开通中核实后删除该记录，暂停自动补卡；本次未开卡、未付款，原任务等待核实后继续", card.Last4, occupant.Email, reason, occupant.OrderID)
			}
		}
	}
	if !pro.EJHAutoOpen {
		return gptpay.Card{}, fmt.Errorf("无可用银行卡；EJH 自动补卡未启用")
	}
	if e = s.validateEJHAutomation(pro); e != nil {
		return gptpay.Card{}, e
	}
	cfg, _, e := s.ejhConfig()
	if e != nil {
		return gptpay.Card{}, e
	}
	var template ejh.Template
	for _, t := range cfg.Templates {
		if t.ID == pro.EJHTemplateID {
			template = t
		}
	}
	if pro.PurchaseRoute != nil && !pro.PurchaseRoute.MatchesTemplatePlan(template.PlanCode) {
		return gptpay.Card{}, fmt.Errorf("EJH 模板适用套餐已变化，未开卡")
	}
	input := template.Input(time.Now())
	if e = ejh.ValidateOpen(input, time.Now()); e != nil {
		return gptpay.Card{}, e
	}
	ops, e := store.EJHRecords[ejh.Operation](s.store, "operation")
	if e != nil {
		return gptpay.Card{}, e
	}
	count := 0
	total := new(big.Rat)
	zone := time.FixedZone("CST", 8*3600)
	today := time.Now().In(zone).Format("2006-01-02")
	for _, o := range ops {
		if o.Method != "open" {
			continue
		}
		if o.Automatic && o.Status == "failed" && o.Resolution != "not_executed" && o.ErrorCode != "100004" && o.ErrorCode != "800003" && time.Since(o.UpdatedAt) < 5*time.Minute {
			return gptpay.Card{}, cardPreparationWait("自动开卡失败后冷却至 %s，本次未开卡，原任务等待后继续", o.UpdatedAt.Add(5*time.Minute).Format("15:04:05"))
		}
		if o.Automatic && o.Status == "completed" && o.CardID != "" {
			if err := s.store.ReconcileEJHRejectedIncrease(o.CardID); err != nil {
				return gptpay.Card{}, err
			}
			prior, _, err := s.store.GPTPayCard(o.CardID)
			if err != nil {
				return gptpay.Card{}, fmt.Errorf("自动开卡记录对应的银行卡 %s 无法读取，请先核实该卡，暂停自动补卡", o.CardID)
			}
			closed := prior.Source == "ejh" && prior.IssuerStatus == "2"
			closing := prior.Source == "ejh" && prior.IssuerCancelStatus == "processing"
			if (closed || closing) && ((!closing && prior.IssuerLocked) || prior.PendingAccounts > 0 || prior.ActiveAccounts > 0 || len(prior.Occupants) > 0) {
				return gptpay.Card{}, fmt.Errorf("EJH 卡尾号 %s 已进入销卡流程，但仍有未决付款、账号占用或卡片资料待核实，暂停自动补卡", prior.Last4)
			}
			// Confirmed cancellation with no outstanding work permits replacement.
			// It must still reach the daily count and amount accounting below.
			if !prior.DeadAccount && !closed && !closing && prior.OpenedAccounts == 0 && prior.PendingAccounts == 0 && prior.ActiveAccounts == 0 && !prior.Available {
				return gptpay.Card{}, fmt.Errorf("EJH 卡尾号 %s 尚无成功开通记录且当前不可用，请先核实该卡，暂停自动补卡", prior.Last4)
			}
		}
		if o.Method == "open" && (o.Status == "submitting" || o.Status == "unknown" || o.Status == "import_pending") {
			return gptpay.Card{}, cardPreparationWait("存在未核实开卡（%s），先在 EJH 操作记录核对，原任务等待，禁止重复扣款", o.ID)
		}
		if o.Automatic && o.CreatedAt.In(zone).Format("2006-01-02") == today && o.Status != "failed" {
			count++
			if o.Currency != input.CardCurrency {
				return gptpay.Card{}, fmt.Errorf("今日自动开卡预算币种发生变化，请核对后明日继续")
			}
			n, e := ejh.Money(o.Amount)
			if e != nil {
				return gptpay.Card{}, e
			}
			total.Add(total, n)
		}
	}
	n, e := ejh.Money(input.CardAmount)
	if e != nil {
		return gptpay.Card{}, e
	}
	total.Add(total, n)
	cap, e := ejh.Money(pro.EJHDailyAmountLimit)
	if e != nil {
		return gptpay.Card{}, e
	}
	if count >= pro.EJHDailyOpenLimit || total.Cmp(cap) > 0 {
		return gptpay.Card{}, fmt.Errorf("自动开卡达到每日数量或刷卡额度预算上限")
	}
	sum := sha256.Sum256([]byte(time.Now().String()))
	id := fmt.Sprintf("auto-ejh-%x", sum[:12])
	input.Note = "Space " + id
	// Recheck after waiting for other card operations, immediately before issuance.
	if err := ctx.Err(); err != nil {
		return gptpay.Card{}, err
	}
	for _, check := range admissionChecks {
		if err := check(); err != nil {
			return gptpay.Card{}, err
		}
	}
	op, e := s.ejhOpenLocked(ctx, id, input, true, template.ID)
	if e != nil {
		return gptpay.Card{}, e
	}
	if op.Status != "completed" {
		return gptpay.Card{}, fmt.Errorf("自动开卡 %s：%s", op.Status, op.Error)
	}
	card, _, e := s.store.GPTPayCard(op.CardID)
	if e == nil && !card.Available {
		e = fmt.Errorf("开卡已完成，但卡片尚不可用，请刷新卡片核实后继续")
	}
	return card, e
}
