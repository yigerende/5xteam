package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/store"
	"context"
	"crypto/des"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ejhFixtureTransport(t *testing.T, response func(string, map[string]any) any) *http.Client {
	t.Helper()
	return &http.Client{Transport: ejhBalanceTransport(func(r *http.Request) (*http.Response, error) {
		var data any
		if r.URL.Path == "/api/v3/authenticate" {
			data = map[string]any{"authToken": "fixture-token", "validity": 3600000}
		} else {
			var envelope map[string]string
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			encrypted, err := base64.StdEncoding.DecodeString(envelope["param"])
			if err != nil || len(encrypted) == 0 || len(encrypted)%8 != 0 {
				t.Fatal("bad encrypted request")
			}
			block, _ := des.NewCipher([]byte("fixture-"))
			plain := make([]byte, len(encrypted))
			for i := 0; i < len(plain); i += 8 {
				block.Decrypt(plain[i:i+8], encrypted[i:i+8])
			}
			var body struct {
				Method string
				Data   map[string]any
			}
			if err = json.Unmarshal(plain[:len(plain)-int(plain[len(plain)-1])], &body); err != nil {
				t.Fatal(err)
			}
			data = response(body.Method, body.Data)
		}
		envelope := map[string]any{"code": "000000", "data": data}
		if apiErr, ok := data.(*ejh.APIError); ok {
			envelope = map[string]any{"code": apiErr.Code, "msg": apiErr.Message}
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
	})}
}

func TestEJHClosedReadsAndRefreshSkipIssuer(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	remote := ejh.Card{OrderNo: "closed", Number: "4242424242424242", CVV: "123", Validity: "1299", Status: "2", CardCurrency: "PHP"}
	closed, err := s.store.ImportEJHCard(remote, false)
	if err != nil {
		t.Fatal(err)
	}
	var before ejh.CardRecord
	if err = s.store.EJHGet("card", closed.ID, &before); err != nil {
		t.Fatal(err)
	}
	active := remote
	active.OrderNo = "active"
	active.Number = "5555555555554444"
	active.Status = "3"
	closing := remote
	closing.OrderNo = "closing"
	closing.Number = "5555555555555555"
	closing.Status = "8"
	if _, err = s.store.ImportEJHCard(active, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.ImportEJHCard(closing, false); err != nil {
		t.Fatal(err)
	}
	trade := ejh.Trade{OrderNo: "closed", TradeNo: "saved", Type: "5", Status: "2", CardAmount: "10", CardCurrency: "PHP", Number: remote.Number, TradeTime: "2026-10-15T12:00:00+08:00"}
	if err = s.store.EJHSaveTrade(closed.ID, trade); err != nil {
		t.Fatal(err)
	}
	refund := ejh.Trade{OrderNo: "closed", TradeNo: "closure", Type: "17", Status: "2", TradeAmount: "10", TradeCurrency: "PHP", BillAmount: "0.16", BillCurrency: "USD", TradeTime: trade.TradeTime}
	if err = s.store.EJHSaveTrade(closed.ID, refund); err != nil {
		t.Fatal(err)
	}
	requests := []string{}
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
		requests = append(requests, method+":"+fmt.Sprint(data["orderNo"]))
		switch method {
		case "card.list.info":
			changed := remote
			changed.Note = "must not replace closed snapshot"
			return map[string]any{"cardInfos": []ejh.Card{changed, active, closing}}
		case "card.trade.info":
			if data["orderNo"] == "closed" {
				t.Error("closed card queried")
			}
			return map[string]any{"tradeInfos": []ejh.Trade{}}
		default:
			t.Errorf("unexpected issuer call: %s", method)
			return map[string]any{}
		}
	})
	for _, action := range []string{"balance", "refresh", "details"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"action":%q}`, action)))
		r.SetPathValue("id", closed.ID)
		w := httptest.NewRecorder()
		s.ejhCardAction(w, r)
		if w.Code != 200 {
			t.Fatal(action, w.Code, w.Body.String())
		}
		if action != "details" && strings.Contains(w.Body.String(), remote.Number) {
			t.Fatal("full PAN leaked")
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"start":"2026-10-01","end":"2026-10-15","page":1}`))
	r.SetPathValue("id", closed.ID)
	w := httptest.NewRecorder()
	s.ejh.mu.Lock()
	s.queryEJHCardTrades(w, r)
	s.ejh.mu.Unlock()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cached":true`) || !strings.Contains(w.Body.String(), "saved") || strings.Contains(w.Body.String(), remote.Number) {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(requests) != 0 {
		t.Fatal("closed read contacted supplier", requests)
	}
	cfg, _, err := s.store.EJHSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if _, err = s.store.SaveEJHSettings(cfg, ""); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"balance", "refresh", "details"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"action":%q}`, action)))
		r.SetPathValue("id", closed.ID)
		w := httptest.NewRecorder()
		s.ejhCardAction(w, r)
		if w.Code != 200 || len(requests) != 0 {
			t.Fatal("disabled interface blocked closed history", action, w.Code, requests)
		}
	}
	cfg.Enabled = true
	if _, err = s.store.SaveEJHSettings(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.syncEJHLocked(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatal("wrong refresh calls", requests)
	}
	var after ejh.CardRecord
	if err = s.store.EJHGet("card", closed.ID, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("closed snapshot modified")
	}
}

func TestEJHIncompleteCollectionDoesNotSavePartialPage(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	card := ejh.CardRecord{ID: "selected", Remote: ejh.Card{OrderNo: "selected"}}
	calls := 0
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
		calls++
		if method != "card.trade.info" || data["orderNo"] != "selected" {
			t.Fatal(method, data)
		}
		if calls == 2 {
			return map[string]any{}
		}
		rows := []ejh.Trade{}
		for i := 0; i < 100; i++ {
			rows = append(rows, ejh.Trade{OrderNo: "selected", TradeNo: fmt.Sprint(i), Type: "1"})
		}
		return map[string]any{"tradeInfos": rows}
	})
	cfg, key, err := s.ejhConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.collectEJHCardTrades(context.Background(), s.ejh.client, cfg, key, card, time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Fatal("missing second page accepted")
	}
	rows, err := store.EJHRecords[ejh.TradeRecord](s.store, "trade")
	if err != nil || len(rows) != 0 {
		t.Fatal("partial collection persisted", err)
	}
}

func TestEJHRefreshIsolatesCardFailuresAndReconcilesCompletedCards(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelRequest), func(t *testing.T) {
			s, _ := ejhTestServer(t, 200)
			now := time.Now()
			for i, number := range []string{"4242424242424242", "5555555555554444"} {
				card, err := s.store.ImportEJHCard(ejh.Card{OrderNo: fmt.Sprintf("card-%d", i), Number: number, CVV: "123", Validity: "1299", Status: "3"}, false)
				if err != nil {
					t.Fatal(err)
				}
				order := gptpay.Order{ID: fmt.Sprintf("payment-%d", i), Email: fmt.Sprintf("account-%d@example.com", i), CardID: card.ID, Status: "success", CreatedAt: now, UpdatedAt: now}
				snap := gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: gptpay.CardSecret{Number: number, Year: 2099, Month: 12, CVV: "123"}}}
				if err = s.store.CreateGPTPayOrder(order, snap); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, failFirst := 0, !cancelRequest
			s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
				if method == "card.list.info" {
					return map[string]any{"cardInfos": []ejh.Card{}}
				}
				if method != "card.trade.info" {
					t.Fatal(method)
				}
				calls++
				if calls == 1 && failFirst {
					return map[string]any{}
				}
				if cancelRequest {
					cancel()
				}
				id := fmt.Sprint(data["orderNo"])
				return map[string]any{"tradeInfos": []ejh.Trade{{OrderNo: id, TradeNo: "pay-" + id, Type: "1", Status: "1", CardAmount: "100.30", CardCurrency: "PHP", Merchant: "OPENAI", TradeTime: now.Format(time.RFC3339Nano)}}}
			})
			err := s.syncEJHLocked(ctx, now.Add(-time.Hour), now.Add(time.Minute))
			if err == nil || cancelRequest && !errors.Is(err, context.Canceled) {
				t.Fatal("missing refresh error", err)
			}
			wantCalls := 2
			if cancelRequest {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("queried %d cards, want %d", calls, wantCalls)
			}
			cases, err := s.store.EJHCases()
			if err != nil {
				t.Fatal(err)
			}
			matched := 0
			for _, c := range cases {
				if c.Charge != nil {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("completed card was not reconciled: %d", matched)
			}
			var state ejh.SyncState
			if err = s.store.EJHGet("sync", "latest", &state); err != nil || state.Error == "" || state.LastSuccess != nil {
				t.Fatal("partial refresh incorrectly marked successful", state, err)
			}
			if !cancelRequest {
				failFirst = false
				if err = s.syncEJHLocked(context.Background(), now.Add(-time.Hour), now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				cases, err = s.store.EJHCases()
				if err != nil || len(cases) != 2 || cases[0].Charge == nil || cases[1].Charge == nil {
					t.Fatal("retry did not recover", err)
				}
				if err = s.store.EJHGet("sync", "latest", &state); err != nil || state.Error != "" || state.LastSuccess == nil {
					t.Fatal("retry status not cleared", state, err)
				}
			}
		})
	}
}
