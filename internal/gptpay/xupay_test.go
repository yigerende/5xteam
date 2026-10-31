package gptpay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXUPayCreateContractAndReplay(t *testing.T) {
	var first string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/orders" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("Idempotency-Key") != "same-order-id" || r.Header.Get("X-API-Key") != "" {
			t.Error("incorrect subscription route/credentials/idempotency")
		}
		raw, _ := io.ReadAll(r.Body)
		if calls == 1 {
			first = string(raw)
		} else if first != string(raw) {
			t.Error("changed replay payload")
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["product_id"] != "pro" || body["country"] != "PH" || body["workflow"] != "subscription" || body["accepted"] != true || body["keep_renewal"] != false || body["seats"] != float64(1) {
			t.Errorf("incorrect subscription contract")
		}
		card := body["card"].(map[string]any)
		if card["number"] != "4242424242424242" || card["month"] != "12" || card["year"] != "2035" || card["cvc"] != "123" {
			t.Error("card serialization mismatch")
		}
		session := body["session"].(map[string]any)
		if session["sessionToken"] != "fixture-session" || session["expires"] != "2099-01-01T00:00:00Z" || session["refresh_token"] != nil || session["refreshToken"] != nil || body["billing"] != nil {
			t.Error("session or billing contract mismatch")
		}
		if calls == 1 {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"fixture-key 4242424242424242 upstream secret"}`)
			return
		}
		fmt.Fprint(w, `{"id":"remote-123","status":"succeeded","replayed":true}`)
	}))
	defer server.Close()
	var session Session
	_ = json.Unmarshal([]byte(`{"accessToken":"fixture-at","sessionToken":"fixture-session","refresh_token":"private-rt","refreshToken":"private-rt","user":{"email":"fixture@example.com"},"account":{"id":"personal"},"expires":"2099-01-01T00:00:00Z"}`), &session)
	snap := NewSnapshot(Settings{Provider: XUPay, URL: server.URL + "/api/v1", PlanCode: "pro20", Country: "PH", TermsAccepted: true}, "fixture-key", CardSecret{Number: "4242424242424242", Month: 12, Year: 2035, CVV: "123"}, session)
	_, _, err := New().Create(context.Background(), snap, "same-order-id")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Uncertain || strings.Contains(err.Error(), "fixture-key") || strings.Contains(err.Error(), "4242") {
		t.Fatal("uncertain submission or redaction failed", err)
	}
	encoded, _ := json.Marshal(snap)
	var replay Snapshot
	_ = json.Unmarshal(encoded, &replay)
	r, _, err := New().Create(context.Background(), replay, "same-order-id")
	if err != nil || r.ID != "remote-123" || r.Status != "processing" {
		t.Fatal("create receipt must wait for payment detail", r, err)
	}
}

func TestXUPayPaymentActivationAndRenewal(t *testing.T) {
	for _, tc := range []struct{ name, payment, activation, renewal, order, status, cancel string }{
		{"queued", "preparing", "pending", "not_started", "queued", "processing", "waiting"},
		{"payment_unknown", "checking", "pending", "not_started", "processing", "processing", "waiting"},
		{"paid_pending", "succeeded", "pending", "pending", "succeeded", "processing", "pending"},
		{"paid_review", "succeeded", "review", "review", "succeeded", "manual_review", "failed"},
		{"legacy_paid", "succeeded", "not_checked", "disabled", "succeeded", "manual_review", "success"},
		{"done", "succeeded", "active", "disabled", "succeeded", "success", "success"},
		{"renewal_failed", "succeeded", "active", "review", "succeeded", "success", "failed"},
		{"renewal_running", "succeeded", "active", "retrying", "processing", "success", "pending"},
		{"keep", "succeeded", "active", "kept", "succeeded", "success", "not_applicable"},
		{"verification", "action_required", "pending", "not_started", "review", "manual_review", "waiting"},
		{"unknown", "review", "pending", "not_started", "review", "manual_review", "waiting"},
		{"failed", "failed", "pending", "not_started", "failed", "failed", "not_applicable"},
		{"cancelled", "cancelled", "pending", "not_started", "cancelled", "failed", "not_applicable"},
		{"contradiction", "failed", "active", "disabled", "succeeded", "manual_review", "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v xupayDetail
			v.Order.ID, v.Order.Product, v.Order.Workflow, v.Order.Status = "order-1", "pro5x", "subscription", tc.order
			v.Order.Activation, v.Order.Renewal, v.Payment.State = tc.activation, tc.renewal, tc.payment
			v.Order.Fee = 123
			v.Payment.FeeState = "released"
			if tc.payment == "succeeded" {
				v.Payment.FeeState = "charged"
			}
			amount := int64(20000)
			v.Payment.ChargedAmount, v.Payment.ChargedCurrency = &amount, "USD"
			r, err := v.remote("order-1")
			if err != nil || r.Status != tc.status || r.CancellationStatus != tc.cancel || r.ChargedCredits+r.ReleasedCredits != 123 || *r.PaymentAmountMinor != 20000 {
				t.Fatal(r, err)
			}
			o := Order{Provider: XUPay, Status: r.Status, Remote: r}
			if (tc.payment == "succeeded" || tc.status == "manual_review") && !o.RetryBlocked() {
				t.Fatal("must prevent a second payment")
			}
			if tc.name == "renewal_running" && !o.NeedsStatusPoll() {
				t.Fatal("must continue renewal status reads")
			}
		})
	}
	var empty xupayDetail
	if _, err := empty.remote("order-1"); err == nil {
		t.Fatal("missing detail accepted")
	}
	var yen xupayDetail
	yen.Order.ID, yen.Order.Product, yen.Order.Workflow, yen.Order.Status = "yen-1", "pro", "subscription", "succeeded"
	yen.Order.Activation, yen.Payment.State, yen.Payment.Currency = "active", "succeeded", "JPY"
	minor := int64(30000)
	yen.Payment.Amount = &minor
	r, err := yen.remote("yen-1")
	if err != nil || r.PaymentAmount != "30000" || *r.PaymentAmountMinor != 30000 {
		t.Fatal("JPY minor units broke card reconciliation", r, err)
	}
}

func TestXUPayWalletCatalogStatusAndErrorPrivacy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer key" {
			t.Error("read credentials/method")
		}
		switch r.URL.Path {
		case "/api/v1/wallet":
			fmt.Fprint(w, `{"balance":12345,"frozen":500,"unit":"cent_R","membership":{"current":{"code":"L2","name":"银阶"}}}`)
		case "/api/v1/catalog":
			fmt.Fprint(w, `{"unit":"cent_R","payment_connected":true,"countries":[{"code":"PH","name":"菲律宾","currency":"PHP"}],"items":[{"id":"pro5x","name":"Pro 5x","member_fee":123},{"id":"pro","name":"Pro 20x","member_fee":456},{"id":"team","member_fee":789}]}`)
		case "/api/v1/orders/order-1":
			fmt.Fprint(w, `{"order":{"id":"order-1","product_id":"pro","workflow":"subscription","status":"succeeded","activation":"active","renewal":"disabled","fee":456},"payment":{"state":"succeeded","fee_state":"charged","amount":20000,"currency":"USD"},"events":[{"stage":"payment","message":"付款核实中 accessToken=DO-NOT-EXPOSE-raw-secret"}]}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"DO-NOT-EXPOSE-raw-secret"}`)
		}
	}))
	defer server.Close()
	c := New()
	a, err := c.AccountForProvider(context.Background(), XUPay, server.URL+"/api/v1", "key")
	if err != nil || a.Wallet.AvailableCredits != 12345 || a.Level.Code != "L2" || len(a.Plans) != 2 || a.Plans[1].PlanCode != "pro20" || a.CreditUnit != "cent_R" {
		t.Fatal(a, err)
	}
	r, err := c.StatusForProvider(context.Background(), XUPay, server.URL+"/api/v1", "key", []string{"order-1"})
	if err != nil || len(r) != 1 || r[0].Status != "success" {
		t.Fatal(r, err)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "DO-NOT-EXPOSE") {
		t.Fatal("raw event exposed")
	}
	_, err = c.StatusForProvider(context.Background(), XUPay, server.URL+"/api/v1", "key", []string{"missing"})
	if err == nil || strings.Contains(err.Error(), "DO-NOT-EXPOSE") {
		t.Fatal("error redaction")
	}
	if _, err = c.StatusForProvider(context.Background(), XUPay, server.URL+"/api/v1", "key", []string{"../wallet"}); err == nil {
		t.Fatal("invalid ID accepted")
	}
}

func TestXUPaySettingsAndPreflight(t *testing.T) {
	cfg, err := NormalizeSettings(Settings{Provider: XUPay})
	if err != nil || cfg.URL != XUPayURL || cfg.Country != "PH" || ValidatePurchase(cfg) == nil {
		t.Fatal(cfg, err)
	}
	cfg.URL = "http://104.160.172.162:5496/"
	cfg.TermsAccepted = true
	if v, err := NormalizeSettings(cfg); err != nil || v.URL != XUPayURL || ValidatePurchase(v) != nil {
		t.Fatal(v, err)
	}
	for _, tc := range []Settings{
		{Provider: XUPay, PlanCode: "pro50"}, {Provider: XUPay, Country: "CL"},
		{Provider: XUPay, URL: "http://localhost/api/v1/orders"}, {Provider: XUPay, URL: "http://user:pass@localhost"},
	} {
		if _, err := NormalizeSettings(tc); err == nil {
			t.Fatal("invalid settings accepted", tc)
		}
	}
}

func TestXUPayRefreshCredentialAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"at_only", `{"accessToken":"fixture-at"}`, false},
		{"oauth_refresh_only", `{"refresh_token":"fixture-oauth-rt"}`, false},
		{"session_token", `{"sessionToken":"fixture-web-session"}`, true},
		{"saved_login_alias", `{"session_token":"fixture-web-session"}`, true},
		{"cookie", `{"cookies":"__Secure-platform.session-token=fixture-cookie"}`, true},
		{"chunked_cookie", `{"cookies":"__Secure-next-auth.session-token.0=part-one; __Secure-next-auth.session-token.1=part-two"}`, true},
		{"unrelated_cookie", `{"cookies":"analytics=fixture-cookie"}`, false},
		{"empty_cookie", `{"cookies":"__Secure-next-auth.session-token="}`, false},
		{"invalid_token", `{"sessionToken":"invalid\r\nvalue"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var session Session
			if err := json.Unmarshal([]byte(tc.raw), &session); err != nil {
				t.Fatal(err)
			}
			if got := XUPayRefreshable(session); got != tc.want {
				t.Fatalf("refreshable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestXUPayCurrencyCasePreservesMinorUnits(t *testing.T) {
	for _, currency := range []string{"jpy", "JPY", "php", "PHP"} {
		for _, charged := range []bool{false, true} {
			v := xupayDetail{}
			v.Order.ID, v.Order.Product, v.Order.Workflow, v.Order.Status = "order-currency", "pro", "subscription", "succeeded"
			v.Order.Activation, v.Payment.State = "active", "succeeded"
			amount := int64(30000)
			v.Payment.Amount, v.Payment.Currency = &amount, currency
			if charged {
				v.Payment.ChargedAmount, v.Payment.ChargedCurrency = &amount, currency
			}
			r, err := v.remote(v.Order.ID)
			want := "300.00"
			if strings.EqualFold(currency, "JPY") {
				want = "30000"
			}
			if err != nil || r.PaymentAmount != want || r.PaymentCurrency != strings.ToUpper(currency) || *r.PaymentAmountMinor != amount {
				t.Fatalf("currency=%s charged=%v: %+v, %v", currency, charged, r, err)
			}
		}
	}
}
