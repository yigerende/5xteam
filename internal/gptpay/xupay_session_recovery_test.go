package gptpay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestXUPayUnpaidSessionDetail(t *testing.T) {
	const message = "当前 Session 已失效或无法刷新。请重新登录该账号，获取新的完整 Session 后重新提交。本单未提交付款，冻结服务费已释放。"
	for _, tc := range []struct {
		name   string
		change func(*xupayDetail)
		want   bool
	}{
		{"confirmed", nil, true},
		{"no_fee", func(v *xupayDetail) {
			v.Order.Fee = 0
			v.Payment.Message = strings.ReplaceAll(message, "冻结服务费已释放", "无需服务费")
		}, true},
		{"terminal_event", func(v *xupayDetail) { v.Payment.Message = "" }, true},
		{"prior_event_only", func(v *xupayDetail) {
			v.Payment.Message = ""
			v.Events = append(v.Events, v.Events[0])
			v.Events[1].Message = "网络错误"
		}, false},
		{"still_processing", func(v *xupayDetail) { v.Order.Status = "processing" }, false},
		{"active", func(v *xupayDetail) { v.Order.Activation = "active" }, false},
		{"checking", func(v *xupayDetail) { v.Payment.State = "checking" }, false},
		{"paid", func(v *xupayDetail) { v.Payment.State = "succeeded" }, false},
		{"review", func(v *xupayDetail) { v.Order.Status = "review" }, false},
		{"held", func(v *xupayDetail) { v.Payment.FeeState = "held" }, false},
		{"charged", func(v *xupayDetail) { n := int64(100); v.Payment.ChargedAmount = &n }, false},
		{"running", func(v *xupayDetail) { v.Payment.AutomaticRunning = true }, false},
		{"wrong_account", func(v *xupayDetail) { v.Order.Email = "other@example.com" }, false},
		{"missing_cookie", func(v *xupayDetail) {
			v.Payment.Message = strings.Replace(message, "当前 Session 已失效或无法刷新。", "提交的 Session 缺少刷新所需的 sessionToken 或会话 Cookie。", 1)
		}, false},
		{"account_mismatch", func(v *xupayDetail) {
			v.Payment.Message = strings.Replace(message, "当前 Session 已失效或无法刷新。", "提交的 Session 与本单办理账号不一致。", 1)
		}, false},
		{"unconfirmed", func(v *xupayDetail) {
			v.Payment.Message = "当前 Session 已失效或无法刷新。付款结果需要核实，请联系客服处理原订单，不要重新下单或付款。"
		}, false},
		{"generic_401", func(v *xupayDetail) { v.Payment.Message = "HTTP 401" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v xupayDetail
			if err := json.Unmarshal([]byte(`{"order":{"id":"original","email":"test@example.com","workflow":"subscription","product_id":"pro20x","status":"failed","fee":100},"payment":{"state":"failed","fee_state":"released"},"events":[{"stage":"failed"}]}`), &v); err != nil {
				t.Fatal(err)
			}
			v.Payment.Message, v.Events[0].Message = message, message
			if tc.change != nil {
				tc.change(&v)
			}
			r, err := v.remote("original")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			o := Order{Provider: XUPay, Email: "test@example.com", PlanCode: "pro20", Status: r.Status, Remote: r, LastStatusCheckAt: &now}
			if o.CanReloginXUPaySession() != tc.want {
				t.Fatal("incorrect recovery decision", r.Status, r.FailureCategory)
			}
			o.SubmissionUncertain = true
			if o.CanReloginXUPaySession() {
				t.Fatal("ambiguous order accepted")
			}
		})
	}
}
