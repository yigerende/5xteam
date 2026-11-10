package refundmerchant

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStarBridgeContract(t *testing.T) {
	response := `{"ids":["order-1"],"replayed":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("wrong key")
		}
		if r.Method == "POST" && (r.URL.Path != "/api/v1/refund/orders" || r.Header.Get("Idempotency-Key") != "space-test-request") {
			t.Error("wrong submit endpoint or idempotency")
		}
		if r.Method == "GET" && r.URL.Path != "/api/v1/refund/orders/order-1" {
			t.Error("wrong query endpoint")
		}
		fmt.Fprint(w, response)
	}))
	defer srv.Close()
	c := Client{Provider: ProviderStarBridge, SubmitURL: srv.URL + "/api/v1/refund/orders"}
	r, err := c.Submit(t.Context(), "fixture-key", "space-test-request", []byte(`{"authorized":true,"accounts":[]}`), "test@example.com")
	if err != nil || r.JobID != "order-1" || r.BatchID != "" {
		t.Fatal("receipt", r, err)
	}
	for _, invalid := range []string{`{"ids":[]}`, `{"ids":["one","two"]}`, `{"id":"one"}`, `{"batch_id":"batch"}`, `{"ids":["../other"]}`} {
		response = invalid
		if _, err = c.Submit(t.Context(), "fixture-key", "space-test-request", nil, "test@example.com"); err == nil {
			t.Fatal("accepted invalid receipt", invalid)
		}
	}
	for _, status := range []string{"queued", "processing", "verifying", "attention", "succeeded", "failed", "cancelled", "banned", "unknown"} {
		response = fmt.Sprintf(`{"order":{"id":"order-1","email":"TEST@example.com","status":%q,"fee":5000,"fee_status":"frozen"},"events":[]}`, status)
		job, err := c.Progress(t.Context(), "fixture-key", "order-1", "test@example.com")
		if err != nil || job.Succeeded() != (status == "succeeded") || job.TerminalFailure() != (status == "failed" || status == "cancelled" || status == "banned") {
			t.Fatal(status, job, err)
		}
	}
	for _, invalid := range []string{
		`{"id":"order-1","email":"test@example.com","status":"succeeded"}`,
		`{"order":{"id":"different","email":"test@example.com","status":"succeeded"}}`,
		`{"order":{"id":"order-1","email":"other@example.com","status":"succeeded"}}`,
		`{"order":{"id":"order-1","email":"test@example.com"}}`,
	} {
		response = invalid
		if _, err = c.Progress(t.Context(), "fixture-key", "order-1", "test@example.com"); err == nil {
			t.Fatal("accepted mismatched progress", invalid)
		}
	}
	if u, err := NormalizeProviderURL(ProviderStarBridge, ""); err != nil || u != StarBridgeSubmitURL {
		t.Fatal(u, err)
	}
	if _, err := NormalizeProviderURL("unknown", srv.URL); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
