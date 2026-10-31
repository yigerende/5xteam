package httpapi

import (
	"chatgpt-space-merge/internal/gptpay"
	"errors"
	"net/http"
	"time"
)

// Recheck the original payment; this route never creates an order or submits a card.
func (s *Server) checkXUPayOrder(w http.ResponseWriter, r *http.Request) {
	o, _, err := s.store.GPTPayOrder(r.PathValue("id"))
	if err != nil {
		writeAPI(w, 404, nil, "订单不存在")
		return
	}
	if s.proAutomationRunning(o.Email) {
		writeAPI(w, 409, nil, "全自动正在跟踪此订单，请等待")
		return
	}
	unlock := s.lockProAccount(o.Email)
	defer unlock()
	o, snapshot, err := s.store.GPTPayOrder(o.ID)
	if err != nil {
		writeAPI(w, 500, nil, "读取订单失败")
		return
	}
	if gptpay.Provider(snapshot.Provider) != gptpay.XUPay || o.Remote.ID == "" {
		writeAPI(w, 409, nil, "仅支持已有序付订单")
		return
	}
	if o.NextStatusCheckAt != nil && o.NextStatusCheckAt.After(time.Now()) && (o.CheckPending || o.Remote.AutomaticRunning || o.StatusCheckError != "") {
		writeAPI(w, 409, nil, "原订单正在处理或等待查询，请稍后核实")
		return
	}
	o, err = s.syncGPTPayOrderStatus(r.Context(), o, snapshot)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	if !o.Remote.CanCheckXUPay() {
		writeAPI(w, 409, nil, "序付当前不可核实原单，请查看最新订单状态")
		return
	}
	// Persist intent before I/O. A lost response or restart must first query
	// this order, not send another check request based on stale can_check.
	o.CheckPending = true
	nextCheck := time.Now().UTC().Add(time.Minute)
	o.NextStatusCheckAt = &nextCheck
	if err = s.store.UpdateGPTPayOrder(o); err != nil {
		writeAPI(w, 500, nil, "保存核实标记失败，未发送请求")
		return
	}
	err = s.gptPayClient().CheckXUPay(r.Context(), snapshot.URL, snapshot.APIKey, o.Remote.ID)
	if err != nil {
		var apiErr *gptpay.APIError
		if errors.As(err, &apiErr) && !apiErr.Uncertain {
			o.CheckPending = false
		}
		o.StatusCheckError = gptpay.Redact(err.Error(), snapshot)
		if delay := gptpay.XUPayRetryAfter(err); delay > 0 {
			next := time.Now().UTC().Add(delay)
			o.NextStatusCheckAt = &next
		}
		if saveErr := s.store.UpdateGPTPayOrder(o); saveErr != nil {
			writeAPI(w, 500, nil, "核实响应保存失败，请稍后查询原订单")
			return
		}
		writeAPI(w, 502, nil, o.StatusCheckError)
		return
	}
	// Accepted is not success. Track the same order, preserving known payment evidence.
	o.Remote.AutomaticRunning = true
	o.CheckPending = false
	no := false
	o.Remote.CanCheck = &no
	o.Remote.Message = "原订单核实已受理，等待序付返回结果"
	if o.Status != "success" {
		o.Status = "processing"
		o.Remote.Status = "processing"
	}
	if o.Remote.CancellationStatus == "failed" {
		o.Remote.CancellationStatus = "pending"
	}
	o.Error, o.Remote.FailureReason, o.StatusCheckError = "", "", ""
	now := time.Now().UTC()
	next := now.Add(5 * time.Second)
	o.UpdatedAt = now
	o.NextStatusCheckAt = &next
	if err = s.store.UpdateGPTPayOrder(o); err != nil {
		writeAPI(w, 500, nil, "核实已受理但本地保存失败，请查询原订单")
		return
	}
	s.auditGPTPayOrder(o)
	writeAPI(w, 202, o, "")
}
