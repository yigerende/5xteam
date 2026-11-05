package gptregister

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGPTRegisterClientSafetyAndContract(t *testing.T) {
	hits := 0
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; t.Error("followed redirect with credential") }))
	defer redirect.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/open/v1/me":
			http.Redirect(w, r, redirect.URL, http.StatusFound)
		case "/open/v1/products":
			w.Header().Set("Retry-After", "125")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"fixture-secret"}`))
		case "/open/v1/registration-orders":
			if r.Header.Get("Idempotency-Key") != "stable-request" {
				t.Error("missing stable idempotency key")
			}
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"o1","quantity":2,"need_oauth":true,"status":"queued"}`))
		case "/open/v1/accounts":
			if r.URL.Query().Get("order_id") != "order +/?" || r.URL.Query().Get("offset") != "100" {
				t.Error("query not encoded")
			}
			w.Write([]byte(`{"items":[],"total":0}`))
		default:
			w.Write([]byte(`not-json fixture-secret`))
		}
	}))
	defer upstream.Close()
	c, e := New(upstream.URL+"/open/v1/", "fixture-secret")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Me(context.Background()); e == nil || hits != 0 {
		t.Fatal("redirect permitted")
	}
	_, e = c.Products(context.Background())
	var ae *APIError
	if !errors.As(e, &ae) || ae.Status != 429 || ae.RetryAfter != 125 || strings.Contains(e.Error(), "fixture-secret") {
		t.Fatal("unsafe error/ratelimit", e)
	}
	if o, e := c.Create(context.Background(), "stable-request", 2, true); e != nil || o.ID != "o1" {
		t.Fatal(o, e)
	}
	if _, e = c.Accounts(context.Background(), "order +/?", 100); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Credentials(context.Background(), "a1"); e == nil || strings.Contains(e.Error(), "fixture-secret") {
		t.Fatal("raw parse error leaked")
	}
	for _, raw := range []string{"file:///etc/passwd", "https://u:p@example.com", "https://example.com?k=secret", "https://example.com/#x", "javascript:alert(1)"} {
		if _, e := NormalizeURL(raw); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
