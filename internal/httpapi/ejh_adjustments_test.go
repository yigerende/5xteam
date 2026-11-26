package httpapi

import (
	"chatgpt-space-merge/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEJHAdjustmentAPI(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if _, e = st.SaveEJHRechargeLedger("100.00"); e != nil {
		t.Fatal(e)
	}
	if e = st.EJHPut("balance", "latest", map[string]any{"at": time.Now(), "data": map[string]any{"abroadList": []map[string]string{{"currency": "USD", "balance": "231.07"}}}}); e != nil {
		t.Fatal(e)
	}
	s := &Server{store: st, sessions: map[string]time.Time{"test": time.Now().Add(time.Hour)}}
	mux := http.NewServeMux()
	s.ejhRoutes(mux)
	handler := s.authMiddleware(mux)
	call := func(method, path, body string, auth bool, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if auth {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: "test"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/ejh/adjustments", "/api/ejh/adjustments/preview"} {
		if w := call("GET", path, "", false, ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := call("POST", "/api/ejh/adjustments", "{}", true, "https://evil.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	preview, e := s.readEJHAdjustmentPreview(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if preview.GapBefore != "-132.07" || preview.SuggestedAmount != "132.07" {
		t.Fatalf("preview %+v", preview)
	}
	body := `{"request_id":"http-adjustment-0001","note":"历史核对差额","expected_gap":"-132.07","expected_total":"0.00"}`
	for i := 0; i < 2; i++ {
		w := call("POST", "/api/ejh/adjustments", body, true, "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call("GET", "/api/ejh/adjustments", "", true, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var payload struct {
		Data struct {
			Total int                   `json:"total"`
			Items []store.EJHAdjustment `json:"items"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	if payload.Data.Total != 1 || payload.Data.Items[0].GapAfter != "0.00" {
		t.Fatal(w.Body.String())
	}
	after, e := s.readEJHAdjustmentPreview(context.Background())
	if e != nil || after.GapBefore != "0.00" || after.RawGap != "-132.07" {
		t.Fatal(after, e)
	}
	if w = call("POST", "/api/ejh/adjustments", strings.ReplaceAll(body, "0001", "0002"), true, ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	dashboard, e := s.readEJHDashboard(false)
	if e != nil {
		t.Fatal(e)
	}
	if dashboard["reconciliation_adjustments"].(store.EJHAdjustmentSummary).Amount != "132.07" {
		t.Fatal("dashboard missing adjustment")
	}
	ledger, _ := st.EJHRechargeLedger()
	if ledger.Amount != "100.00" {
		t.Fatal(ledger)
	}
	for _, path := range []string{"/api/ejh/adjustments?page=0", "/api/ejh/adjustments?page_size=101"} {
		if w = call("GET", path, "", true, ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
