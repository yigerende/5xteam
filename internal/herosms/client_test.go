package herosms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHeroRESTContract(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "ApiKey secret" || r.URL.Query().Get("api_key") != "" {
			t.Error("incorrect authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/activations":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["amount"] != float64(1) || body["service"] != "dr" || body["maxPrice"] != "0.2" {
				t.Errorf("bad body: %v", body)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":11,"phone":"66990000001"}]}`))
		case "POST /api/v1/activations/11/replace":
			_, _ = w.Write([]byte(`{"data":[{"id":"12","phone":"66990000002"}]}`))
		case "GET /api/v1/activations/12/otp/last":
			_, _ = w.Write([]byte(`{"data":{"smsCode":"012345"}}`))
		case "GET /api/v1/activations":
			if r.URL.Query().Get("size") != "25" || r.URL.Query().Get("page") != "2" {
				t.Error("invalid pagination")
			}
			_, _ = w.Write([]byte(`{"data":[],"meta":{"total":25}}`))
		case "DELETE /api/v1/activations/12", "POST /api/v1/activations/12/finish":
			w.WriteHeader(204)
		default:
			t.Error(r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL + "/api/v1", APIKey: "secret", Service: "dr", Country: "52", MaxPrice: "0.2"}
	ctx := context.Background()
	a, err := Acquire(ctx, cfg)
	if err != nil || a.ID.String() != "11" {
		t.Fatalf("acquire: %v %v", a, err)
	}
	a, err = Replace(ctx, cfg, "11")
	if err != nil || a.ID.String() != "12" {
		t.Fatalf("replace: %v %v", a, err)
	}
	code, err := Fetch(ctx, cfg, "12")
	if err != nil || code != "012345" {
		t.Fatalf("otp: %q %v", code, err)
	}
	if _, err = Active(ctx, cfg, 2); err != nil {
		t.Fatal(err)
	}
	for _, finish := range []bool{false, true} {
		if err = Release(ctx, cfg, "12", finish); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 6 {
		t.Fatal(calls)
	}
}

func TestHeroLegacyPurchaseUsesExistingConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/stubs/handler_api.php" || q.Get("action") != "getNumber" || q.Get("service") != "dr" || q.Get("country") != "52" || q.Get("maxPrice") != "0.17" {
			t.Error(r.Method, r.URL.Path, q.Get("action"))
		}
		_, _ = w.Write([]byte("ACCESS_NUMBER:25:66990000025"))
	}))
	defer srv.Close()
	a, err := Acquire(context.Background(), Config{BaseURL: srv.URL + "/stubs/handler_api.php", APIKey: "secret", Service: "dr", Country: "52", MaxPrice: "0.17"})
	if err != nil || a.ID.String() != "25" {
		t.Fatal(a, err)
	}
}

func TestHeroErrorsAndNoMutationRetries(t *testing.T) {
	for _, status := range []int{401, 404, 422, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"title":"EARLY_CANCEL_DENIED secret"}`))
			}))
			defer srv.Close()
			err := Release(context.Background(), Config{BaseURL: srv.URL, APIKey: "secret"}, "11", false)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Status != status || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if apiErr.Detail != `{"title":"EARLY_CANCEL_DENIED [redacted]"}` {
				t.Fatal(apiErr.Detail)
			}
		})
	}
}
