package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatgpt-space-merge/internal/store"
)

func TestEJHFinanceReadOnlyAndAuthentication(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, sessions: map[string]time.Time{"finance-test": time.Now().Add(time.Hour)}}
	mux := http.NewServeMux()
	s.ejhRoutes(mux)
	handler := s.authMiddleware(mux)
	for _, authorized := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/api/ejh/finance", nil)
		if authorized {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: "finance-test"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if !authorized && w.Code != 401 || authorized && w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	snapshots, err := store.EJHRecords[store.EJHFinancialSnapshot](st, "finance_snapshot")
	if err != nil || len(snapshots) != 0 || s.ejh.client != nil {
		t.Fatal("GET mutated history or called supplier")
	}
	s.captureEJHFinance()
	w := httptest.NewRecorder()
	s.ejhFinance(w, httptest.NewRequest("GET", "/", nil))
	var result struct {
		Data struct {
			Recording ejhFinanceRecording          `json:"recording"`
			Snapshots []store.EJHFinancialSnapshot `json:"snapshots"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Data.Recording.LastSuccess == nil || len(result.Data.Snapshots) != 2 {
		t.Fatal(w.Body.String(), err)
	}
	if s.ejh.client != nil {
		t.Fatal("recorder called supplier")
	}
}

func TestEJHFinanceCompactSkipsHistoricalSnapshots(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// An unreadable old snapshot must not affect the dated ledger report.
	if err = st.EJHPut("finance_snapshot", "2026-10-01:first", map[string]string{"at": "invalid"}); err != nil {
		t.Fatal(err)
	}
	if err = st.EJHPut("finance_usage_day", "usage:2026-11-16", store.EJHConsumptionDay{ID: "usage:2026-11-16", Day: "2026-11-16", Amount: 123.45, Source: "sub2_daily"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	w := httptest.NewRecorder()
	s.ejhFinance(w, httptest.NewRequest("GET", "/api/ejh/finance?snapshots=none", nil))
	var result struct {
		Data struct {
			Snapshots []store.EJHFinancialSnapshot `json:"snapshots"`
			Days      []store.EJHConsumptionDay    `json:"usage_days"`
		} `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data.Snapshots) != 0 || len(result.Data.Days) != 1 || result.Data.Days[0].Amount != 123.45 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.ejh.client != nil {
		t.Fatal("compact report must not query issuer")
	}
}
