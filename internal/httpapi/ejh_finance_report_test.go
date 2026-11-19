package httpapi

import (
	"chatgpt-space-merge/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEJHFinanceReportHTTP(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node runtime not installed in test environment")
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, sessions: map[string]time.Time{"finance": time.Now().Add(time.Hour)}}
	mux := http.NewServeMux()
	s.ejhRoutes(mux)
	handler := s.authMiddleware(mux)
	read := func(query string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/ejh/finance/report?"+query, nil)
		if auth {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: "finance"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := read("mode=day&date=2026-11-19", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, q := range []string{"mode=invalid&date=2026-11-19", "mode=day&date=2026-02-30", "mode=all&date=2026-11-19&section=bad", "mode=day&date=2026-11-19&section=events&page_size=501", "mode=day&date=2026-11-19&section=events&page=0"} {
		if w := read(q, true); w.Code != 400 {
			t.Fatal(q, w.Code)
		}
	}
	w := read("mode=day&date=2026-11-19", true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data["events"] != nil || out.Data["openedAccountRows"] != nil || out.Data["summary"] == nil {
		t.Fatal("summary must exclude account and event details")
	}
	var revision string
	json.Unmarshal(out.Data["revision"], &revision)
	s.ejh.finance.mu.Lock()
	report := s.ejh.finance.cache["day/2026-11-19/"+revision]
	report.events = []map[string]any{{"id": "r1", "kind": "clearing", "refundPrincipal": true, "at": float64(time.Date(2026, 11, 19, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).UnixMilli()), "email": "refund@example.com"}, {"id": "r2", "kind": "refund", "month": "2026-11", "email": "fee@example.com"}, {"id": "r3", "kind": "reversal", "at": nil}}
	report.accounts = []map[string]any{{"email": "a@example.com", "plan": "pro5", "last4": "1234"}, {"email": "b@example.com", "plan": "pro20", "last4": "5678"}}
	s.ejh.finance.mu.Unlock()
	base := "mode=day&date=2026-11-19&revision=" + url.QueryEscape(revision)
	for _, test := range []struct {
		q, id string
		total int
	}{
		{"&section=events&kind=refund_credit&day=2026-11-19", "r1", 1},
		{"&section=events&kind=clearing", "", 0},
		{"&section=events&day=" + url.QueryEscape("2026-11（日期未记录）"), "r2", 1},
		{"&section=events&page_size=1&page=99", "r3", 3},
		{"&section=events&query=FEE%40EXAMPLE", "r2", 1},
		{"&section=accounts&query=Pro%205x", "", 1},
	} {
		w = read(base+test.q, true)
		if w.Code != 200 {
			t.Fatal(test.q, w.Body.String())
		}
		var result struct {
			Data struct {
				Items []map[string]any `json:"items"`
				Total int              `json:"total"`
			} `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		if result.Data.Total != test.total || test.id != "" && (len(result.Data.Items) != 1 || result.Data.Items[0]["id"] != test.id) {
			t.Fatal(test.q, w.Body.String())
		}
	}
	if w = read("mode=day&date=2026-11-19&section=accounts&revision=evicted", true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if err = st.EJHPut("finance_usage_day", "usage:2026-11-19", store.EJHConsumptionDay{ID: "usage:2026-11-19", Day: "2026-11-19", Amount: 15, Source: "sub2_daily"}); err != nil {
		t.Fatal(err)
	}
	next, err := st.EJHFinanceRevision(t.Context())
	if err != nil || next == revision {
		t.Fatal("daily evidence did not invalidate report", err)
	}
	if w = read(base+"&section=events&kind=refund_credit", true); w.Code != 200 {
		t.Fatal("existing summary details must remain pinned", w.Body.String())
	}
	w = read("mode=day&date=2026-11-19", true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.ejh.client != nil {
		t.Fatal("report called card supplier")
	}
	snapshots, _ := store.EJHRecords[store.EJHFinancialSnapshot](st, "finance_snapshot")
	if len(snapshots) != 0 {
		t.Fatal("report mutated finance records")
	}
}

func TestEJHFinanceReportSharedCalculation(t *testing.T) {
	var c ejhFinanceReports
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	fn := func(context.Context) (*ejhFinanceReportData, error) {
		calls.Add(1)
		close(started)
		<-release
		return &ejhFinanceReportData{revision: "one"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.read(ctx, "one", fn); done <- err }()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled reader did not leave")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.read(t.Context(), "one", fn)
			if err != nil || v.revision != "one" {
				t.Error(err)
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate calculation", calls.Load())
	}
	for i := 0; i < 12; i++ {
		key := string(rune('a' + i))
		_, err := c.read(t.Context(), key, func(context.Context) (*ejhFinanceReportData, error) { return &ejhFinanceReportData{}, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(c.cache) != 8 || len(c.order) != 8 {
		t.Fatal("unbounded cache")
	}
}
