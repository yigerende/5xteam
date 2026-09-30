package gptpay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCMSNavProtocolAndSession(t *testing.T) {
	for _, plan := range []string{"pro5", "pro20", "pro50"} {
		t.Run(plan, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("X-API-Key") != "" {
					t.Error("wrong authentication")
				}
				switch r.URL.Path {
				case "/customer/recharges":
					if r.Method != "POST" || r.Header.Get("Idempotency-Key") != "original-payment-id" {
						t.Error("wrong create method/idempotency")
					}
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if string(body["productCode"]) != `"`+plan+`x"` || string(body["country"]) != `"PH"` || string(body["cancelRenewal"]) != "true" || len(body) != 5 {
						t.Error("wrong CMSNav request shape")
					}
					var card CardSecret
					_ = json.Unmarshal(body["card"], &card)
					if card.CVV != "012" || card.Month != 12 || card.Year != 2099 || card.Number != "4242424242424242" {
						t.Error("wrong nested card")
					}
					var session Session
					_ = json.Unmarshal(body["session"], &session)
					if session.User.Email != "fixture@example.com" || session.Account.ID != "personal" || session.AccessToken != "fixture-at" || !strings.Contains(string(session.Raw), `"expires"`) || !strings.Contains(string(session.Raw), `"name":"Fixture"`) {
						t.Error("full web session lost")
					}
					for _, secret := range []string{"private-cookie", "private-rt", "private-id"} {
						if strings.Contains(string(body["session"]), secret) {
							t.Error("sent unnecessary credential")
						}
					}
					fmt.Fprint(w, `{"code":0,"data":{"taskId":"remote-task","status":"reserved","order":{"status":"processing"}}}`)
				case "/customer/orders/remote-task":
					if r.Method != "GET" {
						t.Error("wrong status method")
					}
					fmt.Fprint(w, `{"code":0,"data":{"order":{"status":"processing","planCode":"pro50x","creditCost":100,"creditStatus":"charged"},"progress":{"status":"failed","rechargeStatus":"success","renewalStatus":"failed"}}}`)
				case "/customer/wallet":
					fmt.Fprint(w, `{"code":0,"data":{"balance":120,"held":80,"prices":{"pro5x":80,"pro20x":100,"pro50x":100},"membership":{"level":"basic","name":"普通"}}}`)
				default:
					t.Error("unexpected endpoint")
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var session Session
			if err := json.Unmarshal([]byte(`{"user":{"email":"fixture@example.com","name":"Fixture"},"account":{"id":"personal","planType":"free"},"accessToken":"fixture-at","expires":"2099-01-01T00:00:00Z","session_token":"private-cookie","refresh_token":"private-rt","idToken":"private-id"}`), &session); err != nil {
				t.Fatal(err)
			}
			snap := Snapshot{Provider: CMSNav, Country: "PH", URL: srv.URL, APIKey: "fixture-key", Input: CreateInput{PlanCode: plan, Session: session, CardSecret: CardSecret{Number: "4242424242424242", Month: 12, Year: 2099, CVV: "012"}}}
			client := New()
			for attempt := 0; attempt < 2; attempt++ {
				r, _, err := client.Create(context.Background(), snap, "original-payment-id")
				if err != nil || r.ID != "remote-task" || r.Status != "processing" {
					t.Fatalf("create: %+v %v", r, err)
				}
			}
			rows, err := client.StatusForProvider(context.Background(), CMSNav, srv.URL, "fixture-key", []string{"remote-task"})
			if err != nil || len(rows) != 1 || rows[0].ID != "remote-task" || rows[0].Status != "success" || rows[0].CancellationStatus != "failed" || rows[0].ChargedCredits != 100 {
				t.Fatalf("status: %+v %v", rows, err)
			}
			a, err := client.AccountForProvider(context.Background(), CMSNav, srv.URL, "fixture-key")
			if err != nil || a.Wallet.AvailableCredits != 120 || a.Wallet.ReservedCredits != 80 || len(a.Plans) != 3 || a.Level.Name != "普通" || calls != 4 {
				t.Fatalf("account: %+v %v", a, err)
			}
		})
	}
}

func TestCMSNavPaymentFailures(t *testing.T) {
	for _, tc := range []struct {
		code      int
		body      string
		uncertain bool
	}{
		{400, `{"errorCode":"INSUFFICIENT_CREDITS","error":"secret"}`, false},
		{400, `{"errorCode":"SESSION_INVALID"}`, false},
		{401, `{"errorCode":"API_KEY_INVALID"}`, false},
		{503, `{"errorCode":"CHANNEL_UNAVAILABLE"}`, true},
		{409, `{"errorCode":"IDEMPOTENCY_CONFLICT"}`, true},
		{200, `<html>secret</html>`, true},
		{200, `{"code":0,"data":{}}`, true},
		{200, `{"code":0,"data":{"taskId":"id","status":"unknown-state"}}`, true},
	} {
		t.Run(fmt.Sprint(tc.code, tc.body), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			_, _, err := New().Create(context.Background(), Snapshot{Provider: CMSNav, URL: srv.URL, Input: CreateInput{PlanCode: "pro50"}}, "original-idempotency-key")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Uncertain != tc.uncertain || strings.Contains(err.Error(), "secret") {
				t.Fatalf("classification: %v", err)
			}
		})
	}
}

func TestCMSNavStatusConcurrencyAndNotFound(t *testing.T) {
	var active, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if p >= n || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "missing") {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"errorCode":"ORDER_NOT_FOUND"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"order":{"status":"success"}}}`)
	}))
	defer srv.Close()
	ids := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "missing"}
	rows, err := New().StatusForProvider(context.Background(), CMSNav, srv.URL, "key", ids)
	if err != nil || len(rows) != len(ids) || peak.Load() > 4 || peak.Load() < 2 || rows[9].Status != "not_found" {
		t.Fatalf("bounded lookup: %v peak %d", err, peak.Load())
	}
	for i := range rows {
		if rows[i].ID != ids[i] {
			t.Fatal("lookup correlation lost")
		}
	}
}

func TestCMSNavPlanValidation(t *testing.T) {
	cfg, err := NormalizeSettings(Settings{Provider: CMSNav, PlanCode: "pro50", Country: "ph"})
	if err != nil || cfg.URL != CMSNavURL || cfg.Country != "PH" {
		t.Fatal(cfg, err)
	}
	for _, cfg := range []Settings{{PlanCode: "pro50"}, {Provider: "other"}, {Provider: CMSNav, Country: "USA"}} {
		if _, err := NormalizeSettings(cfg); err == nil {
			t.Fatal("accepted unsupported config")
		}
	}
}
