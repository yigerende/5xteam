package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
	"crypto/des"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEJHCardTradesQuery(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	card := ejh.CardRecord{ID: "selected", Remote: ejh.Card{OrderNo: "selected-order"}}
	mode := "valid"
	calls := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/authenticate" {
			fmt.Fprint(w, `{"code":"000000","data":{"authToken":"fixture-token","validity":60000}}`)
			return
		}
		calls++
		var envelope map[string]string
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		cipher, err := base64.StdEncoding.DecodeString(envelope["param"])
		if err != nil || len(cipher) == 0 || len(cipher)%8 != 0 {
			t.Error("bad encryption")
			w.WriteHeader(500)
			return
		}
		block, _ := des.NewCipher([]byte("fixture-"))
		plain := make([]byte, len(cipher))
		for i := 0; i < len(cipher); i += 8 {
			block.Decrypt(plain[i:i+8], cipher[i:i+8])
		}
		var body struct {
			Method string `json:"method"`
			Data   struct {
				OrderNo string `json:"orderNo"`
				Start   string `json:"tradeTime"`
				End     string `json:"endTime"`
				Page    int    `json:"page"`
				Size    int    `json:"size"`
			} `json:"data"`
		}
		if err := json.Unmarshal(plain[:len(plain)-int(plain[len(plain)-1])], &body); err != nil {
			t.Error(err)
		}
		if body.Method != "card.trade.info" || body.Data.OrderNo != "selected-order" || body.Data.Start != "2026-10-01 00:00:00" || body.Data.End != "2026-10-15 23:59:59" || (mode != "paged" && body.Data.Page != 2) || body.Data.Size != 100 {
			t.Errorf("bad card query: %+v", body)
		}
		trade := ejh.Trade{OrderNo: card.Remote.OrderNo, TradeNo: mode, Type: "4", Status: "2", Number: "4242424242424242", CardAmount: "100", CardCurrency: "USD"}
		rows := []ejh.Trade{trade}
		switch mode {
		case "paged":
			rows = []ejh.Trade{}
			for i := (body.Data.Page - 1) * 100; i < min(body.Data.Page*100, 625); i++ {
				v := trade
				v.TradeNo = fmt.Sprintf("paged-%04d", i)
				rows = append(rows, v)
			}
		case "missing":
			fmt.Fprint(w, `{"code":"000000","data":{}}`)
			return
		case "null":
			rows = nil
		case "empty":
			rows = []ejh.Trade{}
		case "wrong_card":
			rows = append(rows, trade)
			rows[1].OrderNo = "different-card"
		case "missing_id":
			rows[0].TradeNo = ""
		case "duplicate":
			rows = append(rows, trade)
		case "full", "oversized":
			n := 100
			if mode == "oversized" {
				n = 101
			}
			rows = nil
			for i := 0; i < n; i++ {
				v := trade
				v.TradeNo = fmt.Sprintf("page-%d", i)
				rows = append(rows, v)
			}
		case "error":
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]any{"tradeInfos": rows, "totalSize": len(rows)}})
	}))
	defer up.Close()
	cfg, _, err := s.store.EJHSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.URL = up.URL
	if _, err = s.store.SaveEJHSettings(cfg, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	s.ejh.client = ejh.New()
	s.ejh.client.HTTP = up.Client()
	if err := s.store.EJHPut("card", card.ID, card); err != nil {
		t.Fatal(err)
	}
	query := func(body, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/ejh/cards/"+id+"/trades", strings.NewReader(body))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		s.queryEJHCardTrades(w, r)
		return w
	}
	valid := `{"start":"2026-10-01","end":"2026-10-15","page":2,"page_size":100}`
	for _, tc := range []struct {
		mode string
		code int
		more bool
	}{{"valid", 200, false}, {"full", 200, true}, {"empty", 200, false}, {"missing", 502, false}, {"null", 502, false}, {"wrong_card", 502, false}, {"missing_id", 502, false}, {"duplicate", 502, false}, {"oversized", 502, false}, {"error", 502, false}} {
		t.Run(tc.mode, func(t *testing.T) {
			mode = tc.mode
			before, _ := store.EJHRecords[ejh.TradeRecord](s.store, "trade")
			w := query(valid, card.ID)
			if w.Code != tc.code {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "4242424242424242") {
				t.Fatal("full card number leaked")
			}
			if tc.code == 200 {
				var out struct {
					Data struct {
						Page int  `json:"page"`
						More bool `json:"has_more"`
					}
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.Data.Page != 2 || out.Data.More != tc.more || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("bad pagination or caching")
				}
			} else {
				after, _ := store.EJHRecords[ejh.TradeRecord](s.store, "trade")
				if len(after) != len(before) {
					t.Fatal("invalid page partially saved")
				}
			}
		})
	}
	mode = "paged"
	for _, tc := range []struct {
		size, page, count, first int
		more                     bool
	}{
		{10, 2, 10, 10, true}, {50, 2, 50, 50, true}, {50, 3, 50, 100, true},
		{100, 2, 100, 100, true}, {500, 1, 500, 0, true}, {500, 2, 125, 500, false}, {500, 3, 0, 1000, false},
	} {
		w := query(fmt.Sprintf(`{"start":"2026-10-01","end":"2026-10-15","page":%d,"page_size":%d}`, tc.page, tc.size), card.ID)
		var result struct {
			Data struct {
				Items    []ejh.Trade
				PageSize int  `json:"page_size"`
				More     bool `json:"has_more"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(result.Data.Items) != tc.count || result.Data.PageSize != tc.size || result.Data.More != tc.more {
			t.Fatalf("size=%d page=%d: %s", tc.size, tc.page, w.Body.String())
		}
		for i, item := range result.Data.Items {
			if item.TradeNo != fmt.Sprintf("paged-%04d", tc.first+i) {
				t.Fatalf("skipped/duplicate trade: %+v", item)
			}
		}
	}
	settings := s.store.Settings()
	settings.DefaultPageSize = 50
	if err := s.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	wDefault := query(`{"start":"2026-10-01","end":"2026-10-15","page":1}`, card.ID)
	var defaults struct {
		Data struct {
			Items    []ejh.Trade
			PageSize int `json:"page_size"`
		}
	}
	if err := json.Unmarshal(wDefault.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	}
	if wDefault.Code != 200 || defaults.Data.PageSize != 50 || len(defaults.Data.Items) != 50 {
		t.Fatal("missing size did not inherit settings", wDefault.Body.String())
	}
	before := calls
	for _, body := range []string{`{"start":"2026-10-01","end":"2026-10-15","page":1,"page_size":30}`, `{"start":"2026-01-01","end":"2026-04-01","page":1}`, `{"start":"2026-10-16","end":"2026-10-15","page":1}`, `{"start":"bad","end":"2026-10-15","page":1}`, `{"start":"2026-10-01","end":"2026-10-15","page":0}`} {
		if w := query(body, card.ID); w.Code != 400 {
			t.Fatal("invalid range/page accepted", w.Code)
		}
	}
	if w := query(valid, "unknown"); w.Code != 404 {
		t.Fatal("unknown card accepted")
	}
	if calls != before {
		t.Fatal("invalid requests reached supplier")
	}
	s.ejh.mu.Lock()
	w := query(valid, card.ID)
	s.ejh.mu.Unlock()
	if w.Code != 200 || calls != before+1 {
		t.Fatal("background work blocked read-only trades", w.Code, w.Body.String())
	}
	records, err := store.EJHRecords[ejh.TradeRecord](s.store, "trade")
	if err != nil || len(records) != 0 {
		t.Fatal("browsing changed saved transactions", err)
	}
}

func TestEJHCardTradesConcurrentDuringBackgroundWork(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var authCalls atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/authenticate" {
			authCalls.Add(1)
			fmt.Fprint(w, `{"code":"000000","data":{"authToken":"fixture-token","validity":60000}}`)
			return
		}
		started <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, `{"code":"000000","data":{"tradeInfos":[]}}`)
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(release)
	cfg, _, err := s.store.EJHSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.URL = up.URL
	if _, err = s.store.SaveEJHSettings(cfg, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	s.ejh.client = ejh.New()
	s.ejh.client.HTTP = up.Client()
	card := ejh.CardRecord{ID: "concurrent", Remote: ejh.Card{OrderNo: "concurrent-order"}}
	if err = s.store.EJHPut("card", card.ID, card); err != nil {
		t.Fatal(err)
	}
	// Neither the application's database connection nor the mutation lock
	// should be needed while reading details from an independent snapshot.
	if err = s.store.Close(); err != nil {
		t.Fatal(err)
	}
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{"start":"2026-10-01","end":"2026-10-15","page":1}`))
			r.SetPathValue("id", card.ID)
			w := httptest.NewRecorder()
			s.queryEJHCardTrades(w, r)
			results <- w
		}()
	}
	for range 2 {
		select {
		case <-started:
		case w := <-results:
			t.Fatalf("query rejected before reaching issuer: %d %s", w.Code, w.Body.String())
		case <-time.After(5 * time.Second):
			t.Fatal("queries blocked each other or background work")
		}
	}
	if authCalls.Load() != 1 {
		t.Fatal("concurrent reads did not share authenticated client")
	}
	// Release both issuer requests without unlocking the background operation.
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		select {
		case w := <-results:
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("query did not finish")
		}
	}
}
