package gptpay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXUPayPythonProductSnapshotAndLegacyReplay(t *testing.T) {
	for _, tc := range []struct {
		name, plan, product string
		legacy              bool
	}{
		{"new_5x", "pro5", "pro5x", false},
		{"new_20x", "pro20", "pro20x", false},
		{"old_20x_replay", "pro20", "pro", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["product_id"] != tc.product || r.Header.Get("Idempotency-Key") != "unchanged-order-id" {
					t.Error("wrong wire product or changed replay identity")
				}
				fmt.Fprint(w, `{"id":"original","status":"queued"}`)
			}))
			defer server.Close()
			s := NewSnapshot(Settings{Provider: XUPay, URL: server.URL, PlanCode: tc.plan, Country: "PH", TermsAccepted: true}, "key", CardSecret{}, Session{})
			if tc.legacy {
				s.XUPayProductID = ""
			}
			raw, _ := json.Marshal(s)
			var saved Snapshot
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, _, err := New().Create(context.Background(), saved, "unchanged-order-id"); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}

func TestXUPayPythonOrderStatusWithoutLegacyCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, status, payment, activation, renewal, want string
		poll                                             bool
	}{
		{"queued", "queued", "preparing", "not_checked", "not_started", "processing", true},
		{"processing", "processing", "preparing", "pending", "pending", "processing", true},
		{"in_progress_review", "processing", "review", "pending", "review", "processing", true},
		{"payment_unknown", "review", "review", "review", "not_started", "manual_review", false},
		{"paid_activation_unknown", "succeeded", "succeeded", "review", "review", "success", false},
		{"paid_activation_pending", "succeeded", "succeeded", "pending", "pending", "success", true},
		{"paid_activation_missing", "review", "succeeded", "", "review", "success", false},
		{"paid_followup_running", "processing", "succeeded", "review", "pending", "success", true},
		{"paid_followup_failed", "failed", "succeeded", "review", "review", "success", false},
		{"order_success_without_paid", "succeeded", "review", "active", "disabled", "manual_review", false},
		{"renewal_failed", "succeeded", "succeeded", "active", "review", "success", false},
		{"renewal_disabled", "succeeded", "succeeded", "active", "disabled", "success", false},
		{"renewal_kept", "succeeded", "succeeded", "active", "kept", "success", false},
		{"failed", "failed", "failed", "not_checked", "not_started", "failed", false},
		{"cancelled", "cancelled", "cancelled", "not_checked", "not_started", "failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v xupayDetail
			raw := fmt.Sprintf(`{"order":{"id":"python-order","workflow":"subscription","product_id":"pro20x","status":%q,"activation":%q,"renewal":%q},"payment":{"state":%q}}`, tc.status, tc.activation, tc.renewal, tc.payment)
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatal(err)
			}
			r, err := v.remote("python-order")
			o := Order{Provider: XUPay, Status: r.Status, Remote: r}
			if err != nil || r.PlanCode != "pro20" || r.Status != tc.want || o.NeedsStatusPoll() != tc.poll || r.CanCheckXUPay() {
				t.Fatalf("unexpected Python state: %+v %v", r, err)
			}
			if tc.payment == "succeeded" && !o.RetryBlocked() {
				t.Fatal("confirmed payment allowed resubmission")
			}
			if tc.payment == "succeeded" && tc.activation != "active" && (r.ActivationStatus != tc.activation || !strings.Contains(r.RetryHint, "订阅核实尚未完成") || r.FailureReason != "") {
				t.Fatal("activation diagnostics lost or blocked a confirmed payment")
			}
			if tc.name == "renewal_failed" && (r.CancellationStatus != "failed" || r.RetryHint == "") {
				t.Fatal("missing renewal failure diagnostics")
			}
		})
	}
}

func TestXUPayPythonValidationErrorsRemainPrivateAndConservative(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, message, code string
		status                            int
		uncertain                         bool
	}{
		{"balance", "POST", "/orders", "可用余额不足，请充值后再试", "INSUFFICIENT_CREDITS", 400, false},
		{"session", "POST", "/orders", "Session 已过期，请重新获取", "SESSION_EXPIRED", 400, false},
		{"session_invalid", "POST", "/orders", "Session 空间信息不一致", "SESSION_INVALID", 400, false},
		{"card", "POST", "/orders", "请提供有效的银行卡信息", "CARD_INVALID", 400, false},
		{"parameters", "POST", "/orders", "请检查参数类型、必填项和取值范围：private-token card.cvc", "PARAMETERS_INVALID", 400, false},
		{"missing_api", "POST", "/orders", "接口不存在", "API_UNAVAILABLE", 404, true},
		{"missing_order", "GET", "/orders/original", "订单不存在", "ORDER_NOT_FOUND", 404, false},
		{"missing_product", "POST", "/orders", "套餐不存在或已下架", "PRODUCT_UNAVAILABLE", 404, true},
		{"conflict", "POST", "/orders", "同一 Idempotency-Key 不能用于不同请求", "ORDER_CONFLICT", 409, true},
		{"billing", "POST", "/orders", "暂无可用账单资料，请联系管理员", "BILLING_UNAVAILABLE", 503, true},
		{"proxy", "POST", "/orders", "无可用代理，请检查代理配置", "PROXY_UNAVAILABLE", 503, true},
		{"disabled", "POST", "/orders", "订阅办理暂未开放", "SUBSCRIPTION_DISABLED", 403, false},
		{"unknown_secret", "POST", "/orders", "private-token 4242424242424242 sessionToken=secret", "REQUEST_REJECTED", 400, false},
		{"server_secret", "POST", "/orders", "private-token 4242424242424242", "INVALID_RESPONSE", 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.message})
			}))
			defer server.Close()
			var out map[string]any
			err := New().xupayCall(context.Background(), server.URL, "key", tc.method, tc.path, "same-order-id", nil, &out)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.ErrorCode != tc.code || apiErr.Uncertain != tc.uncertain {
				t.Fatalf("wrong classification or lost submission fence: %+v", err)
			}
			for _, secret := range []string{"private-token", "4242424242424242", "sessionToken=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("upstream error leaked credentials")
				}
			}
		})
	}
}
