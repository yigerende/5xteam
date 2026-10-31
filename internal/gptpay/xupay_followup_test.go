package gptpay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestXUPayAutomaticRunningAndOriginalCheck(t *testing.T) {
	for _, tc := range []struct{ payment, activation, renewal, want string }{
		{"action_required", "pending", "not_started", "processing"},
		{"succeeded", "review", "review", "processing"},
		{"succeeded", "active", "review", "success"},
		{"succeeded", "active", "disabled", "success"},
		{"failed", "pending", "not_started", "processing"},
	} {
		var v xupayDetail
		v.Order.ID, v.Order.Product, v.Order.Workflow, v.Order.Status = "original", "pro", "subscription", "review"
		v.Order.Activation, v.Order.Renewal = tc.activation, tc.renewal
		v.Payment.State, v.Payment.AutomaticRunning = tc.payment, true
		yes := true
		v.Payment.CanCheck = &yes
		r, err := v.remote("original")
		if err != nil || r.Status != tc.want || !(Order{Provider: XUPay, Status: r.Status, Remote: r}).NeedsStatusPoll() || r.CanCheckXUPay() {
			t.Fatalf("automatic state lost: %+v %v", r, err)
		}
		if tc.renewal == "review" && r.CancellationStatus != "pending" {
			t.Fatal("running renewal became failed")
		}
	}
	r := RemoteOrder{ID: "original", PaymentState: "review"}
	if !r.CanCheckXUPay() {
		t.Fatal("documented absent-field fallback lost")
	}
	no := false
	r.CanCheck = &no
	if r.CanCheckXUPay() {
		t.Fatal("explicit false ignored")
	}
	yes := true
	r.CanCheck = &yes
	r.PaymentState = "succeeded"
	if !r.CanCheckXUPay() {
		t.Fatal("paid original cannot be checked")
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/orders/original/payment/check" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("check attempted a new payment")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 0 {
			t.Error("check sent payment credentials")
		}
		fmt.Fprint(w, `{"accepted":true}`)
	}))
	defer server.Close()
	if err := New().CheckXUPay(context.Background(), server.URL, "key", "original"); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestXUPayRetryAfterAndSharedCooldown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{{"", time.Minute}, {"invalid", time.Minute}, {"0", time.Second}, {"7", 7 * time.Second}, {now.Add(2 * time.Minute).Format(http.TimeFormat), 2 * time.Minute}} {
		if got := xupayRetryDelay(tc.header, now); got != tc.want {
			t.Fatalf("%q: %v != %v", tc.header, got, tc.want)
		}
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"rate limited"}`)
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		_, err := New().StatusForProvider(context.Background(), XUPay, server.URL, "key", []string{"original"})
		if delay := XUPayRetryAfter(err); delay <= 0 || delay > 15*time.Second {
			t.Fatal(err, delay)
		}
	}
	if calls != 1 {
		t.Fatal("same key ignored cooldown", calls)
	}
	_, _ = New().StatusForProvider(context.Background(), XUPay, server.URL, "other-key", []string{"original"})
	if calls != 2 {
		t.Fatal("different credentials blocked")
	}
}

func TestXUPayPublicDiagnosticsBoundedAndRedacted(t *testing.T) {
	var v xupayDetail
	raw := `{"order":{"id":"original","product_id":"pro","workflow":"subscription","status":"failed"},"payment":{"state":"failed","error_code":"amount_changed","message":"报价改变 accessToken=hidden-at"},"events":[{"stage":"payment","message":"核实中 \"sessionToken\":\"hidden-session\" 4242 4242 4242 4242"}]}`
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	for len(v.Events) < 25 {
		v.Events = append(v.Events, v.Events[0])
	}
	r, err := v.remote("original")
	if err != nil || r.ErrorCode != "amount_changed" || len(r.Events) != 20 {
		t.Fatal(r, err)
	}
	var session Session
	_ = json.Unmarshal([]byte(`{"accessToken":"private-at","cookies":"__Secure-next-auth.session-token.0=cookie-part; __Secure-next-auth.session-token.1=cookie-rest"}`), &session)
	r.Events[0].Message += " cookie-part cookie-rest private-at key-value"
	SanitizeXUPayRemote(&r, Snapshot{APIKey: "key-value", Input: CreateInput{Session: session}})
	encoded, _ := json.Marshal(r)
	for _, secret := range []string{"hidden-at", "hidden-session", "4242", "cookie-part", "cookie-rest", "private-at", "key-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("public diagnostics leaked credential", secret)
		}
	}
	if !strings.Contains(string(encoded), "报价改变") || !strings.Contains(string(encoded), "核实中") {
		t.Fatal("public diagnostics lost")
	}
	if len([]rune(xupayPublicText(strings.Repeat("中", 2000)))) > 1001 {
		t.Fatal("unbounded diagnostic")
	}
}
