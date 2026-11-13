package httpapi

import (
	"chatgpt-space-merge/internal/store"
	"net/http"
	"sync"
)

func (s *Server) setEJHConsumptionReview(w http.ResponseWriter, r *http.Request) {
	var in store.EJHConsumptionReviewInput
	if decodeJSON(w, r, &in, 8<<10) != nil {
		return
	}
	order, _, err := s.store.GPTPayOrder(r.PathValue("id"))
	if err != nil {
		writeAPI(w, 404, nil, "原开通订单不存在")
		return
	}
	v, _ := s.proLocks.LoadOrStore("account:"+order.Email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		writeAPI(w, 409, nil, "该账号正在处理，请稍后重试")
		return
	}
	defer lock.Unlock()
	if s.proAutomationRunning(order.Email) {
		writeAPI(w, 409, nil, "该账号仍在执行自动流程，请先停止并核实结果")
		return
	}
	row, err := s.store.SetEJHConsumptionReview(order.ID, in)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	writeAPI(w, 200, row, "")
}
